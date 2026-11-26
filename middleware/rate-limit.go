package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

const redisRateLimitNamespace = "rateLimit:v2"

// Redis rate limiting intentionally uses a fixed window. The single Lua script
// makes increment, expiry, and the limit decision atomic, while retaining the
// simple fixed-window behavior: traffic at a window boundary can burst up to
// twice the configured limit. Do not replace this with a sliding-window ZSET
// unless that externally visible behavior is intentionally changed.
//
// A counter without an expiry is given one. So is a counter with more time
// left than the window: its window was longer when it started, and the
// administrator has shortened it since (the registration limits read their
// window per request), so the shorter window applies at once, and so does
// the wait it reports. A lengthened window applies from the next window. A
// limiter with a constant window is not affected: its counters never have
// more than the window left.
const redisFixedWindowScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
local ttl = redis.call('TTL', KEYS[1])
if ttl < 0 or ttl > tonumber(ARGV[2]) then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
  ttl = redis.call('TTL', KEYS[1])
end
if count > tonumber(ARGV[1]) then
  return {0, count, ttl}
end
return {1, count, ttl}
`

var inMemoryRateLimiter common.InMemoryRateLimiter

var defNext = func(c *gin.Context) {
	c.Next()
}

func redisIPRateLimitKey(mark string, clientIP string) string {
	return fmt.Sprintf("%s:ip:%s:%s", redisRateLimitNamespace, mark, clientIP)
}

func redisUserRateLimitKey(mark string, userID int) string {
	return fmt.Sprintf("%s:user:%s:%d", redisRateLimitNamespace, mark, userID)
}

func redisReplyInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected Redis integer reply type %T", value)
	}
}

func redisFixedWindowTake(ctx context.Context, key string, maxRequestNum int, duration int64) (bool, int64, int64, error) {
	if common.RDB == nil {
		return false, 0, 0, errors.New("Redis client is not initialized")
	}
	if key == "" {
		return false, 0, 0, errors.New("rate limit key is empty")
	}
	if maxRequestNum <= 0 {
		return false, 0, 0, errors.New("rate limit maximum must be positive")
	}
	if duration <= 0 {
		return false, 0, 0, errors.New("rate limit duration must be positive")
	}

	values, err := common.RDB.Eval(
		ctx,
		redisFixedWindowScript,
		[]string{key},
		maxRequestNum,
		duration,
	).Slice()
	if err != nil {
		return false, 0, 0, err
	}
	if len(values) != 3 {
		return false, 0, 0, fmt.Errorf("unexpected Redis rate limit reply length %d", len(values))
	}

	allowedValue, err := redisReplyInteger(values[0])
	if err != nil {
		return false, 0, 0, err
	}
	count, err := redisReplyInteger(values[1])
	if err != nil {
		return false, 0, 0, err
	}
	ttlSeconds, err := redisReplyInteger(values[2])
	if err != nil {
		return false, 0, 0, err
	}

	return allowedValue == 1, count, ttlSeconds, nil
}

func redisRateLimiter(c *gin.Context, maxRequestNum int, duration int64, mark string) {
	allowed, _, ttlSeconds, err := redisFixedWindowTake(
		c.Request.Context(),
		redisIPRateLimitKey(mark, c.ClientIP()),
		maxRequestNum,
		duration,
	)
	if err != nil {
		if common.RateLimitRedisFailure == common.RateLimitRedisFailureReject {
			logger.LogError(c.Request.Context(), common.LogText("rate limit check failed (mark=%s): %v", mark, err))
			c.Status(http.StatusInternalServerError)
			c.Abort()
			return
		}
		logRedisRateLimitFallback(c, mark, err)
		memoryFallbackRateLimit(c, mark+c.ClientIP(), maxRequestNum, duration)
		return
	}
	if !allowed {
		writeRateLimited(c, ttlSeconds)
	}
}

// memoryFallbackRateLimit counts a request that Redis could not count in the
// in-memory limiter, under the key the memory backend uses, so the limit
// keeps holding on this node while Redis fails.
func memoryFallbackRateLimit(c *gin.Context, key string, maxRequestNum int, duration int64) {
	allowed, retryAfter := inMemoryRateLimiter.RequestWithRetryAfter(key, maxRequestNum, duration)
	if !allowed {
		writeRateLimited(c, retryAfter)
	}
}

// redisFallbackLogInterval spaces the log lines of one limiter while Redis is
// failing: a busy site would otherwise write a line for every request.
const redisFallbackLogInterval = 10 * time.Second

// redisFallbackLogNow is the clock of the fallback log; tests replace it.
var redisFallbackLogNow = time.Now

// redisFallbackLog is when a limiter last logged a Redis failure (Unix
// nanoseconds) and how many failures it left out since.
type redisFallbackLog struct {
	loggedAt atomic.Int64
	skipped  atomic.Int64
}

var redisFallbackLogs sync.Map // limiter mark -> *redisFallbackLog

// logRedisRateLimitFallback logs that the limiter mark counts in memory
// because Redis failed: the first failure, then at most one line per
// redisFallbackLogInterval, saying how many failures were left out.
func logRedisRateLimitFallback(c *gin.Context, mark string, err error) {
	value, _ := redisFallbackLogs.LoadOrStore(mark, &redisFallbackLog{})
	entry := value.(*redisFallbackLog)
	now := redisFallbackLogNow().UnixNano()
	last := entry.loggedAt.Load()
	if (last != 0 && now-last < int64(redisFallbackLogInterval)) || !entry.loggedAt.CompareAndSwap(last, now) {
		entry.skipped.Add(1)
		return
	}
	logger.LogWarn(c.Request.Context(), common.LogText(
		"rate limit check failed, using the in-memory limiter (mark=%s, %d more failures since the last line): %v",
		mark, entry.skipped.Swap(0), err,
	))
}

func memoryRateLimiter(c *gin.Context, maxRequestNum int, duration int64, mark string) {
	key := mark + c.ClientIP()
	if !inMemoryRateLimiter.Request(key, maxRequestNum, duration) {
		writeRateLimited(c, duration)
		return
	}
}

// writeRateLimited rejects the request with 429 and a Retry-After hint so
// clients can back off instead of treating the rejection as a fatal error.
// The in-memory limiter cannot report the remaining window, so callers
// without a TTL pass the full window duration as a conservative upper bound.
func writeRateLimited(c *gin.Context, retryAfterSeconds int64) {
	if retryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
	}
	// Never let an upstream Cache() header make a 429 cacheable.
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusTooManyRequests)
	c.Abort()
}

func rateLimitFactory(maxRequestNum int, duration int64, mark string) func(c *gin.Context) {
	// It's safe to call multi times. The Redis limiter falls back to it when
	// Redis fails, so it is ready before the first request either way.
	inMemoryRateLimiter.Init(common.RateLimitKeyExpirationDuration)
	if common.RedisEnabled {
		return func(c *gin.Context) {
			redisRateLimiter(c, maxRequestNum, duration, mark)
		}
	}
	return func(c *gin.Context) {
		memoryRateLimiter(c, maxRequestNum, duration, mark)
	}
}

// GlobalWebRateLimit limits dashboard pages and other fallback paths per IP.
// Files that exist in the frontend build count against a separate static
// limiter instead, so lazy-loaded chunks and icons do not use up the page
// budget. The static limiter is disabled by default.
func GlobalWebRateLimit(frontendFS static.ServeFileSystem) func(c *gin.Context) {
	webLimit := defNext
	if common.GlobalWebRateLimitEnable {
		webLimit = webTierRateLimit(common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration, "GW")
	}
	staticLimit := defNext
	if common.GlobalStaticRateLimitEnable {
		staticLimit = webTierRateLimit(common.GlobalStaticRateLimitNum, common.GlobalStaticRateLimitDuration, "GS")
	}
	return func(c *gin.Context) {
		if frontendFS.Exists("/", c.Request.URL.Path) {
			staticLimit(c)
			return
		}
		webLimit(c)
	}
}

// webTierRateLimit is rateLimitFactory for page and frontend-file requests,
// except that it fails open: a Redis hiccup must not turn every dashboard
// page load into an error page. API limiters count in memory instead (or
// answer 500 with RATE_LIMIT_REDIS_FAILURE=reject).
func webTierRateLimit(maxRequestNum int, duration int64, mark string) func(c *gin.Context) {
	if !common.RedisEnabled {
		return rateLimitFactory(maxRequestNum, duration, mark)
	}
	return func(c *gin.Context) {
		allowed, _, ttlSeconds, err := redisFixedWindowTake(
			c.Request.Context(),
			redisIPRateLimitKey(mark, c.ClientIP()),
			maxRequestNum,
			duration,
		)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("rate limit check failed (mark=%s): %v", mark, err))
			return
		}
		if !allowed {
			writeRateLimited(c, ttlSeconds)
		}
	}
}

func GlobalAPIRateLimit() func(c *gin.Context) {
	if common.GlobalApiRateLimitEnable {
		return rateLimitFactory(common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration, "GA")
	}
	return defNext
}

func CriticalRateLimit() func(c *gin.Context) {
	if common.CriticalRateLimitEnable {
		return rateLimitFactory(common.CriticalRateLimitNum, common.CriticalRateLimitDuration, "CT")
	}
	return defNext
}

// AuthRefreshRateLimit limits session refreshes per IP in a budget of their
// own (RF, AUTH_REFRESH_RATE_LIMIT*). Every hard page load of a signed-in
// user refreshes the session; in the critical budget, about 20 page loads
// from one IP signed the user out and then left sign-in itself rate limited.
// Sign-in, logout and the other critical routes keep CriticalRateLimit.
func AuthRefreshRateLimit() func(c *gin.Context) {
	if common.AuthRefreshRateLimitEnable {
		return rateLimitFactory(common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration, "RF")
	}
	return defNext
}

func UserCriticalRateLimit(scope string) func(c *gin.Context) {
	if !common.CriticalRateLimitEnable {
		return defNext
	}
	return userRateLimitFactory(
		common.CriticalRateLimitNum,
		common.CriticalRateLimitDuration,
		"UC:"+scope,
	)
}

func DownloadRateLimit() func(c *gin.Context) {
	return rateLimitFactory(common.DownloadRateLimitNum, common.DownloadRateLimitDuration, "DW")
}

func UploadRateLimit() func(c *gin.Context) {
	return rateLimitFactory(common.UploadRateLimitNum, common.UploadRateLimitDuration, "UP")
}

// userRateLimitFactory creates a rate limiter keyed by authenticated user ID
// instead of client IP, making it resistant to proxy rotation attacks.
// Must be used AFTER authentication middleware (UserAuth).
func userRateLimitFactory(maxRequestNum int, duration int64, mark string) func(c *gin.Context) {
	// It's safe to call multi times. The Redis limiter falls back to it when
	// Redis fails, so it is ready before the first request either way.
	inMemoryRateLimiter.Init(common.RateLimitKeyExpirationDuration)
	if common.RedisEnabled {
		return func(c *gin.Context) {
			userID := c.GetInt("id")
			if userID == 0 {
				c.Status(http.StatusUnauthorized)
				c.Abort()
				return
			}
			userRedisRateLimiter(c, maxRequestNum, duration, mark, userID)
		}
	}
	return func(c *gin.Context) {
		userID := c.GetInt("id")
		if userID == 0 {
			c.Status(http.StatusUnauthorized)
			c.Abort()
			return
		}
		if !inMemoryRateLimiter.Request(memoryUserRateLimitKey(mark, userID), maxRequestNum, duration) {
			writeRateLimited(c, duration)
			return
		}
	}
}

// memoryUserRateLimitKey is the in-memory key of a per-user limiter.
func memoryUserRateLimitKey(mark string, userID int) string {
	return fmt.Sprintf("%s:user:%d", mark, userID)
}

// userRedisRateLimiter is redisRateLimiter for a per-user limiter: it counts
// the user instead of the client IP.
func userRedisRateLimiter(c *gin.Context, maxRequestNum int, duration int64, mark string, userID int) {
	key := redisUserRateLimitKey(mark, userID)
	allowed, _, ttlSeconds, err := redisFixedWindowTake(c.Request.Context(), key, maxRequestNum, duration)
	if err != nil {
		if common.RateLimitRedisFailure == common.RateLimitRedisFailureReject {
			logger.LogError(c.Request.Context(), common.LogText("rate limit check failed (key=%s): %v", key, err))
			c.Status(http.StatusInternalServerError)
			c.Abort()
			return
		}
		logRedisRateLimitFallback(c, mark, err)
		memoryFallbackRateLimit(c, memoryUserRateLimitKey(mark, userID), maxRequestNum, duration)
		return
	}
	if !allowed {
		writeRateLimited(c, ttlSeconds)
	}
}

// SearchRateLimit returns a per-user rate limiter for search endpoints.
// Configurable via SEARCH_RATE_LIMIT_ENABLE / SEARCH_RATE_LIMIT / SEARCH_RATE_LIMIT_DURATION.
func SearchRateLimit() func(c *gin.Context) {
	if !common.SearchRateLimitEnable {
		return defNext
	}
	return userRateLimitFactory(common.SearchRateLimitNum, common.SearchRateLimitDuration, "SR")
}
