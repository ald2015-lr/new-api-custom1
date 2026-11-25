package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelDefaultBaseURLsRequireReadPermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/default_base_urls", authz.ChannelRead, controller.GetChannelDefaultBaseURLs)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/channel/default_base_urls", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestChannelStatusRoutesUseExpectedPermissions(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/priority/order", authz.ChannelRead, controller.GetChannelPriorityOrder)
	assertChannelRoutePermission(t, http.MethodPost, "/priority/reorder", authz.ChannelWrite, controller.ReorderChannelPriority)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/vllm/status", authz.ChannelRead, controller.GetVLLMChannelStatus)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/sglang/status", authz.ChannelRead, controller.GetSGLangChannelStatus)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/gemini_free_key_usage", authz.ChannelRead, controller.GetGeminiFreeKeyUsage)
	assertChannelRoutePermission(t, http.MethodPost, "/:id/moderation_test", authz.ChannelOperate, controller.TestChannelModeration)
	assertChannelRoutePermission(t, http.MethodPost, "/:id/status", authz.ChannelOperate, controller.UpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/status/batch", authz.ChannelOperate, controller.BatchUpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
}

func TestChannelDeleteRoutesUseSensitiveWritePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodDelete, "/:id", authz.ChannelSensitiveWrite, controller.DeleteChannel)
	assertChannelRoutePermission(t, http.MethodPost, "/batch", authz.ChannelSensitiveWrite, controller.DeleteChannelBatch)
	assertChannelRoutePermission(t, http.MethodDelete, "/disabled", authz.ChannelSensitiveWrite, controller.DeleteDisabledChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/tag", authz.ChannelWrite, controller.EditTagChannels)
	assertChannelRoutePermission(t, http.MethodPost, "/batch/tag", authz.ChannelWrite, controller.BatchSetChannelTag)
}

func TestPluginLoginRoutesUseExpectedPermissions(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodPost, "/:id/plugin_login", authz.ChannelSensitiveWrite, controller.StartPluginLogin)
	assertChannelRoutePermission(t, http.MethodPost, "/:id/plugin_login/continue", authz.ChannelSensitiveWrite, controller.ContinuePluginLogin)
	assertChannelRoutePermission(t, http.MethodPost, "/plugin_login/callback", authz.ChannelSensitiveWrite, controller.CompletePluginLoginCallback)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/plugin_credentials", authz.ChannelRead, controller.GetPluginCredentials)
}

func TestChannelStatusRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")

	require.NotPanics(t, func() {
		registerChannelRoutes(api)
	})
}

func assertChannelRoutePermission(t *testing.T, method string, path string, permission authz.Permission, handler any) {
	t.Helper()
	for _, route := range channelPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, permission, route.permission)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}

// rechargeRoutes are the routes a deputy admin is refused (R1-R22), with the
// permission each one requires. The scope strings equal the static access
// token scopes these routes had, so stored tokens keep their meaning.
var rechargeRoutes = []struct {
	method, path string
	permission   authz.Permission
	scope        string
	handler      gin.HandlerFunc
}{
	{http.MethodGet, "/api/user/topup", authz.BillingRead, "billing:read", controller.GetAllTopUps},
	{http.MethodPost, "/api/user/topup/complete", authz.BillingWrite, "billing:write", controller.AdminCompleteTopUp},
	{http.MethodGet, "/api/user/invite_abuse/preview", authz.BillingRead, "billing:read", controller.GetInviteAbusePreview},
	{http.MethodGet, "/api/subscription/admin/plans", authz.BillingRead, "billing:read", controller.AdminListSubscriptionPlans},
	{http.MethodPost, "/api/subscription/admin/plans", authz.BillingWrite, "billing:write", controller.AdminCreateSubscriptionPlan},
	{http.MethodPut, "/api/subscription/admin/plans/:id", authz.BillingWrite, "billing:write", controller.AdminUpdateSubscriptionPlan},
	{http.MethodPatch, "/api/subscription/admin/plans/:id", authz.BillingWrite, "billing:write", controller.AdminUpdateSubscriptionPlanStatus},
	{http.MethodPost, "/api/subscription/admin/bind", authz.BillingWrite, "billing:write", controller.AdminBindSubscription},
	{http.MethodPost, "/api/subscription/admin/plans/:id/subscriptions/reset", authz.BillingWrite, "billing:write", controller.AdminResetPlanSubscriptions},
	{http.MethodGet, "/api/subscription/admin/users/:id/subscriptions", authz.BillingRead, "billing:read", controller.AdminListUserSubscriptions},
	{http.MethodPost, "/api/subscription/admin/users/:id/subscriptions", authz.BillingWrite, "billing:write", controller.AdminCreateUserSubscription},
	{http.MethodPost, "/api/subscription/admin/users/:id/subscriptions/reset", authz.BillingWrite, "billing:write", controller.AdminResetUserSubscriptionsByPlan},
	{http.MethodPost, "/api/subscription/admin/user_subscriptions/:id/invalidate", authz.BillingWrite, "billing:write", controller.AdminInvalidateUserSubscription},
	{http.MethodDelete, "/api/subscription/admin/user_subscriptions/:id", authz.BillingWrite, "billing:write", controller.AdminDeleteUserSubscription},
	{http.MethodGet, "/api/redemption/", authz.RedemptionRead, "redemption:read", controller.GetAllRedemptions},
	{http.MethodGet, "/api/redemption/search", authz.RedemptionRead, "redemption:read", controller.SearchRedemptions},
	{http.MethodGet, "/api/redemption/:id", authz.RedemptionRead, "redemption:read", controller.GetRedemption},
	{http.MethodPost, "/api/redemption/", authz.RedemptionWrite, "redemption:write", controller.AddRedemption},
	{http.MethodPost, "/api/redemption/batch", authz.RedemptionWrite, "redemption:write", controller.DeleteRedemptionBatch},
	{http.MethodPut, "/api/redemption/", authz.RedemptionWrite, "redemption:write", controller.UpdateRedemption},
	{http.MethodDelete, "/api/redemption/invalid", authz.RedemptionWrite, "redemption:write", controller.DeleteInvalidRedemption},
	{http.MethodDelete, "/api/redemption/:id", authz.RedemptionWrite, "redemption:write", controller.DeleteRedemption},
}

func TestRechargeRoutesUseRechargePermissions(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)
	handlers := map[string]uintptr{}
	for _, route := range engine.Routes() {
		handlers[route.Method+" "+route.Path] = reflect.ValueOf(route.HandlerFunc).Pointer()
	}
	declared := map[string]authz.Permission{}
	for _, route := range subscriptionAdminPermissionRoutes {
		declared[route.method+" "+joinPaths("/api/subscription/admin", route.path)] = route.permission
	}
	for _, route := range redemptionPermissionRoutes {
		declared[route.method+" "+joinPaths("/api/redemption", route.path)] = route.permission
	}
	for key, permission := range accessTokenHelperRoutes {
		if strings.HasPrefix(key, "GET /api/user/") || strings.HasPrefix(key, "POST /api/user/") {
			declared[key] = permission
		}
	}
	assert.Len(t, declared, len(rechargeRoutes))
	for _, route := range rechargeRoutes {
		key := route.method + " " + route.path
		assert.Equal(t, route.permission, declared[key], key)
		assert.Equal(t, reflect.ValueOf(route.handler).Pointer(), handlers[key], key)
		rule, ok := middleware.AccessTokenRouteRule(key)
		require.True(t, ok, key)
		assert.Equal(t, route.scope, rule.Scope(), key)
	}
}
