package middleware

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// Per-network limits of the registration pages. Each mark is a separate
// counter: registration submissions (including sign-ups through third-party
// login), registration email code requests and captcha images.
const (
	RegisterAttemptRateLimitMark = "RG"
	RegisterCodeRateLimitMark    = "RV"
	CaptchaRateLimitMark         = "CP"
	CaptchaRateLimitNum          = 30
	CaptchaRateLimitDuration     = 15 * 60 // seconds

	// registerRateLimitKeyExpiration keeps an idle key in the in-memory
	// fallback for the longest registration window an administrator can set.
	registerRateLimitKeyExpiration = operation_setting.RegisterIPRateLimitMaxMinutes * time.Minute
)

// registerRateLimiter is the in-memory store of these limits. It is separate
// from inMemoryRateLimiter, which forgets a key after 20 idle minutes, because
// a registration window can last a whole day.
var registerRateLimiter = new(common.InMemoryRateLimiter)

// registerLimit is one of the administrator-configured registration limits.
type registerLimit struct {
	mark       string
	code       string
	messageKey string
}

var (
	registerAttemptLimit = registerLimit{
		mark:       RegisterAttemptRateLimitMark,
		code:       "REGISTER_RATE_LIMITED",
		messageKey: "Too many registration attempts from your network. Please try again in {{minutes}} minutes.",
	}
	registerCodeLimit = registerLimit{
		mark:       RegisterCodeRateLimitMark,
		code:       "REGISTER_CODE_RATE_LIMITED",
		messageKey: "Too many verification code requests from your network. Please try again in {{minutes}} minutes.",
	}
)

// take counts one request of the client network while the registration limit
// is on. It returns 0 when the request is allowed, otherwise the seconds until
// the network may try again. The settings are read on every request, so a
// change applies without a restart.
func (limit registerLimit) take(c *gin.Context) int64 {
	if !common.RegisterIPRateLimitEnabled {
		return 0
	}
	// Saved values are validated; a hand-edited row is clamped into range.
	maxRequests := min(max(common.RegisterIPRateLimitNum, 1), operation_setting.RegisterIPRateLimitMaxNum)
	minutes := min(max(common.RegisterIPRateLimitMinutes, 1), operation_setting.RegisterIPRateLimitMaxMinutes)
	allowed, retryAfter := takeClientNetworkLimit(c, limit.mark, maxRequests, int64(minutes)*60)
	if allowed {
		return 0
	}
	return retryAfter
}

// reject answers a refused request with 429, the limit's code and the whole
// minutes left to wait.
func (limit registerLimit) reject(c *gin.Context, retryAfter int64) {
	minutes := max((retryAfter+59)/60, 1)
	message := common.NewMessage(limit.messageKey, map[string]any{"minutes": minutes})
	writeClientNetworkRateLimited(c, retryAfter, message, gin.H{"code": limit.code})
}

func (limit registerLimit) handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if retryAfter := limit.take(c); retryAfter > 0 {
			limit.reject(c, retryAfter)
			return
		}
		c.Next()
	}
}

// RegisterAttemptRateLimit counts every registration submission of the client
// network (RG). It runs before any other check of the request, so wrong
// captchas, wrong codes and taken usernames all count.
func RegisterAttemptRateLimit() gin.HandlerFunc {
	return registerAttemptLimit.handler()
}

// RegisterCodeRateLimit counts every registration email code request of the
// client network (RV).
func RegisterCodeRateLimit() gin.HandlerFunc {
	return registerCodeLimit.handler()
}

// TakeRegisterAttempt counts a sign-up through a third-party login that is
// about to create an account, on the same counter as registration
// submissions. It returns 0 when the sign-up is allowed, otherwise the
// seconds to pass to RejectRegisterAttempt.
func TakeRegisterAttempt(c *gin.Context) int64 {
	return registerAttemptLimit.take(c)
}

// RejectRegisterAttempt answers a sign-up that TakeRegisterAttempt refused.
func RejectRegisterAttempt(c *gin.Context, retryAfter int64) {
	registerAttemptLimit.reject(c, retryAfter)
}

// CaptchaRateLimit limits captcha images per client network (CP), whatever
// the registration settings are.
func CaptchaRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, retryAfter := takeClientNetworkLimit(c, CaptchaRateLimitMark, CaptchaRateLimitNum, CaptchaRateLimitDuration)
		if !allowed {
			writeClientNetworkRateLimited(c, retryAfter, common.NewMessage("Too many captcha requests. Please try again later."))
			return
		}
		c.Next()
	}
}

// clientNetworkKey identifies the client network of an address: an IPv4
// address as is, an IPv6 address by its /64 prefix, which one subscriber
// usually controls as a whole.
func clientNetworkKey(clientIP string) string {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return clientIP
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// takeClientNetworkLimit counts one request of mark for the client network in
// a Redis fixed window, or in registerRateLimiter when Redis is off or
// failing. It reports whether the request is allowed and, when it is not,
// the seconds until the window frees a slot.
func takeClientNetworkLimit(c *gin.Context, mark string, maxRequests int, duration int64) (bool, int64) {
	key := clientNetworkKey(c.ClientIP())
	if common.RedisEnabled {
		allowed, _, ttlSeconds, err := redisFixedWindowTake(c.Request.Context(), redisIPRateLimitKey(mark, key), maxRequests, duration)
		if err == nil {
			if allowed {
				return true, 0
			}
			if ttlSeconds <= 0 {
				ttlSeconds = duration
			}
			return false, ttlSeconds
		}
		logger.LogWarn(c.Request.Context(), common.LogText("rate limit check failed, using the in-memory limiter (mark=%s): %v", mark, err))
	}
	registerRateLimiter.Init(registerRateLimitKeyExpiration)
	return registerRateLimiter.RequestWithRetryAfter(mark+":"+key, maxRequests, duration)
}

// writeClientNetworkRateLimited rejects a request with 429, Retry-After and a
// web console message.
func writeClientNetworkRateLimited(c *gin.Context, retryAfter int64, message *common.Message, fields ...gin.H) {
	c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
	c.Header("Cache-Control", "no-store")
	common.ApiErrorStatus(c, http.StatusTooManyRequests, message, fields...)
	c.Abort()
}
