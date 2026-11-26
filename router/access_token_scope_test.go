package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// accessTokenExemptRoutes are /api routes that never reach dashboard
// authentication, so a personal access token has no rule to satisfy there.
var accessTokenExemptRoutes = []string{
	// Anonymous pages and setup.
	"GET /api/setup",
	"POST /api/setup",
	"GET /api/status",
	"GET /api/uptime/status",
	"GET /api/notice",
	"GET /api/user-agreement",
	"GET /api/privacy-policy",
	"GET /api/about",
	"GET /api/home_page_content",
	"GET /api/ratio_config",
	"GET /api/user/groups",

	// Anonymous account entry points.
	"GET /api/captcha",
	"GET /api/verification",
	"GET /api/reset_password",
	"POST /api/user/reset",
	"POST /api/user/register",
	"GET /api/user/login/encryption-key",
	"POST /api/user/login",
	"POST /api/user/login/2fa",
	"POST /api/user/login/verify",
	"POST /api/user/login/passkey/begin",
	"POST /api/user/login/passkey/finish",
	"POST /api/user/passkey/login/begin",
	"POST /api/user/passkey/login/finish",
	"GET /api/oauth/wechat",
	"GET /api/oauth/telegram/login",
	"GET /api/oauth/telegram/bind/:flow_token",

	// Browser session cookie endpoints.
	"POST /api/user/auth/refresh",
	"POST /api/user/auth/logout",

	// Payment callbacks.
	"POST /api/stripe/webhook",
	"POST /api/creem/webhook",
	"POST /api/waffo/webhook",
	"POST /api/waffo-pancake/webhook/:env",
	"GET /api/user/epay/notify",
	"POST /api/user/epay/notify",
	"GET /api/subscription/epay/notify",
	"POST /api/subscription/epay/notify",
	"GET /api/subscription/epay/return",
	"POST /api/subscription/epay/return",

	// API key authentication (TokenAuthReadOnly).
	"GET /api/usage/token/",
	"GET /api/log/token",
}

// accessTokenHelperRoutes are declared through handlePermissionRoute outside
// channelPermissionRoutes.
var accessTokenHelperRoutes = map[string]authz.Permission{
	"GET /api/task_plugin_options":       authz.TaskPluginBind,
	"GET /api/audit":                     authz.AuditRead,
	"GET /api/user/topup":                authz.BillingRead,
	"POST /api/user/topup/complete":      authz.BillingWrite,
	"GET /api/user/invite_abuse/preview": authz.BillingRead,
}

func newAccessTokenScopeTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// A permission route that is also in the static table panics here.
	require.NotPanics(t, func() {
		SetApiRouter(engine)
		SetDashboardRouter(engine)
		SetRelayRouter(engine)
		SetTaskPluginProtocolRouter(engine)
		SetVideoRouter(engine)
		SetTaskRouter(engine)
	})
	return engine
}

func TestAccessTokenRouteRulesCoverEveryDashboardRoute(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)
	exempt := map[string]bool{}
	for _, key := range accessTokenExemptRoutes {
		exempt[key] = true
	}
	registered := map[string]bool{}
	panel := map[string]bool{}
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") && !strings.HasPrefix(route.Path, "/pg/") {
			continue
		}
		key := route.Method + " " + route.Path
		registered[key] = true
		if !exempt[key] {
			panel[key] = true
		}
	}
	for _, key := range accessTokenExemptRoutes {
		assert.True(t, registered[key], "exempt route %s is no longer registered", key)
	}

	declared := map[string]int{}
	for _, key := range middleware.AccessTokenRouteRuleKeys() {
		declared[key]++
	}
	for key := range panel {
		assert.Equal(t, 1, declared[key], "panel route %s must be declared exactly once", key)
	}
	for key := range declared {
		assert.True(t, panel[key], "declared route %s is not a registered panel route", key)
	}

	permissionRoutes := map[string]authz.Permission{}
	for _, route := range channelPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/channel", route.path)] = route.permission
	}
	for _, route := range subscriptionAdminPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/subscription/admin", route.path)] = route.permission
	}
	for _, route := range redemptionPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/redemption", route.path)] = route.permission
	}
	for key, permission := range accessTokenHelperRoutes {
		permissionRoutes[key] = permission
	}
	for key, permission := range permissionRoutes {
		rule, ok := middleware.AccessTokenRouteRule(key)
		require.True(t, ok, key)
		assert.Equal(t, "scope", rule.Kind(), key)
		assert.Equal(t, service.AccessTokenScopeOf(permission), rule.Scope(), key)
	}
}

func TestAccessTokenCatalogCoversEveryPermission(t *testing.T) {
	catalog := service.AccessTokenCatalog(1, common.RoleRootUser)
	scopes := map[string]bool{}
	resources := map[string]int{}
	for _, group := range catalog {
		for _, resource := range group.Resources {
			resources[resource.Resource]++
			for _, action := range resource.Actions {
				scopes[resource.Resource+":"+action.Action] = true
			}
		}
	}
	for _, permission := range authz.AllPermissions() {
		assert.True(t, scopes[service.AccessTokenScopeOf(permission)], "root catalog lacks %s", service.AccessTokenScopeOf(permission))
	}
	for resource, count := range resources {
		assert.Equal(t, 1, count, "resource %s appears in more than one catalog entry", resource)
	}
}

func TestAccessTokenVerificationScopesAreAllListed(t *testing.T) {
	files, err := filepath.Glob("../service/*.go")
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	var scopes []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if !strings.HasPrefix(name.Name, "VerificationScope") || i >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[i].(*ast.BasicLit)
					require.True(t, ok, name.Name)
					scope, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					scopes = append(scopes, scope)
				}
			}
		}
	}
	require.NotEmpty(t, scopes)
	for _, scope := range scopes {
		_, listed := service.AccessTokenVerificationScope(scope)
		assert.True(t, listed, "verification scope %s has no access token rule", scope)
	}
}

// setupRouterAuthzTest points the models at an empty SQLite database with the
// dashboard authentication tables and initializes authz on it as the master.
func setupRouterAuthzTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret, previousMaster := common.RedisEnabled, common.SessionSecret, common.IsMasterNode
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/router.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuditLog{}, &model.UserAccessToken{}, &model.Option{}, &model.CasbinRule{}, &model.AuthzRole{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.SessionSecret = "router-authz-test-secret"
	common.IsMasterNode = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret, common.IsMasterNode = previousRedis, previousSecret, previousMaster
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(time.Now().Unix()))
	require.NoError(t, authz.Init(db))
	return db
}

// createRouterTokenUser creates an enabled user of role and a never expiring
// personal access token granting scopes.
func createRouterTokenUser(t *testing.T, db *gorm.DB, name string, role int, scopes ...string) (int, string) {
	t.Helper()
	user := &model.User{Username: name, Password: "password-placeholder", Role: role, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "router-" + name}
	require.NoError(t, db.Create(user).Error)
	suffix, err := common.GenerateRandomCharsKey(43)
	require.NoError(t, err)
	raw := model.AccessTokenPrefix + suffix
	token := &model.UserAccessToken{Name: "router token", TokenHash: model.AccessTokenFingerprint(raw), TokenHint: model.AccessTokenHint(raw)}
	require.NoError(t, token.SetScopes(scopes))
	require.NoError(t, model.CreateUserAccessToken(user.Id, token, service.AccessTokenMaxPerUser))
	return user.Id, raw
}

func TestAccessTokenCatalogOmitsRechargeScopesForDeputyAdmins(t *testing.T) {
	db := setupRouterAuthzTest(t)
	const admin, deputy = 70, 71
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return authz.AssignDeputyInTx(tx, deputy) }))

	catalogResources := func(userID int) map[string][]string {
		resources := map[string][]string{}
		for _, group := range service.AccessTokenCatalog(userID, common.RoleAdminUser) {
			for _, resource := range group.Resources {
				for _, action := range resource.Actions {
					resources[resource.Resource] = append(resources[resource.Resource], action.Action)
				}
			}
		}
		return resources
	}
	adminResources := catalogResources(admin)
	assert.Equal(t, []string{"read", "write"}, adminResources["billing"])
	assert.Equal(t, []string{"read", "write"}, adminResources["redemption"])
	deputyResources := catalogResources(deputy)
	assert.NotContains(t, deputyResources, "billing")
	assert.NotContains(t, deputyResources, "redemption")
	assert.Equal(t, adminResources["channel"], deputyResources["channel"])
	assert.Equal(t, adminResources["user"], deputyResources["user"])

	_, err := service.NormalizeAccessTokenScopes(deputy, common.RoleAdminUser, []string{"user:read", "billing:read"})
	assert.ErrorIs(t, err, service.ErrAccessTokenScopeForbidden)
	scopes, err := service.NormalizeAccessTokenScopes(admin, common.RoleAdminUser, []string{"user:read", "billing:read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"billing:read", "user:read"}, scopes)
}

// A deputy admin is refused every recharge route through the real router, also
// with a token that still carries the recharge scopes; admin and root pass.
func TestRechargeRoutesRefuseDeputyAdmins(t *testing.T) {
	db := setupRouterAuthzTest(t)
	scopes := []string{"billing:read", "billing:write", "redemption:read", "redemption:write"}
	_, rootToken := createRouterTokenUser(t, db, "recharge-root", common.RoleRootUser, scopes...)
	_, adminToken := createRouterTokenUser(t, db, "recharge-admin", common.RoleAdminUser, scopes...)
	deputyID, deputyToken := createRouterTokenUser(t, db, "recharge-deputy", common.RoleAdminUser, scopes...)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return authz.AssignDeputyInTx(tx, deputyID) }))
	engine := newAccessTokenScopeTestEngine(t)

	for _, route := range rechargeRoutes {
		for _, caller := range []struct {
			name, token string
			refused     bool
		}{{"deputy", deputyToken, true}, {"admin", adminToken, false}, {"root", rootToken, false}} {
			request := httptest.NewRequest(route.method, strings.ReplaceAll(route.path, ":id", "1"), strings.NewReader("{}"))
			request.Header.Set("Authorization", "Bearer "+caller.token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			name := caller.name + " " + route.method + " " + route.path
			if caller.refused {
				assert.Equal(t, http.StatusForbidden, response.Code, name)
				assert.Contains(t, response.Body.String(), "Unauthorized, insufficient privileges", name)
				assert.NotContains(t, response.Body.String(), "ACCESS_TOKEN_SCOPE_DENIED", name)
				continue
			}
			// The handler ran: it answers 200, here with its own error about the
			// empty test database.
			assert.Equal(t, http.StatusOK, response.Code, name)
			assert.NotContains(t, response.Body.String(), "insufficient privileges", name)
		}
	}
}

// PUT /api/option/model_square is root only like every /api/option route, and
// a personal access token needs option:write for it.
func TestModelSquareOptionsRouteIsRootOnly(t *testing.T) {
	db := setupRouterAuthzTest(t)
	previousOptions := common.OptionMap
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		for key, value := range previousConfig {
			if operation_setting.IsModelSquareOptionKey(key) {
				operation_setting.LoadModelSquareOption(key, value)
			}
		}
	})
	common.OptionMap = map[string]string{}
	_, rootToken := createRouterTokenUser(t, db, "square-root", common.RoleRootUser, "option:write")
	_, readOnlyRootToken := createRouterTokenUser(t, db, "square-root-read", common.RoleRootUser, "option:read")
	_, adminToken := createRouterTokenUser(t, db, "square-admin", common.RoleAdminUser, "option:write")
	engine := newAccessTokenScopeTestEngine(t)
	put := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/api/option/model_square", strings.NewReader(`{"options":{"model_square_setting.show_tag_filter":true}}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		return response
	}

	for _, caller := range []struct{ name, token, code string }{
		{"admin", adminToken, "AUTH_INSUFFICIENT_PRIVILEGE"},
		{"root without option:write", readOnlyRootToken, "ACCESS_TOKEN_SCOPE_DENIED"},
	} {
		response := put(caller.token)
		assert.Equal(t, http.StatusForbidden, response.Code, caller.name)
		assert.Contains(t, response.Body.String(), `"code":"`+caller.code+`"`, caller.name)
		assert.False(t, operation_setting.GetModelSquareDisplay().FilterSections.Tags, caller.name)
	}
	stored := func() map[string]string {
		var options []model.Option
		require.NoError(t, db.Find(&options).Error)
		values := make(map[string]string, len(options))
		for _, option := range options {
			values[option.Key] = option.Value
		}
		return values
	}
	assert.NotContains(t, stored(), operation_setting.ModelSquareShowTagFilterOptionKey, "a refused caller saves nothing")

	response := put(rootToken)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"success":true`)
	assert.True(t, operation_setting.GetModelSquareDisplay().FilterSections.Tags)
	assert.Equal(t, "true", stored()[operation_setting.ModelSquareShowTagFilterOptionKey])
}
