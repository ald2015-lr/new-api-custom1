package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useRateLimitMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()

	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())

	common.RedisEnabled = true
	common.RDB = redisClient
	t.Cleanup(func() {
		_ = redisClient.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})

	return redisServer, redisClient
}

func performRateLimitRequest(router http.Handler, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestRedisIPRateLimiterThresholdTTLAndNamespace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/limited", rateLimitFactory(2, 37, "TEST"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	remoteAddr := "192.0.2.10:12345"
	legacyKey := "rateLimit:TEST192.0.2.10"
	_, err := redisServer.Push(legacyKey, "legacy-list-entry")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/limited", remoteAddr).Code)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/limited", remoteAddr).Code)
	limitedResponse := performRateLimitRequest(router, "/limited", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, limitedResponse.Code)
	assert.Equal(t, "37", limitedResponse.Header().Get("Retry-After"))
	assert.Equal(t, "no-store", limitedResponse.Header().Get("Cache-Control"))

	key := redisIPRateLimitKey("TEST", "192.0.2.10")
	count, err := redisServer.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "3", count)
	assert.Equal(t, 37*time.Second, redisServer.TTL(key))
	assert.True(t, redisServer.Exists(legacyKey), "the v2 counter must not touch an old list key")
}

func TestRedisUserRateLimiterUsesSharedFixedWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)

	router := gin.New()
	router.GET(
		"/limited",
		func(c *gin.Context) { c.Set("id", 42) },
		userRateLimitFactory(1, 23, "USER"),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)

	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/limited", "192.0.2.20:12345").Code)
	assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/limited", "198.51.100.20:12345").Code)

	key := redisUserRateLimitKey("USER", 42)
	assert.True(t, redisServer.Exists(key))
	assert.Equal(t, 23*time.Second, redisServer.TTL(key))
}

func TestRedisEmailVerificationRateLimiterPreservesResponseAndTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/verify", EmailVerificationRateLimit(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	remoteAddr := "192.0.2.30:12345"
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/verify", remoteAddr).Code)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/verify", remoteAddr).Code)
	response := performRateLimitRequest(router, "/verify", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.JSONEq(t, `{"success":false,"message":"Please wait 30 seconds before requesting another verification code.","message_key":"Please wait {{seconds}} seconds before requesting another verification code.","message_params":{"seconds":30}}`, response.Body.String())

	key := redisIPRateLimitKey(EmailVerificationRateLimitMark, "192.0.2.30")
	assert.True(t, redisServer.Exists(key))
	assert.Equal(t, time.Duration(EmailVerificationDuration)*time.Second, redisServer.TTL(key))
}

func TestRedisFixedWindowIsAtomicUnderConcurrency(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	const (
		requestCount = 20
		maximumCount = 7
		duration     = int64(41)
	)
	key := redisIPRateLimitKey("CONCURRENT", "192.0.2.40")

	var allowedCount atomic.Int64
	errorsFound := make(chan error, requestCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(requestCount)
	for range requestCount {
		go func() {
			defer waitGroup.Done()
			allowed, _, _, err := redisFixedWindowTake(context.Background(), key, maximumCount, duration)
			if err != nil {
				errorsFound <- err
				return
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	waitGroup.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}

	assert.Equal(t, int64(maximumCount), allowedCount.Load())
	count, err := redisServer.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "20", count)
	assert.Equal(t, time.Duration(duration)*time.Second, redisServer.TTL(key))
}

func TestRedisFixedWindowResetsAtBoundary(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	const duration = int64(10)
	key := redisIPRateLimitKey("BOUNDARY", "192.0.2.50")

	for range 2 {
		allowed, _, _, err := redisFixedWindowTake(context.Background(), key, 2, duration)
		require.NoError(t, err)
		assert.True(t, allowed)
	}
	allowed, _, _, err := redisFixedWindowTake(context.Background(), key, 2, duration)
	require.NoError(t, err)
	assert.False(t, allowed)

	// This reset is intentional fixed-window behavior. A client can consume one
	// full allowance immediately before and another immediately after a boundary.
	redisServer.FastForward(time.Duration(duration) * time.Second)
	for range 2 {
		allowed, _, _, err = redisFixedWindowTake(context.Background(), key, 2, duration)
		require.NoError(t, err)
		assert.True(t, allowed)
	}
}

func TestRedisFixedWindowRepairsCounterWithoutTTL(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	const duration = int64(29)
	key := redisIPRateLimitKey("MISSING-TTL", "192.0.2.51")
	redisServer.Set(key, "5")

	allowed, count, ttl, err := redisFixedWindowTake(context.Background(), key, 3, duration)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, int64(6), count)
	assert.Equal(t, duration, ttl)
	assert.Equal(t, time.Duration(duration)*time.Second, redisServer.TTL(key))

	redisServer.FastForward(time.Duration(duration) * time.Second)
	assert.False(t, redisServer.Exists(key), "a recovered counter must not remain permanently rate-limited")
}

// useRateLimitRedisFailure sets RATE_LIMIT_REDIS_FAILURE for one test.
func useRateLimitRedisFailure(t *testing.T, policy string) {
	t.Helper()
	previous := common.RateLimitRedisFailure
	common.RateLimitRedisFailure = policy
	t.Cleanup(func() { common.RateLimitRedisFailure = previous })
}

// uniqueRateLimitMark gives a limiter its own in-memory counters in every
// run, so repeated runs in one process do not share them.
func uniqueRateLimitMark(base string) string {
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// newRedisFailureRouter wires an IP limiter, a per-user limiter (user 7) and
// the email code limiter, each allowing one request per 30 seconds.
func newRedisFailureRouter(t *testing.T, ipMark, userMark string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/ip", rateLimitFactory(1, 30, ipMark), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET(
		"/user",
		func(c *gin.Context) { c.Set("id", 7) },
		userRateLimitFactory(1, 30, userMark),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	router.GET("/email", EmailVerificationRateLimit(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return router
}

// While Redis fails, the API and per-user limiters count in this node's
// memory instead (RATE_LIMIT_REDIS_FAILURE=memory, the default): requests pass
// up to the limit and then get the usual 429 with Retry-After, keyed like the
// memory backend (the IP, or the user wherever the request comes from).
func TestRedisFailurePolicies(t *testing.T) {
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())
	useRateLimitRedisFailure(t, common.RateLimitRedisFailureMemory)
	ipMark, userMark := uniqueRateLimitMark("FAIL-IP"), uniqueRateLimitMark("FAIL-USER")
	router := newRedisFailureRouter(t, ipMark, userMark)
	clientIP := uniqueTestClientIP()

	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/ip", clientIP+":12345").Code)
	ipLimited := performRateLimitRequest(router, "/ip", clientIP+":12345")
	assert.Equal(t, http.StatusTooManyRequests, ipLimited.Code)
	assert.Equal(t, "no-store", ipLimited.Header().Get("Cache-Control"))
	retryAfter, err := strconv.Atoi(ipLimited.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.Positive(t, retryAfter)
	assert.LessOrEqual(t, retryAfter, 30)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/ip", uniqueTestClientIP()+":12345").Code, "another IP has its own count")

	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/user", uniqueTestClientIP()+":12345").Code)
	userLimited := performRateLimitRequest(router, "/user", uniqueTestClientIP()+":12345")
	assert.Equal(t, http.StatusTooManyRequests, userLimited.Code, "the user is counted wherever the request comes from")
	assert.NotEmpty(t, userLimited.Header().Get("Retry-After"))

	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/email", uniqueTestClientIP()+":12345").Code)
}

// RATE_LIMIT_REDIS_FAILURE=reject restores the behaviour before the memory
// fallback: every request the API and per-user limiters cannot count in
// Redis gets an empty 500, and nothing is counted in memory.
func TestRedisFailureRejectModeKeepsOldBehaviour(t *testing.T) {
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())
	useRateLimitRedisFailure(t, common.RateLimitRedisFailureReject)
	ipMark, userMark := uniqueRateLimitMark("REJECT-IP"), uniqueRateLimitMark("REJECT-USER")
	router := newRedisFailureRouter(t, ipMark, userMark)
	clientIP := uniqueTestClientIP()

	for range 2 {
		ipResponse := performRateLimitRequest(router, "/ip", clientIP+":12345")
		assert.Equal(t, http.StatusInternalServerError, ipResponse.Code)
		assert.Empty(t, ipResponse.Body.String())
		userResponse := performRateLimitRequest(router, "/user", clientIP+":12345")
		assert.Equal(t, http.StatusInternalServerError, userResponse.Code)
		assert.Empty(t, userResponse.Body.String())
	}
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/email", clientIP+":12345").Code)

	// Back on the default, the first requests are counted in memory only now.
	common.RateLimitRedisFailure = common.RateLimitRedisFailureMemory
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/ip", clientIP+":12345").Code)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/user", clientIP+":12345").Code)
}

// The fallback logs the first Redis failure of a limiter, then at most one
// line per 10 seconds, saying how many failures it left out; each limiter
// has its own interval.
func TestRedisFailureFallbackLogIsThrottled(t *testing.T) {
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())
	useRateLimitRedisFailure(t, common.RateLimitRedisFailureMemory)
	now := time.Unix(1_800_000_000, 0)
	previousNow := redisFallbackLogNow
	redisFallbackLogNow = func() time.Time { return now }
	var output strings.Builder
	previousErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &output
	t.Cleanup(func() {
		redisFallbackLogNow = previousNow
		gin.DefaultErrorWriter = previousErrorWriter
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	firstMark, secondMark := uniqueRateLimitMark("LOG-A"), uniqueRateLimitMark("LOG-B")
	clientIP := uniqueTestClientIP()
	router.GET("/a", rateLimitFactory(100, 60, firstMark), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/b", rateLimitFactory(100, 60, secondMark), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	linesOf := func(mark string) []string {
		var lines []string
		for _, line := range strings.Split(output.String(), "\n") {
			if strings.Contains(line, "mark="+mark+",") {
				lines = append(lines, line)
			}
		}
		return lines
	}

	for range 5 {
		require.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/a", clientIP+":12345").Code)
	}
	require.Len(t, linesOf(firstMark), 1)
	assert.Contains(t, linesOf(firstMark)[0], "[WARN]")
	assert.Contains(t, linesOf(firstMark)[0], "using the in-memory limiter (mark="+firstMark+", 0 more failures since the last line)")

	now = now.Add(9 * time.Second)
	performRateLimitRequest(router, "/a", clientIP+":12345")
	assert.Len(t, linesOf(firstMark), 1, "no second line within 10 seconds")
	performRateLimitRequest(router, "/b", clientIP+":12345")
	assert.Len(t, linesOf(secondMark), 1, "another limiter logs on its own")

	now = now.Add(time.Second)
	performRateLimitRequest(router, "/a", clientIP+":12345")
	require.Len(t, linesOf(firstMark), 2)
	assert.Contains(t, linesOf(firstMark)[1], "(mark="+firstMark+", 5 more failures since the last line)")
}

func useGlobalWebRateLimit(t *testing.T, maxRequestNum int, duration int64) {
	t.Helper()

	previousEnable := common.GlobalWebRateLimitEnable
	previousNum := common.GlobalWebRateLimitNum
	previousDuration := common.GlobalWebRateLimitDuration
	common.GlobalWebRateLimitEnable = true
	common.GlobalWebRateLimitNum = maxRequestNum
	common.GlobalWebRateLimitDuration = duration
	t.Cleanup(func() {
		common.GlobalWebRateLimitEnable = previousEnable
		common.GlobalWebRateLimitNum = previousNum
		common.GlobalWebRateLimitDuration = previousDuration
	})
}

func TestGlobalWebRateLimitUsesRedisWindowWithGWMark(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)
	useGlobalWebRateLimit(t, 1, 45)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/page", GlobalWebRateLimit(emptyFrontendFS(t)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	remoteAddr := "192.0.2.70:12345"
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/page", remoteAddr).Code)
	limitedResponse := performRateLimitRequest(router, "/page", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, limitedResponse.Code)
	assert.Equal(t, "45", limitedResponse.Header().Get("Retry-After"))
	assert.Equal(t, "no-store", limitedResponse.Header().Get("Cache-Control"))

	count, err := redisServer.Get(redisIPRateLimitKey("GW", "192.0.2.70"))
	require.NoError(t, err)
	assert.Equal(t, "2", count)
}

// The web tier fails open when Redis is unavailable: a Redis restart must not
// turn every dashboard page load into an error page. API limiters count in
// memory instead, as TestRedisFailurePolicies asserts.
func TestGlobalWebRateLimitFailsOpenOnRedisError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())
	useGlobalWebRateLimit(t, 1, 30)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/page", GlobalWebRateLimit(emptyFrontendFS(t)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	remoteAddr := "192.0.2.71:12345"
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/page", remoteAddr).Code)
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/page", remoteAddr).Code)
}

func TestGlobalWebRateLimitCountsFrontendFilesInStaticBucket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousWebEnable, previousWebNum, previousWebDuration := common.GlobalWebRateLimitEnable, common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration
	previousStaticEnable, previousStaticNum, previousStaticDuration := common.GlobalStaticRateLimitEnable, common.GlobalStaticRateLimitNum, common.GlobalStaticRateLimitDuration
	t.Cleanup(func() {
		common.GlobalWebRateLimitEnable, common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration = previousWebEnable, previousWebNum, previousWebDuration
		common.GlobalStaticRateLimitEnable, common.GlobalStaticRateLimitNum, common.GlobalStaticRateLimitDuration = previousStaticEnable, previousStaticNum, previousStaticDuration
	})
	common.GlobalWebRateLimitEnable, common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration = true, 2, 41
	common.GlobalStaticRateLimitNum, common.GlobalStaticRateLimitDuration = 2, 43

	frontendDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(frontendDir, "static", "js"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "static", "js", "app.js"), []byte("app"), 0o644))
	frontendFS := static.LocalFile(frontendDir, false)

	const clientIP = "192.0.2.70"
	const remoteAddr = clientIP + ":12345"

	t.Run("static limiter disabled by default", func(t *testing.T) {
		redisServer, _ := useRateLimitMiniRedis(t)
		common.GlobalStaticRateLimitEnable = false
		router := gin.New()
		require.NoError(t, router.SetTrustedProxies(nil))
		router.NoRoute(GlobalWebRateLimit(frontendFS), func(c *gin.Context) { c.Status(http.StatusNoContent) })

		for range 5 {
			assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/static/js/app.js", remoteAddr).Code)
		}
		assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/channels", remoteAddr).Code)
		assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/static/js/missing.js", remoteAddr).Code)
		pageResponse := performRateLimitRequest(router, "/channels", remoteAddr)
		assert.Equal(t, http.StatusTooManyRequests, pageResponse.Code)
		assert.Equal(t, "41", pageResponse.Header().Get("Retry-After"))

		webCount, err := redisServer.Get(redisIPRateLimitKey("GW", clientIP))
		require.NoError(t, err)
		assert.Equal(t, "3", webCount)
		assert.False(t, redisServer.Exists(redisIPRateLimitKey("GS", clientIP)))
	})

	t.Run("static limiter enabled uses its own bucket", func(t *testing.T) {
		redisServer, _ := useRateLimitMiniRedis(t)
		common.GlobalStaticRateLimitEnable = true
		router := gin.New()
		require.NoError(t, router.SetTrustedProxies(nil))
		router.NoRoute(GlobalWebRateLimit(frontendFS), func(c *gin.Context) { c.Status(http.StatusNoContent) })

		assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/static/js/app.js", remoteAddr).Code)
		assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/static/js/app.js", remoteAddr).Code)
		staticResponse := performRateLimitRequest(router, "/static/js/app.js", remoteAddr)
		assert.Equal(t, http.StatusTooManyRequests, staticResponse.Code)
		assert.Equal(t, "43", staticResponse.Header().Get("Retry-After"))
		assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/channels", remoteAddr).Code)

		staticCount, err := redisServer.Get(redisIPRateLimitKey("GS", clientIP))
		require.NoError(t, err)
		assert.Equal(t, "3", staticCount)
		webCount, err := redisServer.Get(redisIPRateLimitKey("GW", clientIP))
		require.NoError(t, err)
		assert.Equal(t, "1", webCount)
	})
}

func emptyFrontendFS(t *testing.T) static.ServeFileSystem {
	t.Helper()
	return static.LocalFile(t.TempDir(), false)
}

// useRegisterRateLimit sets the per-network registration limit for one test.
func useRegisterRateLimit(t *testing.T, enabled bool, maxRequests int, minutes int) {
	t.Helper()
	previousEnabled, previousNum, previousMinutes := common.RegisterIPRateLimitEnabled, common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes
	common.RegisterIPRateLimitEnabled, common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes = enabled, maxRequests, minutes
	t.Cleanup(func() {
		common.RegisterIPRateLimitEnabled, common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes = previousEnabled, previousNum, previousMinutes
	})
}

// useFreshRegisterRateLimiter gives the test an empty in-memory store for the
// registration limits, so repeated runs in one process do not share counters.
func useFreshRegisterRateLimiter(t *testing.T) {
	t.Helper()
	previousLimiter := registerRateLimiter
	registerRateLimiter = new(common.InMemoryRateLimiter)
	t.Cleanup(func() { registerRateLimiter = previousLimiter })
}

func useMemoryRateLimitBackend(t *testing.T) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	useFreshRegisterRateLimiter(t)
}

func newRegisterLimitRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	accepted := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	router.POST("/api/user/register", RegisterAttemptRateLimit(), accepted)
	router.GET("/api/verification", RegisterCodeRateLimit(), accepted)
	router.GET("/api/captcha", CaptchaRateLimit(), accepted)
	return router
}

func performRegisterLimitRequest(router http.Handler, method string, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestRegisterRateLimitsCountSubmissionsAndCodesSeparately(t *testing.T) {
	for _, backend := range []struct{ name, clientIP string }{{"redis", "192.0.2.90"}, {"memory", "192.0.2.91"}} {
		t.Run(backend.name, func(t *testing.T) {
			var redisServer *miniredis.Miniredis
			if backend.name == "redis" {
				redisServer, _ = useRateLimitMiniRedis(t)
			} else {
				useMemoryRateLimitBackend(t)
			}
			useRegisterRateLimit(t, true, 2, 3)
			router := newRegisterLimitRouter(t)
			remoteAddr := backend.clientIP + ":4321"

			for range 2 {
				assert.Equal(t, http.StatusNoContent, performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", remoteAddr).Code)
			}
			limited := performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", remoteAddr)
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.JSONEq(t, `{"success":false,"code":"REGISTER_RATE_LIMITED","message":"Too many registration attempts from your network. Please try again in 3 minutes.","message_key":"Too many registration attempts from your network. Please try again in {{minutes}} minutes.","message_params":{"minutes":3}}`, limited.Body.String())
			retryAfter, err := strconv.Atoi(limited.Header().Get("Retry-After"))
			require.NoError(t, err)
			assert.InDelta(t, 180, retryAfter, 1)
			assert.Equal(t, "no-store", limited.Header().Get("Cache-Control"))

			// Email code requests have their own counter, and other networks are not affected.
			for range 2 {
				assert.Equal(t, http.StatusNoContent, performRegisterLimitRequest(router, http.MethodGet, "/api/verification", remoteAddr).Code)
			}
			limited = performRegisterLimitRequest(router, http.MethodGet, "/api/verification", remoteAddr)
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.JSONEq(t, `{"success":false,"code":"REGISTER_CODE_RATE_LIMITED","message":"Too many verification code requests from your network. Please try again in 3 minutes.","message_key":"Too many verification code requests from your network. Please try again in {{minutes}} minutes.","message_params":{"minutes":3}}`, limited.Body.String())
			assert.Equal(t, http.StatusNoContent, performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", "198.51.100.90:4321").Code)

			if redisServer != nil {
				key := redisIPRateLimitKey(RegisterAttemptRateLimitMark, backend.clientIP)
				count, err := redisServer.Get(key)
				require.NoError(t, err)
				assert.Equal(t, "3", count, "refused attempts count too")
				assert.Equal(t, 3*time.Minute, redisServer.TTL(key))
				assert.True(t, redisServer.Exists(redisIPRateLimitKey(RegisterCodeRateLimitMark, backend.clientIP)))
			}
		})
	}
}

func TestRegisterRateLimitGroupsIPv6ByPrefixAndFollowsSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useMemoryRateLimitBackend(t)
	useRegisterRateLimit(t, true, 1, 15)
	router := newRegisterLimitRouter(t)
	register := func(remoteAddr string) int {
		return performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", remoteAddr).Code
	}

	assert.Equal(t, http.StatusNoContent, register("[2001:db8:7:1::10]:4321"))
	assert.Equal(t, http.StatusTooManyRequests, register("[2001:db8:7:1:ffff::2]:4321"), "one /64 is one network")
	assert.Equal(t, http.StatusNoContent, register("[2001:db8:7:2::10]:4321"))
	assert.Equal(t, http.StatusNoContent, register("[::ffff:192.0.2.92]:4321"))
	assert.Equal(t, http.StatusTooManyRequests, register("192.0.2.92:4321"), "an IPv4-mapped address is its IPv4 address")

	// The settings are read on every request.
	common.RegisterIPRateLimitNum = 2
	assert.Equal(t, http.StatusNoContent, register("192.0.2.92:4321"))
	assert.Equal(t, http.StatusTooManyRequests, register("192.0.2.92:4321"))
	common.RegisterIPRateLimitEnabled = false
	assert.Equal(t, http.StatusNoContent, register("192.0.2.92:4321"))
}

func TestRegisterRateLimitFallsBackToMemoryWhenRedisFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())
	useFreshRegisterRateLimiter(t)
	useRegisterRateLimit(t, true, 1, 15)
	router := newRegisterLimitRouter(t)

	assert.Equal(t, http.StatusNoContent, performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", "192.0.2.93:4321").Code)
	limited := performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", "192.0.2.93:4321")
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Contains(t, limited.Body.String(), `"code":"REGISTER_RATE_LIMITED"`)
	assert.Contains(t, limited.Body.String(), `"message_params":{"minutes":15}`)
}

// Shortening the registration window applies at once on both backends: a
// network that reached the limit under a long window waits at most the new
// window, and Retry-After and the message say so. On Redis the counter
// started under the long window then expires with the new one; later
// requests do not restart it.
func TestRegisterRateLimitShortenedWindowAppliesAtOnce(t *testing.T) {
	for _, backend := range []struct{ name, clientIP string }{{"redis", "192.0.2.96"}, {"memory", "192.0.2.97"}} {
		t.Run(backend.name, func(t *testing.T) {
			var redisServer *miniredis.Miniredis
			if backend.name == "redis" {
				redisServer, _ = useRateLimitMiniRedis(t)
			} else {
				useMemoryRateLimitBackend(t)
			}
			useRegisterRateLimit(t, true, 1, 1440)
			router := newRegisterLimitRouter(t)
			register := func() *httptest.ResponseRecorder {
				return performRegisterLimitRequest(router, http.MethodPost, "/api/user/register", backend.clientIP+":4321")
			}
			retryAfter := func(response *httptest.ResponseRecorder) int {
				seconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
				require.NoError(t, err)
				return seconds
			}

			require.Equal(t, http.StatusNoContent, register().Code)
			limited := register()
			require.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.InDelta(t, 24*60*60, retryAfter(limited), 1)

			common.RegisterIPRateLimitMinutes = 15
			limited = register()
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.InDelta(t, 15*60, retryAfter(limited), 1)
			assert.Contains(t, limited.Body.String(), `"message":"Too many registration attempts from your network. Please try again in 15 minutes."`)
			if redisServer == nil {
				return
			}
			key := redisIPRateLimitKey(RegisterAttemptRateLimitMark, backend.clientIP)
			assert.Equal(t, 15*time.Minute, redisServer.TTL(key))
			redisServer.FastForward(5 * time.Minute)
			limited = register()
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.Equal(t, 10*60, retryAfter(limited), "a later request does not restart the window")
			redisServer.FastForward(10 * time.Minute)
			assert.Equal(t, http.StatusNoContent, register().Code, "the network may register again after the new window")
		})
	}
}

// useCaptchaBudgetSettings sets the switches the captcha image limit reads:
// the three captchas and the critical rate limit (per window in seconds).
func useCaptchaBudgetSettings(t *testing.T, registerCaptcha, loginCaptcha, resetCaptcha, critical bool, criticalNum int, criticalWindow int64) {
	t.Helper()
	previous := []any{common.RegisterCaptchaEnabled, common.LoginCaptchaEnabled, common.PasswordResetCaptchaEnabled,
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration}
	common.RegisterCaptchaEnabled, common.LoginCaptchaEnabled, common.PasswordResetCaptchaEnabled = registerCaptcha, loginCaptcha, resetCaptcha
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = critical, criticalNum, criticalWindow
	t.Cleanup(func() {
		common.RegisterCaptchaEnabled, common.LoginCaptchaEnabled, common.PasswordResetCaptchaEnabled = previous[0].(bool), previous[1].(bool), previous[2].(bool)
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = previous[3].(bool), previous[4].(int), previous[5].(int64)
	})
}

// The captcha image limit (CP) follows the per-network registration limit
// while only the registration captcha is on and the critical rate limit is
// off: nothing is limited or counted while that is off, otherwise a network
// gets max(30, 6 × RegisterIPRateLimitNum) images per
// RegisterIPRateLimitMinutes. The settings are read on every request.
func TestCaptchaRateLimitFollowsTheRegistrationLimit(t *testing.T) {
	for _, backend := range []struct{ name, network, other string }{
		{"redis", "192.0.2.94", "198.51.100.94"}, {"memory", "192.0.2.95", "198.51.100.95"},
	} {
		t.Run(backend.name, func(t *testing.T) {
			var redisServer *miniredis.Miniredis
			if backend.name == "redis" {
				redisServer, _ = useRateLimitMiniRedis(t)
			} else {
				useMemoryRateLimitBackend(t)
			}
			useRegisterRateLimit(t, false, 1, 1)
			useCaptchaBudgetSettings(t, true, false, false, false, 20, 1200)
			router := newRegisterLimitRouter(t)
			captcha := func(network string) *httptest.ResponseRecorder {
				return performRegisterLimitRequest(router, http.MethodGet, "/api/captcha", network+":4321")
			}
			const limitedJSON = `{"success":false,"message":"Too many captcha requests. Please try again later.","message_key":"Too many captcha requests. Please try again later."}`

			for range 2 * CaptchaRateLimitMinNum {
				require.Equal(t, http.StatusNoContent, captcha(backend.network).Code, "no limit while the registration limit is off")
			}
			if redisServer != nil {
				assert.False(t, redisServer.Exists(redisIPRateLimitKey(CaptchaRateLimitMark, backend.network)))
			}

			// 5 registrations per 15 minutes: the floor of 30 images applies,
			// and the images loaded while the limit was off do not count.
			common.RegisterIPRateLimitEnabled, common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes = true, 5, 15
			for range CaptchaRateLimitMinNum {
				require.Equal(t, http.StatusNoContent, captcha(backend.network).Code)
			}
			limited := captcha(backend.network)
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.JSONEq(t, limitedJSON, limited.Body.String())
			retryAfter, err := strconv.Atoi(limited.Header().Get("Retry-After"))
			require.NoError(t, err)
			assert.InDelta(t, 900, retryAfter, 1)
			assert.Equal(t, "no-store", limited.Header().Get("Cache-Control"))
			if redisServer != nil {
				assert.Equal(t, 15*time.Minute, redisServer.TTL(redisIPRateLimitKey(CaptchaRateLimitMark, backend.network)))
			}

			// 10 registrations per hour: 6 images per registration.
			common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes = 10, 60
			for range 60 {
				require.Equal(t, http.StatusNoContent, captcha(backend.other).Code)
			}
			limited = captcha(backend.other)
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.JSONEq(t, limitedJSON, limited.Body.String())
			retryAfter, err = strconv.Atoi(limited.Header().Get("Retry-After"))
			require.NoError(t, err)
			assert.InDelta(t, 3600, retryAfter, 1)
			if redisServer != nil {
				assert.Equal(t, time.Hour, redisServer.TTL(redisIPRateLimitKey(CaptchaRateLimitMark, backend.other)))
			}

			common.RegisterIPRateLimitEnabled = false
			assert.Equal(t, http.StatusNoContent, captcha(backend.network).Code, "turning the registration limit off lifts the captcha limit at once")
		})
	}
}

// With the default settings and any captcha on, a network may load three
// captcha images for each request the critical rate limit allows: 60 per 20
// minutes while the registration limit is off, 30 + 3 × 20 per registration
// window while it is on. Without the critical rate limit, and with every
// captcha off, the registration-off case stays unlimited.
func TestCaptchaRateLimitBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useMemoryRateLimitBackend(t)
	useRegisterRateLimit(t, false, 5, 15)
	useCaptchaBudgetSettings(t, false, true, false, true, 20, 1200)
	router := newRegisterLimitRouter(t)
	captcha := func(remoteAddr string) *httptest.ResponseRecorder {
		return performRegisterLimitRequest(router, http.MethodGet, "/api/captcha", remoteAddr)
	}
	drain := func(remoteAddr string, allowed int) {
		t.Helper()
		for i := range allowed {
			require.Equal(t, http.StatusNoContent, captcha(remoteAddr).Code, "image %d", i+1)
		}
		limited := captcha(remoteAddr)
		require.Equal(t, http.StatusTooManyRequests, limited.Code)
		assert.Equal(t, "Too many captcha requests. Please try again later.", gjsonMessage(t, limited))
	}

	// Registration limit off, critical limit on, login captcha on: 60 per 20 minutes.
	drain("[2001:db8:60::1]:4321", 3*20)
	assert.Equal(t, http.StatusTooManyRequests, captcha("[2001:db8:60::ffff]:4321").Code, "one /64 shares one budget")
	retryAfter, err := strconv.Atoi(captcha("[2001:db8:60::2]:4321").Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.InDelta(t, 1200, retryAfter, 1)

	// The critical rate limit off: unlimited, as before.
	common.CriticalRateLimitEnable = false
	for range 100 {
		require.Equal(t, http.StatusNoContent, captcha("192.0.2.160:4321").Code)
	}
	// Every captcha off: unlimited.
	common.CriticalRateLimitEnable, common.LoginCaptchaEnabled = true, false
	for range 100 {
		require.Equal(t, http.StatusNoContent, captcha("192.0.2.161:4321").Code)
	}
	// The registration captcha alone counts too.
	common.RegisterCaptchaEnabled = true
	drain("192.0.2.162:4321", 60)

	// Registration limit on (5 per 15 minutes) with a sign-in captcha on:
	// 30 + 3 × 20 × 1 images per 15 minutes.
	common.RegisterIPRateLimitEnabled, common.PasswordResetCaptchaEnabled = true, true
	drain("192.0.2.163:4321", 90)
	// Both sign-in captchas off: the unchanged registration budget.
	common.LoginCaptchaEnabled, common.PasswordResetCaptchaEnabled = false, false
	drain("192.0.2.164:4321", CaptchaRateLimitMinNum)
}

func gjsonMessage(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
	return body.Message
}

// useAuthGuardClock replaces the clock of the password reset limit and the
// login lockout; the test moves it by assigning *now.
func useAuthGuardClock(t *testing.T) *time.Time {
	t.Helper()
	previous := authGuardNow
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	authGuardNow = func() time.Time { return now }
	t.Cleanup(func() { authGuardNow = previous })
	return &now
}

// useFreshAuthGuardMemory gives the test empty in-memory stores.
func useFreshAuthGuardMemory(t *testing.T) {
	t.Helper()
	previousReset, previousLogin := passwordResetMemory, loginLockMemory
	passwordResetMemory, loginLockMemory = newAuthGuardMemory(authGuardMemoryMaxEntries), newAuthGuardMemory(authGuardMemoryMaxEntries)
	t.Cleanup(func() { passwordResetMemory, loginLockMemory = previousReset, previousLogin })
}

func useAuthGuardSettings(t *testing.T, resetLimit bool, cooldownSeconds, dailyMax int, lockout bool, maxFailures, minutes int) {
	t.Helper()
	previousReset, previousCooldown, previousDaily := common.PasswordResetEmailLimitEnabled, common.PasswordResetEmailCooldownSeconds, common.PasswordResetEmailDailyMax
	previousLockout, previousMax, previousMinutes := common.LoginLockoutEnabled, common.LoginLockoutMaxFailures, common.LoginLockoutMinutes
	common.PasswordResetEmailLimitEnabled, common.PasswordResetEmailCooldownSeconds, common.PasswordResetEmailDailyMax = resetLimit, cooldownSeconds, dailyMax
	common.LoginLockoutEnabled, common.LoginLockoutMaxFailures, common.LoginLockoutMinutes = lockout, maxFailures, minutes
	t.Cleanup(func() {
		common.PasswordResetEmailLimitEnabled, common.PasswordResetEmailCooldownSeconds, common.PasswordResetEmailDailyMax = previousReset, previousCooldown, previousDaily
		common.LoginLockoutEnabled, common.LoginLockoutMaxFailures, common.LoginLockoutMinutes = previousLockout, previousMax, previousMinutes
	})
}

// useAuthGuardBackend runs the test on miniredis ("redis") or on the
// in-memory store ("memory"); it returns the miniredis server or nil.
func useAuthGuardBackend(t *testing.T, backend string) *miniredis.Miniredis {
	t.Helper()
	useFreshAuthGuardMemory(t)
	if backend == "redis" {
		redisServer, _ := useRateLimitMiniRedis(t)
		return redisServer
	}
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	return nil
}

// assertNoRawValueInKeys checks that no Redis key name carries one of the
// given values in clear text.
func assertNoRawValueInKeys(t *testing.T, redisServer *miniredis.Miniredis, values ...string) {
	t.Helper()
	for _, key := range redisServer.Keys() {
		for _, value := range values {
			assert.NotContains(t, strings.ToLower(key), strings.ToLower(value), "key %s", key)
		}
	}
}

// One rolling 24-hour log per address: a cooldown between two admitted
// requests, at most the daily maximum in any 24 hours, the daily maximum
// reported first, and refusals never recorded.
func TestPasswordResetEmailLimitCooldownAndRollingDay(t *testing.T) {
	for _, backend := range []string{"redis", "memory"} {
		t.Run(backend, func(t *testing.T) {
			redisServer := useAuthGuardBackend(t, backend)
			now := useAuthGuardClock(t)
			useAuthGuardSettings(t, true, 120, 5, true, 10, 15)
			ctx := context.Background()
			const email = "owner@example.com"
			start := *now
			at := func(offset time.Duration) { *now = start.Add(offset) }
			subject := passwordResetEmailSubject(email)
			key := authGuardRedisPrefix + "reset:" + subject
			admitted := func() int {
				if redisServer != nil {
					members, err := redisServer.ZMembers(key)
					require.NoError(t, err)
					return len(members)
				}
				entry := passwordResetMemory.lookup(subject)
				if entry == nil {
					return 0
				}
				return len(entry.marks)
			}

			assert.Nil(t, TakePasswordResetEmail(ctx, email))
			at(10 * time.Second)
			assert.Equal(t, &PasswordResetEmailRefusal{Code: PasswordResetEmailCooldownCode, WaitSeconds: 110}, TakePasswordResetEmail(ctx, email))
			at(119*time.Second + 500*time.Millisecond)
			assert.Equal(t, &PasswordResetEmailRefusal{Code: PasswordResetEmailCooldownCode, WaitSeconds: 1}, TakePasswordResetEmail(ctx, email), "waits round up")
			assert.Equal(t, 1, admitted(), "refused requests are not recorded")
			for i := 1; i < 5; i++ {
				at(time.Duration(i) * 120 * time.Second)
				require.Nil(t, TakePasswordResetEmail(ctx, email), "request %d", i+1)
			}
			at(490 * time.Second)
			assert.Equal(t, &PasswordResetEmailRefusal{Code: PasswordResetEmailDailyLimitCode, WaitSeconds: 86400 - 490}, TakePasswordResetEmail(ctx, email), "the daily maximum is reported before the cooldown")
			at(600 * time.Second)
			assert.Equal(t, &PasswordResetEmailRefusal{Code: PasswordResetEmailDailyLimitCode, WaitSeconds: 86400 - 600}, TakePasswordResetEmail(ctx, email))
			assert.Equal(t, 5, admitted())

			// Rolling, not a calendar day: the first request leaves the window after 24 hours.
			at(24*time.Hour - time.Millisecond)
			assert.Equal(t, PasswordResetEmailDailyLimitCode, TakePasswordResetEmail(ctx, email).Code)
			at(24 * time.Hour)
			assert.Nil(t, TakePasswordResetEmail(ctx, email))
			assert.Equal(t, 5, admitted())

			// A lowered maximum waits for the entry at index count-max.
			common.PasswordResetEmailDailyMax = 2
			at(24*time.Hour + 200*time.Second)
			assert.Equal(t, &PasswordResetEmailRefusal{Code: PasswordResetEmailDailyLimitCode, WaitSeconds: 480 + 86400 - 86600}, TakePasswordResetEmail(ctx, email))

			// Other addresses have their own log; the limit off admits and records nothing.
			assert.Nil(t, TakePasswordResetEmail(ctx, "other@example.com"))
			common.PasswordResetEmailLimitEnabled = false
			for range 3 {
				assert.Nil(t, TakePasswordResetEmail(ctx, "free@example.com"))
			}
			assert.Equal(t, int64(0), PasswordResetEmailCooldownSeconds())
			common.PasswordResetEmailLimitEnabled = true
			assert.Equal(t, int64(120), PasswordResetEmailCooldownSeconds())
			if redisServer != nil {
				assert.True(t, redisServer.Exists(key))
				assert.Equal(t, 24*time.Hour, redisServer.TTL(key))
				assert.False(t, redisServer.Exists(authGuardRedisPrefix+"reset:"+passwordResetEmailSubject("free@example.com")))
				assert.Len(t, redisServer.Keys(), 2)
				assertNoRawValueInKeys(t, redisServer, "owner", "example.com", "other")
			} else {
				assert.Nil(t, passwordResetMemory.lookup(passwordResetEmailSubject("free@example.com")))
			}

			// Clearing forgets the address.
			ClearPasswordResetEmailLimit(ctx, email)
			assert.Nil(t, TakePasswordResetEmail(ctx, email))
		})
	}
}

func TestPasswordResetEmailLimitIsAtomic(t *testing.T) {
	useAuthGuardBackend(t, "redis")
	useAuthGuardClock(t)
	useAuthGuardSettings(t, true, 120, 5, true, 10, 15)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if TakePasswordResetEmail(context.Background(), "race@example.com") == nil {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), admitted.Load())
}

// The lockout counts failed checks per subject in a rolling window and
// locks every subject that reaches the maximum, for as long as the window.
func TestLoginLockoutCountsLocksAndClears(t *testing.T) {
	for _, backend := range []string{"redis", "memory"} {
		t.Run(backend, func(t *testing.T) {
			redisServer := useAuthGuardBackend(t, backend)
			now := useAuthGuardClock(t)
			useAuthGuardSettings(t, true, 120, 5, true, 3, 1)
			ctx := context.Background()
			fail := func(subjects []string) (int64, bool) {
				t.Helper()
				attempt, wait := BeginLoginAttempt(ctx, subjects)
				require.NotNil(t, attempt)
				require.Zero(t, wait)
				return attempt.Fail(ctx)
			}
			x := LoginLockSubjects(0, "x")
			for range 2 {
				lockSeconds, started := fail(x)
				assert.Zero(t, lockSeconds)
				assert.False(t, started)
			}
			lockSeconds, started := fail(x)
			assert.Equal(t, int64(60), lockSeconds, "the failure that reaches the maximum locks at once")
			assert.True(t, started)
			attempt, wait := BeginLoginAttempt(ctx, x)
			assert.Nil(t, attempt)
			assert.Equal(t, int64(60), wait)
			*now = now.Add(10 * time.Second)
			_, wait = BeginLoginAttempt(ctx, x)
			assert.Equal(t, int64(50), wait, "a refused attempt is not counted and does not extend the lock")

			// An account locked through one name is locked for every name.
			for range 3 {
				fail(LoginLockSubjects(7, "y"))
			}
			attempt, wait = BeginLoginAttempt(ctx, LoginLockSubjects(7, "z"))
			assert.Nil(t, attempt)
			assert.Equal(t, int64(60), wait)

			// After the lock the count starts again.
			*now = now.Add(51 * time.Second)
			for range 2 {
				lockSeconds, _ = fail(x)
				assert.Zero(t, lockSeconds)
			}
			// Failures older than the window drop out.
			*now = now.Add(61 * time.Second)
			lockSeconds, started = fail(x)
			assert.Zero(t, lockSeconds)
			assert.False(t, started)

			// Released checks are not failures; Release after Fail does nothing.
			for range 5 {
				attempt, wait = BeginLoginAttempt(ctx, LoginLockSubjects(0, "released"))
				require.NotNil(t, attempt)
				require.Zero(t, wait)
				attempt.Release(ctx)
				attempt.Release(ctx)
			}
			attempt, _ = BeginLoginAttempt(ctx, LoginLockSubjects(0, "released"))
			lockSeconds, _ = attempt.Fail(ctx)
			attempt.Release(ctx)
			assert.Zero(t, lockSeconds)
			var nilAttempt *LoginAttempt
			nilAttempt.Release(ctx)
			lockSeconds, started = nilAttempt.Fail(ctx)
			assert.Zero(t, lockSeconds)
			assert.False(t, started)

			// Clearing an account clears its names too.
			for range 3 {
				fail(LoginLockSubjects(8, "Alice"))
			}
			for range 2 {
				fail(LoginLockSubjects(0, "a@x.com"))
			}
			assert.True(t, ClearLoginLockout(ctx, 8, "Alice", "a@x.com"))
			for _, subjects := range [][]string{LoginLockSubjects(8, ""), LoginLockSubjects(0, "alice")} {
				attempt, wait = BeginLoginAttempt(ctx, subjects)
				assert.NotNil(t, attempt)
				assert.Zero(t, wait)
				attempt.Release(ctx)
			}
			lockSeconds, _ = fail(LoginLockSubjects(0, "a@x.com"))
			assert.Zero(t, lockSeconds, "the failures of the email name were cleared")
			assert.False(t, ClearLoginLockout(ctx, 8, "Alice"))

			// The lockout off counts nothing; clearing still works.
			for range 3 {
				fail(LoginLockSubjects(7, "y"))
			}
			common.LoginLockoutEnabled = false
			var keysBefore int
			if redisServer != nil {
				keysBefore = len(redisServer.Keys())
			}
			for range 5 {
				attempt, wait = BeginLoginAttempt(ctx, LoginLockSubjects(9, "off"))
				assert.Nil(t, attempt)
				assert.Zero(t, wait)
			}
			if redisServer != nil {
				assert.Len(t, redisServer.Keys(), keysBefore)
			}
			assert.True(t, ClearLoginLockout(ctx, 7, "y"))
			common.LoginLockoutEnabled = true
			attempt, wait = BeginLoginAttempt(ctx, LoginLockSubjects(7, "y"))
			assert.NotNil(t, attempt)
			assert.Zero(t, wait)
			if redisServer != nil {
				assertNoRawValueInKeys(t, redisServer, "alice", "a@x.com", "released")
			}
		})
	}
}

// Every check holds a slot, so a burst cannot have more than the maximum of
// password checks in flight or failed per window, whatever the concurrency.
func TestLoginLockoutReservationsBoundConcurrentChecks(t *testing.T) {
	for _, backend := range []string{"redis", "memory"} {
		t.Run(backend, func(t *testing.T) {
			useAuthGuardBackend(t, backend)
			useAuthGuardClock(t)
			useAuthGuardSettings(t, true, 120, 5, true, 10, 15)
			ctx := context.Background()
			subjects := LoginLockSubjects(1, "root")
			attempts := make(chan *LoginAttempt, 50)
			var refused atomic.Int32
			var wg sync.WaitGroup
			for range 50 {
				wg.Go(func() {
					attempt, wait := BeginLoginAttempt(ctx, subjects)
					if attempt != nil {
						attempts <- attempt
					} else if wait > 0 {
						refused.Add(1)
					}
				})
			}
			wg.Wait()
			close(attempts)
			admitted := make([]*LoginAttempt, 0, 10)
			for attempt := range attempts {
				admitted = append(admitted, attempt)
			}
			assert.Len(t, admitted, 10)
			assert.Equal(t, int32(40), refused.Load())

			var started atomic.Int32
			for _, attempt := range admitted {
				wg.Go(func() {
					if lockSeconds, lockStarted := attempt.Fail(ctx); lockStarted {
						started.Add(1)
					} else {
						assert.Positive(t, lockSeconds)
					}
				})
			}
			wg.Wait()
			assert.Equal(t, int32(1), started.Load())
			for _, subject := range subjects {
				attempt, wait := BeginLoginAttempt(ctx, []string{subject})
				assert.Nil(t, attempt, subject)
				assert.Equal(t, int64(15*60), wait, subject)
			}
		})
	}
}

func TestLoginLockSubjectsFoldNames(t *testing.T) {
	assert.Equal(t, []string{"u:7"}, LoginLockSubjects(7, ""))
	assert.Empty(t, LoginLockSubjects(0, "  "))
	assert.Empty(t, LoginLockSubjects(0, ""))
	alice := LoginLockSubjects(0, "Alice")
	require.Len(t, alice, 1)
	assert.True(t, strings.HasPrefix(alice[0], "n:"))
	assert.NotContains(t, alice[0], "lice")
	for _, variant := range []string{"alice", " ALICE ", "alicé", "ａｌｉｃｅ", "ÄLICE"} {
		assert.Equal(t, alice, LoginLockSubjects(0, variant), variant)
	}
	assert.NotEqual(t, alice, LoginLockSubjects(0, "bob"))
	assert.Equal(t, append(append([]string{}, alice...), "u:3"), LoginLockSubjects(3, "Alice"))
}

// A failing Redis never refuses or breaks a request: the same call uses the
// in-memory store. A Redis reservation whose Fail cannot reach Redis stays
// in Redis as a failure.
func TestAuthGuardFallsBackToMemoryWhenRedisFails(t *testing.T) {
	redisServer, redisClient := useRateLimitMiniRedis(t)
	useFreshAuthGuardMemory(t)
	useAuthGuardClock(t)
	useAuthGuardSettings(t, true, 120, 5, true, 3, 15)
	ctx := context.Background()

	healthy, wait := BeginLoginAttempt(ctx, LoginLockSubjects(5, "kept"))
	require.NotNil(t, healthy)
	require.Zero(t, wait)
	require.NoError(t, redisClient.Close())
	lockSeconds, started := healthy.Fail(ctx)
	assert.Zero(t, lockSeconds)
	assert.False(t, started)
	members, err := redisServer.ZMembers(authGuardRedisPrefix + "login:fail:u:5")
	require.NoError(t, err)
	assert.Len(t, members, 1, "the reservation stays counted")

	assert.Nil(t, TakePasswordResetEmail(ctx, "down@example.com"))
	assert.Equal(t, PasswordResetEmailCooldownCode, TakePasswordResetEmail(ctx, "down@example.com").Code)
	for range 3 {
		attempt, wait := BeginLoginAttempt(ctx, LoginLockSubjects(6, "down"))
		require.NotNil(t, attempt)
		require.Zero(t, wait)
		lockSeconds, started = attempt.Fail(ctx)
	}
	assert.Equal(t, int64(15*60), lockSeconds)
	assert.True(t, started)
	attempt, wait := BeginLoginAttempt(ctx, LoginLockSubjects(6, ""))
	assert.Nil(t, attempt)
	assert.Equal(t, int64(15*60), wait)
	released, _ := BeginLoginAttempt(ctx, LoginLockSubjects(0, "released"))
	require.NotNil(t, released)
	released.Release(ctx)
	assert.True(t, ClearLoginLockout(ctx, 6, "down"))
	attempt, wait = BeginLoginAttempt(ctx, LoginLockSubjects(6, "down"))
	assert.NotNil(t, attempt)
	assert.Zero(t, wait)
	ClearPasswordResetEmailLimit(ctx, "down@example.com")
	assert.Nil(t, TakePasswordResetEmail(ctx, "down@example.com"))
}

// The in-memory store keeps at most its limit of name and address entries:
// expired ones go first, then the least recently updated one, but never one
// with an active lock (a new name is left untracked instead). Account
// entries are never evicted; expired ones are swept as the map grows.
func TestAuthGuardMemoryStoreBoundsAndPins(t *testing.T) {
	store := newAuthGuardMemory(3)
	const window = int64(60_000)
	now := int64(1_000_000)
	begin := func(subjects ...string) {
		t.Helper()
		require.Zero(t, store.beginLogin(subjects, common.GetUUID(), now, window, 3))
	}
	lock := func(subject string) {
		t.Helper()
		for range 3 {
			begin(subject)
		}
		_, started := store.failLogin([]string{subject}, now, window, 3, now+window)
		require.True(t, started)
	}

	begin("n:a")
	now += 1000
	begin("n:b")
	begin("n:c")
	begin("u:7")
	now += window - 500 // n:a expired, n:b and n:c not yet
	begin("n:d")
	assert.Nil(t, store.lookup("n:a"), "an expired entry is dropped first")
	assert.NotNil(t, store.lookup("n:b"))
	begin("n:e")
	assert.Nil(t, store.lookup("n:b"), "the least recently updated entry is evicted next")
	assert.Len(t, store.bounded, 3)

	now += window + 1000 // everything expired
	lock("n:l1")
	lock("n:l2")
	lock("n:l3")
	begin("n:new")
	assert.Nil(t, store.lookup("n:new"), "a full store of active locks does not track a new name")
	for _, subject := range []string{"n:l1", "n:l2", "n:l3"} {
		assert.Positive(t, store.beginLogin([]string{subject}, common.GetUUID(), now, window, 3), "locks are never evicted")
	}
	assert.NotNil(t, store.lookup("u:7"), "account entries are never evicted")

	store.sweepAt = 1
	begin("u:8")
	assert.Nil(t, store.lookup("u:7"), "expired account entries are swept")
	assert.NotNil(t, store.lookup("u:8"))
}
