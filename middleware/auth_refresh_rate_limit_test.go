package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useAuthRefreshRateLimit sets the session refresh limit and the critical
// limit (both with a 20-minute window) for one test.
func useAuthRefreshRateLimit(t *testing.T, refreshEnabled bool, refreshNum int, criticalNum int) {
	t.Helper()
	previousRefresh := []any{common.AuthRefreshRateLimitEnable, common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration}
	previousCritical := []any{common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration}
	common.AuthRefreshRateLimitEnable, common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration = refreshEnabled, refreshNum, 20*60
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = true, criticalNum, 20*60
	t.Cleanup(func() {
		common.AuthRefreshRateLimitEnable, common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration = previousRefresh[0].(bool), previousRefresh[1].(int), previousRefresh[2].(int64)
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = previousCritical[0].(bool), previousCritical[1].(int), previousCritical[2].(int64)
	})
}

// newAuthRefreshLimitRouter wires sign-in and refresh with their limiters as
// the API router does.
func newAuthRefreshLimitRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	accepted := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	router.POST("/api/user/login", CriticalRateLimit(), accepted)
	router.POST("/api/user/auth/refresh", AuthRefreshRateLimit(), accepted)
	return router
}

var authRefreshTestIPs atomic.Uint32

// uniqueTestClientIP gives a test run client IPs of its own: the in-memory
// limiter keeps its counters for the whole test process.
func uniqueTestClientIP() string {
	n := uint32(time.Now().UnixMicro()) + authRefreshTestIPs.Add(1)
	return fmt.Sprintf("10.%d.%d.%d", byte(n>>16), byte(n>>8), byte(n))
}

func postFrom(router http.Handler, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

// Session refreshes count in a per-IP budget of their own (RF): once the
// critical budget of an IP is used up, its refreshes still pass, up to the
// refresh limit, which then answers 429 with Retry-After like the others.
func TestAuthRefreshRateLimitHasItsOwnBucket(t *testing.T) {
	for _, backend := range []string{"redis", "memory"} {
		t.Run(backend, func(t *testing.T) {
			var redisServer *miniredis.Miniredis
			clientIP, otherIP := "192.0.2.110", "198.51.100.110"
			if backend == "redis" {
				redisServer, _ = useRateLimitMiniRedis(t)
			} else {
				useMemoryRateLimitBackend(t)
				clientIP, otherIP = uniqueTestClientIP(), uniqueTestClientIP()
			}
			useAuthRefreshRateLimit(t, true, 300, 20)
			router := newAuthRefreshLimitRouter(t)
			remoteAddr := clientIP + ":4321"

			for i := range 20 {
				require.Equal(t, http.StatusNoContent, postFrom(router, "/api/user/login", remoteAddr).Code, "login %d", i+1)
			}
			require.Equal(t, http.StatusTooManyRequests, postFrom(router, "/api/user/login", remoteAddr).Code, "the critical budget is used up")

			for i := range 300 {
				require.Equal(t, http.StatusNoContent, postFrom(router, "/api/user/auth/refresh", remoteAddr).Code, "refresh %d", i+1)
			}
			limited := postFrom(router, "/api/user/auth/refresh", remoteAddr)
			assert.Equal(t, http.StatusTooManyRequests, limited.Code)
			assert.Equal(t, "no-store", limited.Header().Get("Cache-Control"))
			retryAfter, err := strconv.Atoi(limited.Header().Get("Retry-After"))
			require.NoError(t, err)
			assert.Positive(t, retryAfter)
			assert.LessOrEqual(t, retryAfter, 20*60)

			// Another IP has both budgets untouched.
			assert.Equal(t, http.StatusNoContent, postFrom(router, "/api/user/auth/refresh", otherIP+":4321").Code)
			assert.Equal(t, http.StatusNoContent, postFrom(router, "/api/user/login", otherIP+":4321").Code)

			if redisServer == nil {
				return
			}
			refreshKey := redisIPRateLimitKey("RF", clientIP)
			assert.Equal(t, "rateLimit:v2:ip:RF:"+clientIP, refreshKey)
			count, err := redisServer.Get(refreshKey)
			require.NoError(t, err)
			assert.Equal(t, "301", count)
			assert.Equal(t, 20*time.Minute, redisServer.TTL(refreshKey))
			criticalCount, err := redisServer.Get(redisIPRateLimitKey("CT", clientIP))
			require.NoError(t, err)
			assert.Equal(t, "21", criticalCount, "refreshes do not count against the critical budget")
		})
	}
}

// Refreshes, unlike sign-in, are not limited at all while
// AUTH_REFRESH_RATE_LIMIT_ENABLE is off.
func TestAuthRefreshRateLimitDisabled(t *testing.T) {
	redisServer, _ := useRateLimitMiniRedis(t)
	useAuthRefreshRateLimit(t, false, 1, 1)
	router := newAuthRefreshLimitRouter(t)
	const clientIP = "192.0.2.112"

	for i := range 5 {
		assert.Equal(t, http.StatusNoContent, postFrom(router, "/api/user/auth/refresh", clientIP+":4321").Code, "refresh %d", i+1)
	}
	assert.False(t, redisServer.Exists(redisIPRateLimitKey("RF", clientIP)))
	assert.Equal(t, http.StatusNoContent, postFrom(router, "/api/user/login", clientIP+":4321").Code)
	assert.Equal(t, http.StatusTooManyRequests, postFrom(router, "/api/user/login", clientIP+":4321").Code)
}
