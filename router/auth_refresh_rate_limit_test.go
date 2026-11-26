package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The session refresh route counts in a budget of its own (RF); logout, like
// sign-in, stays in the critical one (CT). Once an IP used up its critical
// budget, its page loads can still refresh the session.
func TestAuthRefreshRouteHasItsOwnRateLimit(t *testing.T) {
	previous := []any{
		common.RedisEnabled, common.GlobalApiRateLimitEnable,
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration,
		common.AuthRefreshRateLimitEnable, common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration,
	}
	t.Cleanup(func() {
		common.RedisEnabled, common.GlobalApiRateLimitEnable = previous[0].(bool), previous[1].(bool)
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = previous[2].(bool), previous[3].(int), previous[4].(int64)
		common.AuthRefreshRateLimitEnable, common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration = previous[5].(bool), previous[6].(int), previous[7].(int64)
	})
	common.RedisEnabled, common.GlobalApiRateLimitEnable = false, false
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = true, 2, 20*60
	common.AuthRefreshRateLimitEnable, common.AuthRefreshRateLimitNum, common.AuthRefreshRateLimitDuration = true, 3, 20*60

	// The limiters are built with the routes, so they see the values above.
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies(nil))
	SetApiRouter(engine)
	post := func(path string) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.RemoteAddr = "203.0.113.91:1234"
		engine.ServeHTTP(recorder, request)
		return recorder.Code
	}

	for i := range 2 {
		assert.NotEqual(t, http.StatusTooManyRequests, post("/api/user/auth/logout"), "logout %d", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, post("/api/user/auth/logout"), "logout uses the critical budget")
	for i := range 3 {
		assert.NotEqual(t, http.StatusTooManyRequests, post("/api/user/auth/refresh"), "refresh %d", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, post("/api/user/auth/refresh"), "refresh has a budget of its own")
}
