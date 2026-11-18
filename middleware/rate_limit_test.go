package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

func TestRedisFailurePolicies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, redisClient := useRateLimitMiniRedis(t)
	require.NoError(t, redisClient.Close())

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/ip", rateLimitFactory(1, 30, "FAIL-IP"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET(
		"/user",
		func(c *gin.Context) { c.Set("id", 7) },
		userRateLimitFactory(1, 30, "FAIL-USER"),
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	router.GET("/email", EmailVerificationRateLimit(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	ipResponse := performRateLimitRequest(router, "/ip", "192.0.2.60:12345")
	assert.Equal(t, http.StatusInternalServerError, ipResponse.Code)
	assert.Empty(t, ipResponse.Body.String())
	userResponse := performRateLimitRequest(router, "/user", "192.0.2.61:12345")
	assert.Equal(t, http.StatusInternalServerError, userResponse.Code)
	assert.Empty(t, userResponse.Body.String())
	assert.Equal(t, http.StatusNoContent, performRateLimitRequest(router, "/email", "192.0.2.62:12345").Code)
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
// turn every dashboard page load into an error page. API limiters keep the
// fail-closed policy asserted by TestRedisFailurePolicies.
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

func TestCaptchaRateLimitAllowsThirtyImagesPerQuarterHour(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer, _ := useRateLimitMiniRedis(t)
	useRegisterRateLimit(t, false, 1, 1)
	router := newRegisterLimitRouter(t)

	for range CaptchaRateLimitNum {
		require.Equal(t, http.StatusNoContent, performRegisterLimitRequest(router, http.MethodGet, "/api/captcha", "192.0.2.94:4321").Code)
	}
	limited := performRegisterLimitRequest(router, http.MethodGet, "/api/captcha", "192.0.2.94:4321")
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.JSONEq(t, `{"success":false,"message":"Too many captcha requests. Please try again later.","message_key":"Too many captcha requests. Please try again later."}`, limited.Body.String())
	assert.Equal(t, "900", limited.Header().Get("Retry-After"))
	assert.Equal(t, 15*time.Minute, redisServer.TTL(redisIPRateLimitKey(CaptchaRateLimitMark, "192.0.2.94")))
}
