package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupManageUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled, previousSecret := common.RedisEnabled, common.SessionSecret
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	databaseTypes := map[string]common.DatabaseType{
		"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL,
	}
	require.Contains(t, databaseTypes, dialect)
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	db, _ := newAuditTestDatabase(t, dialect, dsn)
	logDB := db
	if os.Getenv("TEST_MANAGE_USER_SEPARATE_LOG_DB") == "1" {
		logDB, _ = newAuditTestDatabase(t, dialect, dsn)
	}
	model.DB, model.LOG_DB = db, logDB
	common.RedisEnabled = false
	common.SessionSecret = "manage-user-test-secret"
	common.SetDatabaseTypes(databaseTypes[dialect], databaseTypes[dialect])

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, common.SessionSecret = previousRedisEnabled, previousSecret
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if logDB != db {
			sqlLogDB, err := logDB.DB()
			if err == nil {
				_ = sqlLogDB.Close()
			}
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.UserAccessToken{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s, separate log database: %v", dialect, version, logDB != db)
	return db
}

func performManageUserRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return performVerifiedManageUserRequest(t, body, service.AuthIdentity{UserID: 9999}, "")
}

// performVerifiedManageUserRequest drives ManageUser as the root operator from
// a dashboard login session, carrying the step-up proof status or role changes need.
func performVerifiedManageUserRequest(t *testing.T, body string, identity service.AuthIdentity, proof string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if proof != "" {
		c.Request.Header.Set("X-Security-Proof", proof)
	}
	c.Set("id", identity.UserID)
	c.Set("role", common.RoleRootUser)
	c.Set("username", "root-operator")
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	c.Set(common.RequestIdKey, "quota-test-request")
	ManageUser(c)
	return recorder
}

// manageUserProof signs the root operator in and issues a password-method proof
// bound to one ManageUser operation.
func manageUserProof(t *testing.T, db *gorm.DB, operation service.VerificationOperation) (service.AuthIdentity, string) {
	t.Helper()
	operator := createQuotaTestOperator(t, db, common.RoleRootUser)
	require.NoError(t, model.PublishUserAuthCache(operator.Id))
	bundle, err := service.CreateLoginSession(operator.Id, "password", "127.0.0.1", "manage-user-test")
	require.NoError(t, err)
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	binding, err := service.BindVerificationOperation(operation)
	require.NoError(t, err)
	proof, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
	require.NoError(t, err)
	return identity, proof
}

func TestManageUserDisableAdvancesAuthVersionOnceAndRevokesSession(t *testing.T) {
	db := setupManageUserTestDB(t)
	now := time.Now().Unix()
	user := model.User{
		Username: "managed-disable-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "managed-disable-session", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "refresh-hash", LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"disable"}`, user.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"disable"}`, user.Id), identity, proof)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, updated.Status)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var session model.UserSession
	require.NoError(t, db.First(&session, "sid = ?", "managed-disable-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
}

func TestManageUserDemoteAdvancesAuthVersionAndRevokesSessionsOnce(t *testing.T) {
	db := setupManageUserTestDB(t)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))

	now := time.Now().Unix()
	user := model.User{
		Username: "managed-demote-user", Password: "password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	assignTestDeputy(t, db, user.Id)
	for _, sid := range []string{"managed-demote-session-one", "managed-demote-session-two"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password",
			LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
	}

	sessionUpdateCount := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:count_demote_session_updates", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			sessionUpdateCount++
		}
	}))

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"demote"}`, user.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"demote"}`, user.Id), identity, proof)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.RoleCommonUser, updated.Role)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var sessions []model.UserSession
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("sid asc").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_demote", session.RevokedReason)
	}
	assert.Equal(t, 1, sessionUpdateCount)
	deputy, err := authz.IsDeputy(user.Id)
	require.NoError(t, err)
	assert.False(t, deputy, "demoting a deputy admin removes the assignment")
}

func TestManageUserDeleteReturnsImmediatelyAndUnknownActionFails(t *testing.T) {
	db := setupManageUserTestDB(t)
	deleted := model.User{
		Username: "managed-delete-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "delete-aff",
	}
	require.NoError(t, db.Create(&deleted).Error)

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserDelete, Context: []byte(fmt.Sprintf(`{"user_id":%d}`, deleted.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, deleted.Id), identity, proof)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	var deletedCount int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("id = ? AND deleted_at IS NOT NULL", deleted.Id).Count(&deletedCount).Error)
	assert.EqualValues(t, 1, deletedCount)

	unchanged := model.User{
		Username: "managed-unknown-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "unknown-aff",
	}
	require.NoError(t, db.Create(&unchanged).Error)
	recorder = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"unknown"}`, unchanged.Id))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	require.NoError(t, db.First(&unchanged, unchanged.Id).Error)
	assert.EqualValues(t, 1, unchanged.AuthVersion)
	assert.Equal(t, common.UserStatusEnabled, unchanged.Status)
}

func createQuotaTestOperator(t *testing.T, db *gorm.DB, role int) model.User {
	t.Helper()
	if role == 0 {
		role = common.RoleRootUser
	}
	operator := model.User{Id: 9999, Username: "root-operator", Password: "root-operator-hash", Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "root-operator-aff"}
	require.NoError(t, db.Create(&operator).Error)
	return operator
}

// initManageUserAuthz seeds the authorization baseline on db as the master
// node, so a full admin holds the admin permissions.
func initManageUserAuthz(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))
}

func assignTestDeputy(t *testing.T, db *gorm.DB, userID int) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return authz.AssignDeputyInTx(tx, userID) }))
}

// manageActionProof issues the root operator's proof for one ManageUser action.
func manageActionProof(t *testing.T, identity service.AuthIdentity, userID int, action string) string {
	t.Helper()
	binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":%q}`, userID, action))})
	require.NoError(t, err)
	proof, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
	require.NoError(t, err)
	return proof
}

// userCasbinRules lists the casbin rows of a user as "ptype resource action effect".
func userCasbinRules(t *testing.T, db *gorm.DB, userID int) []string {
	t.Helper()
	var rules []model.CasbinRule
	require.NoError(t, db.Where("v0 = ?", authz.UserSubject(userID)).Order("ptype, v1, v2").Find(&rules).Error)
	result := make([]string, 0, len(rules))
	for _, rule := range rules {
		result = append(result, strings.TrimSpace(rule.Ptype+" "+rule.V1+" "+rule.V2+" "+rule.V3))
	}
	return result
}

func TestManageUserQuotaRefusedForDeputyAdmins(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		deputy bool
	}{{"add", true}, {"subtract", true}, {"override", true}, {"invalid", true}, {"add", false}} {
		name := tc.mode + " by deputy"
		if !tc.deputy {
			name = tc.mode + " by full admin"
		}
		t.Run(name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			initManageUserAuthz(t, db)
			operator := createQuotaTestOperator(t, db, common.RoleAdminUser)
			if tc.deputy {
				assignTestDeputy(t, db, operator.Id)
			}
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000, AffCode: "quota-owner-aff"}
			require.NoError(t, db.Create(&user).Error)

			body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":500}`, user.Id, tc.mode)
			recorder := adminUserRequest(http.MethodPost, "/api/user/manage", body, "", service.AuthIdentity{UserID: operator.Id}, common.RoleAdminUser, nil, ManageUser)
			require.NoError(t, db.First(&user, user.Id).Error)
			var logCount, quotaAuditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeTopup).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Where("action LIKE ?", "user.quota_%").Count(&quotaAuditCount).Error)
			if !tc.deputy {
				assert.Contains(t, recorder.Body.String(), `"success":true`)
				assert.Equal(t, 1500, user.Quota)
				assert.EqualValues(t, 1, logCount)
				assert.EqualValues(t, 1, quotaAuditCount)
				return
			}
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "Unauthorized, insufficient privileges")
			assert.Equal(t, 1000, user.Quota)
			assert.Zero(t, logCount)
			assert.Zero(t, quotaAuditCount)
		})
	}
}

func TestManageUserConvertsBetweenFullAndDeputyAdmin(t *testing.T) {
	db := setupManageUserTestDB(t)
	initManageUserAuthz(t, db)
	now := time.Now().Unix()
	createTarget := func(name string, role int) model.User {
		user := model.User{Username: name, Password: "password", Role: role, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: name}
		require.NoError(t, db.Create(&user).Error)
		require.NoError(t, db.Create(&model.UserSession{
			SID: name + "-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive,
			RefreshHash: "refresh-" + name, LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
		return user
	}
	reactivateSession := func(user model.User) {
		require.NoError(t, db.Model(&model.UserSession{}).Where("user_id = ?", user.Id).Updates(map[string]any{"status": model.UserSessionStatusActive, "revoked_reason": ""}).Error)
	}
	assertConverted := func(user model.User, role int, deputy bool, authVersion int64) {
		t.Helper()
		var stored model.User
		require.NoError(t, db.First(&stored, user.Id).Error)
		assert.Equal(t, role, stored.Role)
		assert.Equal(t, authVersion, stored.AuthVersion, "the auth version moves once")
		isDeputy, err := authz.IsDeputy(user.Id)
		require.NoError(t, err)
		assert.Equal(t, deputy, isDeputy)
		var session model.UserSession
		require.NoError(t, db.First(&session, "user_id = ?", user.Id).Error)
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_role_change", session.RevokedReason)
	}
	member := createTarget("becomes-deputy", common.RoleCommonUser)
	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"set_deputy"}`, member.Id))})

	// A common user becomes a deputy admin: role and assignment in one commit.
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"set_deputy"}`, member.Id), identity, proof)
	require.Contains(t, recorder.Body.String(), `"success":true`, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"role":10`)
	assertConverted(member, common.RoleAdminUser, true, 2)
	assert.False(t, authz.Can(member.Id, common.RoleAdminUser, authz.BillingRead))
	assert.True(t, authz.Can(member.Id, common.RoleAdminUser, authz.ChannelRead))
	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.manage").Order("id desc").First(&audit).Error)
	params, err := common.Marshal(audit.Other.Op.Params)
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(`{"action":"set_deputy","username":"becomes-deputy","id":%d,"verification_method":"password","target_user_id":%d}`, member.Id, member.Id), string(params))

	// A full admin with recharge overrides becomes a deputy admin: the
	// recharge rows go, the other overrides stay.
	admin := createTarget("admin-to-deputy", common.RoleAdminUser)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return authz.SetUserPermissionsInTx(tx, admin.Id, authz.PermissionsMap{
			authz.ResourceChannel: {authz.ActionSensitiveWrite: true},
			authz.ResourceBilling: {authz.ActionWrite: false},
		})
	}))
	recorder = performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"set_deputy"}`, admin.Id), identity, manageActionProof(t, identity, admin.Id, "set_deputy"))
	require.Contains(t, recorder.Body.String(), `"success":true`, recorder.Body.String())
	assertConverted(admin, common.RoleAdminUser, true, 2)
	assert.Equal(t, []string{"g role:deputy", "p channel sensitive_write allow"}, userCasbinRules(t, db, admin.Id))
	assert.True(t, authz.Can(admin.Id, common.RoleAdminUser, authz.ChannelSensitiveWrite))

	// Back to a full admin: the admin baseline applies again, recharge included.
	reactivateSession(admin)
	recorder = performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"set_admin"}`, admin.Id), identity, manageActionProof(t, identity, admin.Id, "set_admin"))
	require.Contains(t, recorder.Body.String(), `"success":true`, recorder.Body.String())
	assertConverted(admin, common.RoleAdminUser, false, 3)
	assert.Equal(t, []string{"p channel sensitive_write allow"}, userCasbinRules(t, db, admin.Id))
	assert.True(t, authz.Can(admin.Id, common.RoleAdminUser, authz.BillingWrite))

	// Refusals change nothing.
	root := createTarget("root-target", common.RoleRootUser)
	staleMarker := createTarget("stale-marker", common.RoleCommonUser)
	assignTestDeputy(t, db, staleMarker.Id)
	for _, tc := range []struct {
		name, action, key string
		target            model.User
		operatorRole      int
	}{
		{"admin caller", "set_deputy", "Regular administrators cannot promote other users to administrator", staleMarker, common.RoleAdminUser},
		{"admin caller to admin", "set_admin", "Regular administrators cannot promote other users to administrator", staleMarker, common.RoleAdminUser},
		{"root target", "set_deputy", "Cannot demote super administrator user", root, common.RoleRootUser},
		{"already deputy", "set_deputy", "This user is already a deputy admin", member, common.RoleRootUser},
		{"full admin", "set_admin", "This user is not a deputy admin", admin, common.RoleRootUser},
		{"common user with a stale assignment", "set_admin", "This user is not a deputy admin", staleMarker, common.RoleRootUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before model.User
			require.NoError(t, db.First(&before, tc.target.Id).Error)
			rulesBefore := userCasbinRules(t, db, tc.target.Id)
			recorder := adminUserRequest(http.MethodPost, "/api/user/manage", fmt.Sprintf(`{"id":%d,"action":%q}`, tc.target.Id, tc.action), "", identity, tc.operatorRole, nil, ManageUser)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), `"message_key":"`+tc.key+`"`)
			var after model.User
			require.NoError(t, db.First(&after, tc.target.Id).Error)
			assert.Equal(t, before.Role, after.Role)
			assert.Equal(t, before.AuthVersion, after.AuthVersion)
			assert.Equal(t, rulesBefore, userCasbinRules(t, db, tc.target.Id))
		})
	}

	// The role change and the assignment commit together: a failed role
	// update leaves no assignment behind.
	failing := createTarget("failing-promotion", common.RoleCommonUser)
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:fail_role_change", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("role change unavailable"))
		}
	}))
	recorder = performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"set_deputy"}`, failing.Id), identity, manageActionProof(t, identity, failing.Id, "set_deputy"))
	require.NoError(t, db.Callback().Update().Remove("test:fail_role_change"))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Empty(t, userCasbinRules(t, db, failing.Id))
	require.NoError(t, db.First(&failing, failing.Id).Error)
	assert.Equal(t, common.RoleCommonUser, failing.Role)
}

func TestDeleteUserClearsAuthorizationOfTheDeletedId(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ExternalIdentityClaim{}, &model.Token{}, &model.TwoFABackupCode{}, &model.UserOAuthBinding{}))
	initManageUserAuthz(t, db)
	deputy := model.User{Username: "deleted-deputy", Password: "password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "deleted-deputy"}
	require.NoError(t, db.Create(&deputy).Error)
	assignTestDeputy(t, db, deputy.Id)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return authz.SetUserPermissionsInTx(tx, deputy.Id, authz.PermissionsMap{authz.ResourceChannel: {authz.ActionSensitiveWrite: true}})
	}))
	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserDelete, Context: []byte(fmt.Sprintf(`{"user_id":%d}`, deputy.Id))})
	recorder := adminUserRequest(http.MethodDelete, "/api/user/:id", "", proof, identity, common.RoleRootUser, gin.Params{{Key: "id", Value: fmt.Sprint(deputy.Id)}}, DeleteUser)
	require.Contains(t, recorder.Body.String(), `"success":true`, recorder.Body.String())
	assert.Empty(t, userCasbinRules(t, db, deputy.Id), "a reused id must not inherit the deleted user's rows")
	assert.False(t, authz.Can(deputy.Id, common.RoleAdminUser, authz.ChannelSensitiveWrite))
}

func TestCreateUserDeputyAdmin(t *testing.T) {
	db := setupManageUserTestDB(t)
	initManageUserAuthz(t, db)
	deputyContext := service.VerificationOperation{Scope: service.VerificationScopeAdminUserCreate, Context: []byte(`{"role":10,"admin_role":"deputy"}`)}
	identity, proof := manageUserProof(t, db, deputyContext)
	issue := func() string {
		binding, err := service.BindVerificationOperation(deputyContext)
		require.NoError(t, err)
		proof, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
		require.NoError(t, err)
		return proof
	}
	create := func(body, proof string) *httptest.ResponseRecorder {
		return adminUserRequest(http.MethodPost, "/api/user/", body, proof, identity, common.RoleRootUser, nil, CreateUser)
	}

	recorder := create(`{"username":"new-deputy","password":"deputy-password-1","role":10,"admin_role":"deputy","admin_permissions":{"channel":{"sensitive_write":true},"billing":{"read":false,"write":false}}}`, proof)
	require.Contains(t, recorder.Body.String(), `"success":true`, recorder.Body.String())
	var created model.User
	require.NoError(t, db.Where("username = ?", "new-deputy").First(&created).Error)
	assert.Equal(t, common.RoleAdminUser, created.Role)
	assert.Equal(t, []string{"g role:deputy", "p channel sensitive_write allow"}, userCasbinRules(t, db, created.Id))
	assert.True(t, authz.Can(created.Id, common.RoleAdminUser, authz.ChannelSensitiveWrite))
	assert.False(t, authz.Can(created.Id, common.RoleAdminUser, authz.BillingRead))
	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "user.create").First(&audit).Error)
	params, err := common.Marshal(audit.Other.Op.Params)
	require.NoError(t, err)
	assert.Contains(t, string(params), `"admin_role":"deputy"`)

	// A recharge grant in the matrix rolls the whole creation back.
	recorder = create(`{"username":"rich-deputy","password":"deputy-password-1","role":10,"admin_role":"deputy","admin_permissions":{"redemption":{"read":true}}}`, issue())
	assert.Contains(t, recorder.Body.String(), `"message_key":"Deputy admins cannot hold recharge permissions"`)
	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "rich-deputy").Count(&count).Error)
	assert.Zero(t, count)
	deputies, err := authz.AllDeputyUserIDs()
	require.NoError(t, err)
	assert.Equal(t, []int{created.Id}, deputies)

	for _, body := range []string{
		`{"username":"deputy-member","password":"deputy-password-1","role":1,"admin_role":"deputy"}`,
		`{"username":"odd-kind","password":"deputy-password-1","role":10,"admin_role":"root"}`,
	} {
		recorder = create(body, "")
		assert.Contains(t, recorder.Body.String(), `"message_key":"Invalid parameters"`, body)
	}
	require.NoError(t, db.Model(&model.User{}).Where("username IN ?", []string{"deputy-member", "odd-kind"}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpdateUserRefusesRechargePermissionsForDeputyAdmins(t *testing.T) {
	db := setupManageUserTestDB(t)
	initManageUserAuthz(t, db)
	deputy := model.User{Username: "edited-deputy", DisplayName: "Deputy", Password: "password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "edited-deputy"}
	require.NoError(t, db.Create(&deputy).Error)
	assignTestDeputy(t, db, deputy.Id)
	updateContext := service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: []byte(fmt.Sprintf(`{"user_id":%d}`, deputy.Id))}
	identity, proof := manageUserProof(t, db, updateContext)
	update := func(body, proof string) *httptest.ResponseRecorder {
		return adminUserRequest(http.MethodPut, "/api/user/", body, proof, identity, common.RoleRootUser, nil, UpdateUser)
	}

	recorder := update(fmt.Sprintf(`{"id":%d,"username":"edited-deputy","display_name":"Changed","admin_permissions":{"channel":{"sensitive_write":true},"billing":{"read":true,"write":false}}}`, deputy.Id), proof)
	assert.Contains(t, recorder.Body.String(), `"message_key":"Deputy admins cannot hold recharge permissions"`)
	var stored model.User
	require.NoError(t, db.First(&stored, deputy.Id).Error)
	assert.Equal(t, "Deputy", stored.DisplayName)
	assert.Equal(t, []string{"g role:deputy"}, userCasbinRules(t, db, deputy.Id))

	// admin_role in the body changes nothing: conversions go through ManageUser.
	binding, err := service.BindVerificationOperation(updateContext)
	require.NoError(t, err)
	proof, _, err = service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
	require.NoError(t, err)
	recorder = update(fmt.Sprintf(`{"id":%d,"username":"edited-deputy","display_name":"Changed","admin_role":"admin","admin_permissions":{"channel":{"sensitive_write":true},"billing":{"read":false,"write":false}}}`, deputy.Id), proof)
	require.Contains(t, recorder.Body.String(), `"success":true`, recorder.Body.String())
	require.NoError(t, db.First(&stored, deputy.Id).Error)
	assert.Equal(t, "Changed", stored.DisplayName)
	assert.Equal(t, []string{"g role:deputy", "p channel sensitive_write allow"}, userCasbinRules(t, db, deputy.Id))
}

func TestManageUserQuotaRecordsTopupAndAudit(t *testing.T) {
	for _, tc := range []struct {
		name, mode, action, content string
		value, wantQuota            int
	}{
		{"add", "add", "user.quota_add", "Increased user quota by 500", 500, 1500},
		{"subtract", "subtract", "user.quota_subtract", "Decreased user quota by 500", 500, 500},
		{"override_up", "override", "user.quota_override", "Overrode user quota from 1000 to 1500", 1500, 1500},
		{"override_down", "override", "user.quota_override", "Overrode user quota from 1000 to 500", 500, 500},
		{"override_unchanged", "override", "user.quota_override", "Overrode user quota from 1000 to 1000", 1000, 1000},
		{"override_zero", "override", "user.quota_override", "Overrode user quota from 1000 to 0", 0, 0},
		{"override_negative", "override", "user.quota_override", "Overrode user quota from 1000 to -1", -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000, AffCode: "quota-owner-aff"}
			require.NoError(t, db.Create(&user).Error)
			createQuotaTestOperator(t, db, common.RoleRootUser)

			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, tc.wantQuota, user.Quota)

			logs, total, err := model.GetAllLogs(model.LogFilter{Type: model.LogTypeTopup}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, logs, 1)
			assert.Equal(t, user.Id, logs[0].UserId)
			assert.Equal(t, user.Username, logs[0].Username)
			assert.Equal(t, tc.content, logs[0].Content)
			model.FormatAdminLogs(logs)
			var other model.AuditOther
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.Equal(t, &model.AuditAdminInfo{AdminID: 9999, AdminUsername: "root-operator", AdminRole: common.RoleRootUser, AuthMethod: "session"}, other.AdminInfo)

			logs, total, err = model.GetUserLogs(user.Id, model.LogFilter{Type: model.LogTypeTopup}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, logs, 1)
			other = model.AuditOther{}
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.Nil(t, other.AdminInfo)
			require.NotNil(t, other.Op)
			assert.Equal(t, tc.action, other.Op.Action)
			params, err := common.Marshal(other.Op.Params)
			require.NoError(t, err)
			expectedParams := model.AuditFields{"target_user_id": user.Id, "target_username": user.Username, "mode": tc.mode, "requested_quota": tc.value, "from": 1000, "to": tc.wantQuota}
			if tc.mode != "override" {
				expectedParams["quota"] = tc.value
			}
			expected, err := common.Marshal(expectedParams)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.Equal(t, "quota-test-request", logs[0].RequestId)
			assert.Empty(t, logs[0].Ip, "recipient logs must not disclose the administrator IP")

			logs, total, err = model.GetUserLogs(9999, model.LogFilter{Type: model.LogTypeTopup}, 0, 20)
			require.NoError(t, err)
			assert.Zero(t, total)
			assert.Empty(t, logs)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.Len(t, audits, 1)
			assert.Equal(t, 9999, audits[0].UserId)
			assert.Equal(t, "root-operator", audits[0].Username)
			assert.Equal(t, tc.action, audits[0].Action)
			assert.True(t, audits[0].Success)
			params, err = common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.Equal(t, "quota-test-request", audits[0].RequestId)
		})
	}
}

func TestManageUserQuotaFailuresDoNotRecordTopup(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		value      int
		failUpdate bool
	}{
		{"zero_add", "add", 0, false},
		{"negative_subtract", "subtract", -1, false},
		{"invalid_mode", "invalid", 500, false},
		{"add_update_error", "add", 500, true},
		{"subtract_update_error", "subtract", 500, true},
		{"override_update_error", "override", 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, common.RoleRootUser)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			if tc.failUpdate {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:fail_quota_update", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("quota update unavailable"))
					}
				}))
			}
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 1000, user.Quota)
			var logCount, auditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&auditCount).Error)
			assert.Zero(t, logCount)
			assert.EqualValues(t, 1, auditCount)
			var audit model.AuditLog
			require.NoError(t, model.LOG_DB.First(&audit).Error)
			assert.False(t, audit.Success)
			params, err := common.Marshal(audit.Other.Op.Params)
			require.NoError(t, err)
			assert.NotContains(t, string(params), `"from"`)
			assert.NotContains(t, string(params), `"to"`)
			assert.NotContains(t, string(params), `"target_username"`)
			assert.NotContains(t, string(params), "quota update unavailable")
			reason := "invalid_parameters"
			if tc.failUpdate {
				reason = "database_error"
			}
			assert.Contains(t, string(params), `"failure_reason":"`+reason+`"`)
			assert.Contains(t, string(params), fmt.Sprintf(`"requested_quota":%d`, tc.value))
		})
	}
}

func TestManageUserQuotaLogFailureKeepsSuccessfulAdjustment(t *testing.T) {
	for _, failedTable := range []string{"logs", "audit_logs"} {
		t.Run(failedTable, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, common.RoleRootUser)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, model.LOG_DB.Callback().Create().Before("gorm:create").Register("test:fail_quota_log", func(tx *gorm.DB) {
				if tx.Statement.Table == failedTable {
					tx.AddError(errors.New("quota log unavailable"))
				}
			}))
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":500}`, user.Id))
			assert.Contains(t, recorder.Body.String(), `"success":true`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 1500, user.Quota)
			var logCount, auditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&auditCount).Error)
			if failedTable == "logs" {
				assert.Zero(t, logCount)
				assert.EqualValues(t, 1, auditCount)
			} else {
				assert.EqualValues(t, 1, logCount)
				assert.Zero(t, auditCount)
			}
		})
	}
}

func TestManageUserQuotaTargetsAndWalletBounds(t *testing.T) {
	for _, tc := range []struct {
		name, mode, reason                                string
		before, value, targetID, targetRole, operatorRole int
		deleted, failRead                                 bool
	}{
		{name: "zero_id", mode: "add", value: 1, reason: "invalid_parameters"},
		{name: "negative_id", mode: "subtract", value: 1, targetID: -1, reason: "invalid_parameters"},
		{name: "missing", mode: "override", value: 1, targetID: 12345, reason: "target_not_found"},
		{name: "deleted", mode: "add", value: 1, targetID: 1, deleted: true, reason: "target_not_found"},
		{name: "peer_admin", mode: "add", value: 1, targetID: 1, targetRole: common.RoleAdminUser, operatorRole: common.RoleAdminUser, reason: "permission_denied"},
		{name: "higher_role", mode: "override", value: 1, targetID: 1, targetRole: common.RoleRootUser, operatorRole: common.RoleAdminUser, reason: "permission_denied"},
		{name: "read_error", mode: "add", value: 1, targetID: 1, failRead: true, reason: "database_error"},
		{name: "add_overflow", mode: "add", before: common.MaxWalletQuota, value: 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "subtract_underflow", mode: "subtract", before: -common.MaxWalletQuota, value: 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_add", mode: "add", value: common.MaxWalletQuota + 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_subtract", mode: "subtract", value: common.MaxWalletQuota + 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_override", mode: "override", value: -common.MaxWalletQuota - 1, targetID: 1, reason: "quota_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			initManageUserAuthz(t, db)
			createQuotaTestOperator(t, db, tc.operatorRole)
			user := model.User{Id: 1, Username: "private-target-name", Role: tc.targetRole, Quota: tc.before}
			require.NoError(t, db.Create(&user).Error)
			if tc.deleted {
				require.NoError(t, db.Delete(&user).Error)
			}
			if tc.failRead {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:quota_read_error", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" && len(tx.Statement.Selects) == 0 {
						tx.AddError(errors.New("private database error"))
					}
				}))
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, tc.targetID, tc.mode, tc.value)))
			role := tc.operatorRole
			if role == 0 {
				role = common.RoleRootUser
			}
			c.Set("id", 9999)
			c.Set("role", role)
			ManageUser(c)
			require.Contains(t, recorder.Body.String(), `"success":false`)
			if tc.failRead {
				require.NoError(t, db.Callback().Query().Remove("test:quota_read_error"))
			}
			require.NoError(t, db.Unscoped().First(&user, user.Id).Error)
			assert.Equal(t, tc.before, user.Quota)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.Len(t, audits, 1)
			assert.False(t, audits[0].Success)
			params, err := common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			expected, err := common.Marshal(model.AuditFields{"target_user_id": tc.targetID, "mode": tc.mode, "requested_quota": tc.value, "failure_reason": tc.reason})
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.NotContains(t, audits[0].Content, user.Username)
			var count int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestManageUserQuotaMiddlewareKeepsOneOperationPerRequest(t *testing.T) {
	db := setupManageUserTestDB(t)
	pat := "quota-middleware-test-token"
	operator := model.User{Id: 9999, Username: "root-operator", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, AccessToken: &pat, Quota: 1000}
	require.NoError(t, db.Create(&operator).Error)
	router := gin.New()
	router.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	router.POST("/api/user/manage", middleware.AdminAuth(), ManageUser)
	for _, tc := range []struct {
		body, action string
		success      bool
	}{
		{`{"id":9999,"action":"add_quota","mode":"add","value":100}`, "user.quota_add", true},
		{`{"id":9999,"action":"add_quota","mode":"subtract","value":0}`, "user.quota_subtract", false},
		{`{"id":9999,"action":"add_quota","mode":"invalid","value":1}`, "generic", false},
		{`{"id":`, "generic", false},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(tc.body))
		request.Header.Set("Authorization", "Bearer "+pat)
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, tc.success))
		requestID := recorder.Header().Get(common.RequestIdKey)
		require.NotEmpty(t, requestID)
		var audits []model.AuditLog
		require.NoError(t, model.LOG_DB.Where("request_id = ?", requestID).Find(&audits).Error)
		require.Len(t, audits, 2, "one operation audit and one PAT request audit")
		for _, audit := range audits {
			assert.Equal(t, tc.success, audit.Success)
			if audit.Category != model.AuditCategoryOperation {
				continue
			}
			assert.Equal(t, tc.action, audit.Action)
			assert.Equal(t, operator.Id, audit.UserId)
			assert.Equal(t, "/api/user/manage", audit.Route)
			if tc.success {
				params, err := common.Marshal(audit.Other.Op.Params)
				require.NoError(t, err)
				assert.Contains(t, string(params), `"target_user_id":9999`)
				assert.Contains(t, string(params), `"target_username":"root-operator"`)
			}
		}
		var count int64
		require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("request_id = ?", requestID).Count(&count).Error)
		if tc.success {
			assert.EqualValues(t, 1, count)
		} else {
			assert.Zero(t, count)
		}
	}
	require.NoError(t, db.First(&operator, operator.Id).Error)
	assert.Equal(t, 1100, operator.Quota)
}

func TestManageUserQuotaConcurrentSnapshots(t *testing.T) {
	db := setupManageUserTestDB(t)
	user := model.User{Username: "concurrent-quota", Quota: 1000}
	require.NoError(t, db.Create(&user).Error)
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:concurrent_quota_start", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			ready.Done()
			<-release
		}
	}))
	type result struct {
		adjustment *model.UserQuotaAdjustment
		err        error
		value      int
	}
	results := make(chan result, 2)
	for _, value := range []int{10, 20} {
		go func(value int) {
			adjustment, err := model.AdjustUserQuota(user.Id, common.RoleRootUser, "add", value)
			results <- result{adjustment, err, value}
		}(value)
	}
	ready.Wait()
	close(release)
	var committed []model.UserQuotaAdjustment
	for range 2 {
		result := <-results
		if result.err != nil {
			require.True(t, common.UsingMainDatabase(common.DatabaseTypeSQLite), "row-locking databases must serialize both adjustments: %v", result.err)
			assert.Contains(t, strings.ToLower(result.err.Error()), "locked")
			assert.Nil(t, result.adjustment)
			continue
		}
		require.NotNil(t, result.adjustment)
		assert.Equal(t, result.value, result.adjustment.After-result.adjustment.Before)
		committed = append(committed, *result.adjustment)
	}
	require.NoError(t, db.Callback().Query().Remove("test:concurrent_quota_start"))
	require.NotEmpty(t, committed)
	sort.Slice(committed, func(i, j int) bool { return committed[i].Before < committed[j].Before })
	balance := 1000
	for _, adjustment := range committed {
		assert.Equal(t, balance, adjustment.Before)
		balance = adjustment.After
	}
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, balance, user.Quota)
}

func TestManageUserQuotaCacheUsesCommittedIntegerDifference(t *testing.T) {
	for _, tc := range []struct {
		name, mode                               string
		before, cached, value, after, wantCached int
		failUpdate, failCache, missingCache      bool
	}{
		{name: "add_preserves_reservations", mode: "add", before: 1000, cached: 900, value: 500, after: 1500, wantCached: 1400},
		{name: "subtract_preserves_reservations", mode: "subtract", before: 1000, cached: 900, value: 500, after: 500, wantCached: 400},
		{name: "override_preserves_reservations", mode: "override", before: 1000, cached: 900, value: 2000, after: 2000, wantCached: 1900},
		{name: "large_odd_difference", mode: "override", before: common.MaxWalletQuota - 1, cached: common.MaxWalletQuota - 1, value: -common.MaxWalletQuota, after: -common.MaxWalletQuota, wantCached: -common.MaxWalletQuota},
		{name: "rollback_does_not_change_cache", mode: "subtract", before: 1000, cached: 900, value: 500, after: 1000, wantCached: 900, failUpdate: true},
		{name: "cache_error_keeps_committed_change", mode: "add", before: 1000, value: 500, after: 1500, failCache: true},
		{name: "missing_cache_is_not_partially_created", mode: "add", before: 1000, value: 500, after: 1500, missingCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			operator := createQuotaTestOperator(t, db, common.RoleRootUser)
			server := miniredis.RunT(t)
			oldRDB := common.RDB
			common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
			common.RedisEnabled = true
			t.Cleanup(func() { _ = common.RDB.Close(); common.RDB = oldRDB })
			user := model.User{Username: "cached-quota", Quota: tc.before, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			cache, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			assert.Equal(t, tc.before, cache.Quota)
			_, err = model.GetUserCache(operator.Id)
			require.NoError(t, err)
			keys := server.Keys()
			var quotaKey string
			for _, key := range keys {
				if server.HGet(key, "Id") == strconv.Itoa(user.Id) && server.HGet(key, "Quota") != "" {
					quotaKey = key
					break
				}
			}
			require.NotEmpty(t, quotaKey)
			server.HSet(quotaKey, "Quota", strconv.Itoa(tc.cached))
			if tc.missingCache {
				server.Del(quotaKey)
			}
			if tc.failCache {
				server.SetError("ERR quota cache unavailable")
			}
			if tc.failUpdate {
				require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cache_quota_rollback", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("quota rollback"))
					}
				}))
			}
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, !tc.failUpdate))
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, tc.after, user.Quota)
			if tc.missingCache {
				// Log username lookup may hydrate the whole user after commit.
				if server.Exists(quotaKey) {
					assert.Equal(t, strconv.Itoa(user.Id), server.HGet(quotaKey, "Id"))
					assert.NotEmpty(t, server.HGet(quotaKey, "CacheSchema"))
					assert.Equal(t, strconv.Itoa(tc.after), server.HGet(quotaKey, "Quota"))
				}
			} else if !tc.failCache {
				assert.Equal(t, strconv.Itoa(tc.wantCached), server.HGet(quotaKey, "Quota"))
			}
		})
	}
}

// Saving notification settings starts from what the user stored: fields the
// form does not manage stay, and switches an older client leaves out keep
// their stored values. Only administrators change the channel and upstream
// switches, and a threshold of 0 means the site default.
func TestUpdateUserSettingKeepsWhatTheRequestLeavesOut(t *testing.T) {
	on, off := true, false
	stored := dto.UserSetting{
		NotifyType:                       dto.NotifyTypeBark,
		BarkUrl:                          "https://api.day.app/key/{{title}}/{{content}}",
		QuotaWarningThreshold:            500000,
		QuotaWarningEnabled:              &off,
		ChannelNotifyEnabled:             &on,
		UpstreamModelUpdateNotifyEnabled: true,
		Language:                         "zhCN",
		SidebarModules:                   `{"chat":{"enabled":true}}`,
		BillingPreference:                "wallet_only",
	}
	kept := func(setting dto.UserSetting) dto.UserSetting {
		setting.Language, setting.SidebarModules, setting.BillingPreference = stored.Language, stored.SidebarModules, stored.BillingPreference
		return setting
	}
	switchesChanged := `{"notify_type":"email","quota_warning_threshold":0,"quota_warning_enabled":true,"channel_notify_enabled":false,"upstream_model_update_notify_enabled":false}`
	for _, tc := range []struct {
		name        string
		role        int
		body        string
		wantSuccess bool
		want        dto.UserSetting
	}{
		{
			name:        "older client without the new switches",
			role:        common.RoleAdminUser,
			body:        `{"notify_type":"webhook","quota_warning_threshold":2000,"webhook_url":"https://example.com/hook","webhook_secret":"hook-secret","accept_unset_model_ratio_model":true,"record_ip_log":true}`,
			wantSuccess: true,
			want: kept(dto.UserSetting{
				NotifyType: dto.NotifyTypeWebhook, WebhookUrl: "https://example.com/hook", WebhookSecret: "hook-secret",
				QuotaWarningThreshold: 2000, QuotaWarningEnabled: &off, ChannelNotifyEnabled: &on, UpstreamModelUpdateNotifyEnabled: true,
				AcceptUnsetRatioModel: true, RecordIpLog: true,
			}),
		},
		{
			name:        "administrator changes the switches and uses the site default threshold",
			role:        common.RoleAdminUser,
			body:        switchesChanged,
			wantSuccess: true,
			want:        kept(dto.UserSetting{NotifyType: dto.NotifyTypeEmail, QuotaWarningEnabled: &on, ChannelNotifyEnabled: &off}),
		},
		{
			name:        "user cannot change the administrator switches",
			role:        common.RoleCommonUser,
			body:        switchesChanged,
			wantSuccess: true,
			want:        kept(dto.UserSetting{NotifyType: dto.NotifyTypeEmail, QuotaWarningEnabled: &on, ChannelNotifyEnabled: &on, UpstreamModelUpdateNotifyEnabled: true}),
		},
		{
			name: "negative threshold",
			role: common.RoleCommonUser,
			body: `{"notify_type":"email","quota_warning_threshold":-1}`,
			want: stored,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			setting, err := common.Marshal(stored)
			require.NoError(t, err)
			user := model.User{Username: "setting-owner", Role: tc.role, Status: common.UserStatusEnabled, AffCode: "setting-owner-aff", Setting: string(setting)}
			require.NoError(t, db.Create(&user).Error)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/user/setting", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("id", user.Id)
			UpdateUserSetting(c)

			assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, tc.wantSuccess))
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, tc.want, user.GetSetting())
		})
	}
}

// Invite abuse rule and the admin users list.

// setupInviteAbuseControllerTest is setupManageUserTestDB with the tables and
// the options of the invite abuse rule: threshold 2, minimum redeemed quota
// 500000, paid inviters exempt, rule off. None of the options is saved, so
// the rule's check of the saved options finds this node's values.
func setupInviteAbuseControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}, &model.Redemption{}, &model.InviteAbuseBan{}, &model.Option{}))
	enabled, threshold, minRedeem := common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota
	exemptPaid := common.InviteAbuseBanExemptPaidInviter
	registerEnabled, newUserQuota := common.RegisterEnabled, common.QuotaForNewUser
	t.Cleanup(func() {
		common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota = enabled, threshold, minRedeem
		common.InviteAbuseBanExemptPaidInviter = exemptPaid
		common.RegisterEnabled, common.QuotaForNewUser = registerEnabled, newUserQuota
	})
	common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota = false, 2, 500000
	common.InviteAbuseBanExemptPaidInviter = true
	common.RegisterEnabled, common.QuotaForNewUser = true, 0
	return db
}

func createInviteAbuseControllerUser(t *testing.T, db *gorm.DB, name string, inviterId int) model.User {
	t.Helper()
	user := model.User{
		Username: name, DisplayName: name, Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		Group: "default", AffCode: name, InviterId: inviterId, AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func inviteAbuseControllerStatus(t *testing.T, db *gorm.DB, id int) int {
	t.Helper()
	var user model.User
	require.NoError(t, db.Select("status").Where("id = ?", id).Take(&user).Error)
	return user.Status
}

func performInviteAbuseGet(t *testing.T, path string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/user/invite_abuse/preview", GetInviteAbusePreview)
	router.GET("/api/user/", GetAllUsers)
	router.GET("/api/user/search", SearchUsers)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	return recorder.Body.String()
}

func TestInviteAbusePreviewEndpoint(t *testing.T) {
	db := setupInviteAbuseControllerTest(t)
	farmer := createInviteAbuseControllerUser(t, db, "farmer", 0)
	for _, name := range []string{"farmer-1", "farmer-2", "farmer-3"} {
		createInviteAbuseControllerUser(t, db, name, farmer.Id)
	}
	redeemer := createInviteAbuseControllerUser(t, db, "redeemer", 0)
	createInviteAbuseControllerUser(t, db, "redeemer-1", redeemer.Id)
	redeemed := createInviteAbuseControllerUser(t, db, "redeemer-2", redeemer.Id)
	require.NoError(t, db.Create(&model.Redemption{Key: "invite-abuse-preview-code-000001", Name: "preview", Status: common.RedemptionCodeStatusUsed, Quota: 100, UsedUserId: redeemed.Id}).Error)
	// The patron recharged itself: abusive only while paid inviters are not exempt.
	patron := createInviteAbuseControllerUser(t, db, "patron", 0)
	createInviteAbuseControllerUser(t, db, "patron-1", patron.Id)
	createInviteAbuseControllerUser(t, db, "patron-2", patron.Id)
	require.NoError(t, db.Create(&model.TopUp{UserId: patron.Id, Amount: 1, Money: 7, TradeNo: "invite-abuse-preview-patron", Status: common.TopUpStatusSuccess}).Error)

	item := func(user model.User, invites int) string {
		return fmt.Sprintf(`{"id":%d,"username":%q,"display_name":%q,"status":1,"invite_count":%d,"invitees_to_disable":%d}`, user.Id, user.Username, user.DisplayName, invites, invites)
	}
	exempted := `{"threshold":2,"min_redeem_quota":500000,"exempt_paid_inviter":true,"abusive_inviters":2,"inviters_to_disable":2,"invitees_to_disable":5,"items":[` +
		item(farmer, 3) + `,` + item(redeemer, 2) + `],"truncated":false}`
	notExempted := `{"threshold":2,"min_redeem_quota":500000,"exempt_paid_inviter":false,"abusive_inviters":3,"inviters_to_disable":3,"invitees_to_disable":7,"items":[` +
		item(farmer, 3) + `,` + item(patron, 2) + `,` + item(redeemer, 2) + `],"truncated":false}`
	for _, tc := range []struct {
		name        string
		savedExempt bool
		query, want string
	}{
		{"saved settings, rule off", true, "", exempted},
		{"saved settings without the paid-inviter exemption", false, "", notExempted},
		{"unsaved paid-inviter exemption", false, "?exempt_paid_inviter=true", exempted},
		{"unsaved paid-inviter exemption turned off", true, "?exempt_paid_inviter=false", notExempted},
		{"unsaved minimum redeemed quota", true, "?min_redeem_quota=100",
			`{"threshold":2,"min_redeem_quota":100,"exempt_paid_inviter":true,"abusive_inviters":1,"inviters_to_disable":1,"invitees_to_disable":3,"items":[` + item(farmer, 3) + `],"truncated":false}`},
		{"unsaved threshold", true, "?threshold=4&min_redeem_quota=0",
			`{"threshold":4,"min_redeem_quota":0,"exempt_paid_inviter":true,"abusive_inviters":0,"inviters_to_disable":0,"invitees_to_disable":0,"items":[],"truncated":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			common.InviteAbuseBanExemptPaidInviter = tc.savedExempt
			var response securityEnrollmentResponse
			require.NoError(t, common.UnmarshalJsonStr(performInviteAbuseGet(t, "/api/user/invite_abuse/preview"+tc.query), &response))
			assert.True(t, response.Success)
			assert.JSONEq(t, tc.want, string(response.Data))
		})
	}
	for _, tc := range []struct{ query, key string }{
		{"?threshold=0", "Invite count threshold must be an integer between 1 and 100000"},
		{"?threshold=100001", "Invite count threshold must be an integer between 1 and 100000"},
		{"?threshold=two", "Invite count threshold must be an integer between 1 and 100000"},
		{"?min_redeem_quota=-1", "Minimum redeemed quota must be a non-negative integer"},
		{"?exempt_paid_inviter=yes", "Value must be true or false"},
	} {
		body := performInviteAbuseGet(t, "/api/user/invite_abuse/preview"+tc.query)
		assert.Contains(t, body, `"success":false`, tc.query)
		assert.Contains(t, body, fmt.Sprintf(`"message_key":%q`, tc.key), tc.query)
	}
	var disabled int64
	require.NoError(t, db.Model(&model.User{}).Where("status <> ?", common.UserStatusEnabled).Count(&disabled).Error)
	assert.Zero(t, disabled, "a preview changes nothing")
}

// The settings form saves the invite abuse rule's options it changed in one
// request, all of them or none: a value the server refuses saves none of the
// others, and only the rule's options are accepted, so the checks PUT
// /api/option/ makes for other options cannot be skipped. Each saved option
// is audited by name, as PUT /api/option/ does. The rule stays off, so no
// save here holds back the sign-up checks of later tests.
func TestUpdateInviteAbuseOptionsSavesAllOrNothing(t *testing.T) {
	db := setupInviteAbuseControllerTest(t)
	previousOptions := common.OptionMap
	t.Cleanup(func() { common.OptionMap = previousOptions })
	common.OptionMap = map[string]string{}
	put := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/option/invite_abuse", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", 9999)
		c.Set("role", common.RoleRootUser)
		c.Set("username", "root-operator")
		UpdateInviteAbuseOptions(c)
		return recorder
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

	recorder := put(`{"options":{"InviteAbuseBanEnabled":false,"InviteAbuseBanThreshold":3,"InviteAbuseBanMinRedeemQuota":1000000,"InviteAbuseBanExemptPaidInviter":false}}`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	saved := map[string]string{
		"InviteAbuseBanEnabled": "false", "InviteAbuseBanThreshold": "3",
		"InviteAbuseBanMinRedeemQuota": "1000000", "InviteAbuseBanExemptPaidInviter": "false",
	}
	assert.Equal(t, saved, stored())
	assert.Equal(t, model.InviteAbuseSettings{Threshold: 3, MinRedeemQuota: 1_000_000}, model.CurrentInviteAbuseSettings())
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Order("id").Find(&audits).Error)
	params := make([]string, 0, len(audits))
	for _, audit := range audits {
		assert.Equal(t, "option.update", audit.Action)
		assert.Equal(t, 9999, audit.UserId)
		require.NotNil(t, audit.Other.Op)
		encoded, err := common.Marshal(audit.Other.Op.Params)
		require.NoError(t, err)
		params = append(params, string(encoded))
	}
	assert.Equal(t, []string{
		`{"key":"InviteAbuseBanEnabled"}`, `{"key":"InviteAbuseBanExemptPaidInviter"}`,
		`{"key":"InviteAbuseBanMinRedeemQuota"}`, `{"key":"InviteAbuseBanThreshold"}`,
	}, params)

	for _, tc := range []struct {
		name, body, message string
		status              int
	}{
		{"a value the server refuses", `{"options":{"InviteAbuseBanExemptPaidInviter":true,"InviteAbuseBanMinRedeemQuota":-1,"InviteAbuseBanThreshold":4}}`, "Minimum redeemed quota must be a non-negative integer", http.StatusOK},
		{"an option that is not the rule's", `{"options":{"InviteAbuseBanThreshold":4,"QuotaForInviter":1000}}`, "Invalid parameters", http.StatusBadRequest},
		{"no option", `{"options":{}}`, "Invalid parameters", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := put(tc.body)
			assert.Equal(t, tc.status, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"message_key":%q`, tc.message))
			assert.Equal(t, saved, stored(), "nothing is saved")
			assert.Equal(t, 3, model.CurrentInviteAbuseSettings().Threshold)
		})
	}
}

// Enabling an account the rule disabled exempts it: the next sign-up with its
// invite code no longer disables anybody.
func TestManageUserEnableExemptsInviteAbuseBan(t *testing.T) {
	db := setupInviteAbuseControllerTest(t)
	common.InviteAbuseBanEnabled = true
	farmer := createInviteAbuseControllerUser(t, db, "farmer", 0)
	first := createInviteAbuseControllerUser(t, db, "farmer-1", farmer.Id)
	second := model.User{Username: "farmer-2", DisplayName: "farmer-2", Role: common.RoleCommonUser, InviterId: farmer.Id}
	require.NoError(t, second.Insert(farmer.Id))
	for _, user := range []model.User{farmer, first, second} {
		assert.Equal(t, common.UserStatusDisabled, inviteAbuseControllerStatus(t, db, user.Id), user.Username)
	}

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"enable"}`, farmer.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"enable"}`, farmer.Id), identity, proof)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Equal(t, common.UserStatusEnabled, inviteAbuseControllerStatus(t, db, farmer.Id))
	var ban model.InviteAbuseBan
	require.NoError(t, db.Where("user_id = ?", farmer.Id).Take(&ban).Error)
	assert.Positive(t, ban.ReleasedAt)
	assert.Equal(t, identity.UserID, ban.ReleasedBy)

	third := model.User{Username: "farmer-3", DisplayName: "farmer-3", Role: common.RoleCommonUser, InviterId: farmer.Id}
	require.NoError(t, third.Insert(farmer.Id))
	assert.Equal(t, common.UserStatusEnabled, inviteAbuseControllerStatus(t, db, third.Id))
	assert.Equal(t, common.UserStatusEnabled, inviteAbuseControllerStatus(t, db, farmer.Id))
}

// An OAuth sign-up that makes its inviter abusive is refused at once instead
// of being signed in; a sign-up below the threshold signs in as before.
func TestOAuthSignUpRefusedWhenInviteAbuseDisablesTheNewUser(t *testing.T) {
	db := setupInviteAbuseControllerTest(t)
	common.InviteAbuseBanEnabled = true
	farmer := createInviteAbuseControllerUser(t, db, "farmer", 0)
	createInviteAbuseControllerUser(t, db, "farmer-1", farmer.Id)
	fresh := createInviteAbuseControllerUser(t, db, "fresh", 0)
	for _, tc := range []struct {
		name, affiliate, providerUserID string
		success                         bool
	}{
		{"second invited user of the farmer", farmer.AffCode, "abuse-oauth-farmer", false},
		{"first invited user of a new inviter", fresh.AffCode, "abuse-oauth-fresh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth/github", nil)
			flow := &model.AuthFlow{Payload: fmt.Sprintf(`{"affiliate_code":%q}`, tc.affiliate)}
			handleOAuthLogin(c, &oauth.GitHubProvider{}, &oauth.OAuthUser{ProviderUserID: tc.providerUserID, Username: tc.providerUserID}, &oauth.OAuthToken{}, flow)

			var created model.User
			require.NoError(t, db.Where("github_id = ?", tc.providerUserID).Take(&created).Error)
			if tc.success {
				assert.Contains(t, recorder.Body.String(), `"success":true`)
				assert.Contains(t, recorder.Body.String(), `"access_token"`)
				assert.Equal(t, common.UserStatusEnabled, created.Status)
				return
			}
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), `"message":"User has been banned"`)
			assert.Equal(t, common.UserStatusDisabled, created.Status)
			assert.Equal(t, common.UserStatusDisabled, inviteAbuseControllerStatus(t, db, farmer.Id))
			sessions, err := model.CountActiveUserSessions(created.Id, time.Now().Unix())
			require.NoError(t, err)
			assert.Zero(t, sessions)
		})
	}
}

// The invite code of a disabled or deleted account links nobody: a password
// registration or an OAuth sign-up that carries it succeeds without an
// inviter, an invite reward or an invite abuse check. Under the same settings
// an enabled inviter's code does all three.
func TestSignUpIgnoresInviteCodesOfDisabledOrDeletedInviters(t *testing.T) {
	db := setupInviteAbuseControllerTest(t)
	require.NoError(t, db.AutoMigrate(&model.InviteReward{}))
	payment := operation_setting.GetPaymentSetting()
	previousConfirmed, previousTerms := payment.ComplianceConfirmed, payment.ComplianceTermsVersion
	previousPassword, previousVerification, previousLimit := common.PasswordRegisterEnabled, common.EmailVerificationEnabled, common.RegisterIPRateLimitEnabled
	previousInviter, previousInvitee, previousRecharge := common.QuotaForInviter, common.QuotaForInvitee, common.InviteRechargeThresholdQuota
	t.Cleanup(func() {
		payment.ComplianceConfirmed, payment.ComplianceTermsVersion = previousConfirmed, previousTerms
		common.PasswordRegisterEnabled, common.EmailVerificationEnabled, common.RegisterIPRateLimitEnabled = previousPassword, previousVerification, previousLimit
		common.QuotaForInviter, common.QuotaForInvitee, common.InviteRechargeThresholdQuota = previousInviter, previousInvitee, previousRecharge
	})
	// A linked sign-up records invite rewards (pending until a recharge) and,
	// as the inviter's second invited user without a recharge, makes the
	// inviter abusive.
	payment.ComplianceConfirmed, payment.ComplianceTermsVersion = true, operation_setting.CurrentComplianceTermsVersion
	common.PasswordRegisterEnabled, common.EmailVerificationEnabled, common.RegisterIPRateLimitEnabled = true, false, false
	common.QuotaForInviter, common.QuotaForInvitee, common.InviteRechargeThresholdQuota = 1000, 1000, 1
	common.InviteAbuseBanEnabled = true

	signUp := map[string]func(username string, affCode string) string{
		"password": func(username string, affCode string) string {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(registerTestBody(username, "", "", affCode)))
			c.Request.Header.Set("Content-Type", "application/json")
			Register(c)
			return recorder.Body.String()
		},
		"oauth": func(username string, affCode string) string {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth/github", nil)
			flow := &model.AuthFlow{Payload: fmt.Sprintf(`{"affiliate_code":%q}`, affCode)}
			handleOAuthLogin(c, &oauth.GitHubProvider{}, &oauth.OAuthUser{ProviderUserID: username, Username: username}, &oauth.OAuthToken{}, flow)
			return recorder.Body.String()
		},
	}
	for _, method := range []string{"password", "oauth"} {
		for _, state := range []string{"enabled", "disabled", "deleted"} {
			t.Run(method+" sign-up, "+state+" inviter", func(t *testing.T) {
				name := method + "-" + state
				inviter := createInviteAbuseControllerUser(t, db, name, 0)
				invited := createInviteAbuseControllerUser(t, db, name+"-1", inviter.Id)
				switch state {
				case "disabled":
					require.NoError(t, db.Model(&model.User{}).Where("id = ?", inviter.Id).Update("status", common.UserStatusDisabled).Error)
				case "deleted":
					require.NoError(t, db.Delete(&inviter).Error)
				}

				body := signUp[method](name+"-2", inviter.AffCode)
				var newcomer model.User
				require.NoError(t, db.Where("username = ?", name+"-2").Take(&newcomer).Error, body)
				var rewards, bans int64
				require.NoError(t, db.Model(&model.InviteReward{}).Where("invitee_id = ?", newcomer.Id).Count(&rewards).Error)
				require.NoError(t, db.Model(&model.InviteAbuseBan{}).Where("inviter_id = ?", inviter.Id).Count(&bans).Error)
				var stored model.User
				require.NoError(t, db.Unscoped().Select("aff_count").Where("id = ?", inviter.Id).Take(&stored).Error)
				if state == "enabled" {
					assert.Equal(t, inviter.Id, newcomer.InviterId)
					assert.EqualValues(t, 1, rewards)
					assert.Equal(t, 1, stored.AffCount)
					assert.EqualValues(t, 3, bans, "the inviter and both invited users")
					assert.Equal(t, common.UserStatusDisabled, newcomer.Status)
					return
				}
				assert.Contains(t, body, `"success":true`, "the sign-up itself goes on")
				assert.Zero(t, newcomer.InviterId)
				assert.Equal(t, common.UserStatusEnabled, newcomer.Status)
				assert.Zero(t, rewards, "no invite reward")
				assert.Zero(t, stored.AffCount)
				assert.Zero(t, bans, "no invite abuse check")
				assert.Equal(t, common.UserStatusEnabled, inviteAbuseControllerStatus(t, db, invited.Id))
			})
		}
	}
}

// The admin list carries invite_count and invite_abuse_ban only where they
// apply, sorts by invite_count, and GET /api/user/ with inviter_id lists the
// users the inviter invited.
func TestAdminUserListInviteFields(t *testing.T) {
	db := setupInviteAbuseControllerTest(t)
	farmer := createInviteAbuseControllerUser(t, db, "farmer", 0)
	first := createInviteAbuseControllerUser(t, db, "farmer-1", farmer.Id)
	second := createInviteAbuseControllerUser(t, db, "farmer-2", farmer.Id)
	quiet := createInviteAbuseControllerUser(t, db, "quiet", 0)
	require.NoError(t, db.Create(&model.InviteAbuseBan{UserId: farmer.Id, InviterId: farmer.Id, Kind: model.InviteAbuseKindInviter, InviteCount: 2, CreatedAt: 1}).Error)

	type listed struct {
		Id             int     `json:"id"`
		InviteCount    *int64  `json:"invite_count"`
		InviteAbuseBan *string `json:"invite_abuse_ban"`
	}
	list := func(path string) (int, []listed) {
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				Total int      `json:"total"`
				Items []listed `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, common.UnmarshalJsonStr(performInviteAbuseGet(t, path), &response))
		require.True(t, response.Success, path)
		return response.Data.Total, response.Data.Items
	}
	ids := func(items []listed) []int {
		result := make([]int, 0, len(items))
		for _, item := range items {
			result = append(result, item.Id)
		}
		return result
	}

	total, items := list("/api/user/?sort_by=invite_count&sort_order=desc&p=1&page_size=10")
	assert.Equal(t, 4, total)
	assert.Equal(t, []int{farmer.Id, quiet.Id, second.Id, first.Id}, ids(items))
	require.NotNil(t, items[0].InviteCount)
	assert.EqualValues(t, 2, *items[0].InviteCount)
	require.NotNil(t, items[0].InviteAbuseBan)
	assert.Equal(t, model.InviteAbuseKindInviter, *items[0].InviteAbuseBan)
	assert.Nil(t, items[1].InviteCount, "omitted for users who invited nobody")
	assert.Nil(t, items[1].InviteAbuseBan, "omitted without an active ban")

	for _, path := range []string{
		fmt.Sprintf("/api/user/?inviter_id=%d&p=1&page_size=10", farmer.Id),
		fmt.Sprintf("/api/user/search?keyword=&inviter_id=%d&p=1&page_size=10", farmer.Id),
	} {
		total, items = list(path)
		assert.Equal(t, 2, total, path)
		assert.Equal(t, []int{second.Id, first.Id}, ids(items), path)
	}
	total, _ = list("/api/user/?inviter_id=abc&p=1&page_size=10")
	assert.Equal(t, 4, total, "an invalid inviter id filters nothing")

	// Role-10 users carry admin_role, and the admin role filter separates
	// full admins from deputy admins. The Stripe customer id is a payment
	// identifier: a viewer without billing:read gets it blank.
	initManageUserAuthz(t, db)
	createAdmin := func(name string, role int) model.User {
		user := model.User{Username: name, DisplayName: name, Role: role, Status: common.UserStatusEnabled, Group: "default", AffCode: name, AuthVersion: 1, StripeCustomer: "cus_" + name}
		require.NoError(t, db.Create(&user).Error)
		return user
	}
	admin, root := createAdmin("full-admin", common.RoleAdminUser), createAdmin("root-admin", common.RoleRootUser)
	require.NoError(t, db.Model(&quiet).Update("stripe_customer", "cus_quiet").Error)
	type adminView struct {
		Id             int     `json:"id"`
		AdminRole      *string `json:"admin_role"`
		StripeCustomer string  `json:"stripe_customer"`
	}
	view := func(path string, viewer model.User) (int, []adminView) {
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				Total int         `json:"total"`
				Items []adminView `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, common.UnmarshalJsonStr(performAdminUserGet(t, path, viewer), &response))
		require.True(t, response.Success, path)
		return response.Data.Total, response.Data.Items
	}
	viewIds := func(items []adminView) []int {
		result := []int{}
		for _, item := range items {
			result = append(result, item.Id)
		}
		return result
	}
	total, items2 := view("/api/user/?admin_role=deputy&p=1&page_size=20", root)
	assert.Zero(t, total, "no deputy admin yet")
	assert.Empty(t, items2)
	total, items2 = view("/api/user/search?keyword=&admin_role=admin&p=1&page_size=20", root)
	assert.Equal(t, 1, total)
	assert.Equal(t, []int{admin.Id}, viewIds(items2))

	deputy := createAdmin("deputy-admin", common.RoleAdminUser)
	assignTestDeputy(t, db, deputy.Id)
	for _, tc := range []struct {
		path string
		want []int
	}{
		{"/api/user/?admin_role=deputy&p=1&page_size=20", []int{deputy.Id}},
		{"/api/user/search?keyword=&admin_role=deputy&p=1&page_size=20", []int{deputy.Id}},
		{"/api/user/search?keyword=&admin_role=admin&p=1&page_size=20", []int{admin.Id}},
		{"/api/user/search?keyword=&role=1&admin_role=deputy&p=1&page_size=20", []int{deputy.Id}},
		{"/api/user/search?keyword=&role=10&admin_role=bogus&p=1&page_size=20", []int{deputy.Id, admin.Id}},
	} {
		total, items2 = view(tc.path, root)
		assert.Equal(t, len(tc.want), total, tc.path)
		assert.Equal(t, tc.want, viewIds(items2), tc.path)
	}

	_, items2 = view("/api/user/?p=1&page_size=20", root)
	roles := map[int]*string{}
	stripe := map[int]string{}
	for _, item := range items2 {
		roles[item.Id], stripe[item.Id] = item.AdminRole, item.StripeCustomer
	}
	require.NotNil(t, roles[admin.Id])
	assert.Equal(t, "admin", *roles[admin.Id])
	require.NotNil(t, roles[deputy.Id])
	assert.Equal(t, "deputy", *roles[deputy.Id])
	assert.Nil(t, roles[root.Id])
	assert.Nil(t, roles[quiet.Id])
	assert.Equal(t, "cus_quiet", stripe[quiet.Id])
	for _, viewer := range []model.User{admin, root} {
		_, items2 = view("/api/user/search?keyword=quiet&p=1&page_size=20", viewer)
		require.Len(t, items2, 1)
		assert.Equal(t, "cus_quiet", items2[0].StripeCustomer)
	}
	for _, path := range []string{"/api/user/?p=1&page_size=20", "/api/user/search?keyword=&p=1&page_size=20"} {
		_, items2 = view(path, deputy)
		require.NotEmpty(t, items2)
		for _, item := range items2 {
			assert.Empty(t, item.StripeCustomer, path)
		}
	}

	detail := func(id int, viewer model.User) adminView {
		var response struct {
			Success bool      `json:"success"`
			Data    adminView `json:"data"`
		}
		require.NoError(t, common.UnmarshalJsonStr(performAdminUserGet(t, fmt.Sprintf("/api/user/%d", id), viewer), &response))
		require.True(t, response.Success)
		return response.Data
	}
	deputyDetail := detail(deputy.Id, root)
	require.NotNil(t, deputyDetail.AdminRole)
	assert.Equal(t, "deputy", *deputyDetail.AdminRole)
	assert.Equal(t, "cus_deputy-admin", deputyDetail.StripeCustomer)
	assert.Equal(t, "cus_quiet", detail(quiet.Id, admin).StripeCustomer)
	assert.Empty(t, detail(quiet.Id, deputy).StripeCustomer)

	for _, tc := range []struct {
		viewer      model.User
		adminRole   string
		billingRead bool
	}{{deputy, "deputy", false}, {admin, "admin", true}} {
		var response struct {
			Data struct {
				AdminRole   string `json:"admin_role"`
				Permissions struct {
					AdminPermissions authz.PermissionsMap `json:"admin_permissions"`
				} `json:"permissions"`
			} `json:"data"`
		}
		require.NoError(t, common.UnmarshalJsonStr(performAdminUserGet(t, "/api/user/self", tc.viewer), &response))
		assert.Equal(t, tc.adminRole, response.Data.AdminRole)
		assert.Equal(t, tc.billingRead, response.Data.Permissions.AdminPermissions[authz.ResourceBilling][authz.ActionRead])
		assert.Equal(t, tc.billingRead, response.Data.Permissions.AdminPermissions[authz.ResourceRedemption][authz.ActionRead])
		assert.True(t, response.Data.Permissions.AdminPermissions[authz.ResourceChannel][authz.ActionRead])
	}
	self := performAdminUserGet(t, "/api/user/self", quiet)
	assert.NotContains(t, self, "admin_role", "only role-10 accounts carry admin_role")
}

// performAdminUserGet serves the users list, search, detail and self routes
// as viewer, the way AdminAuth/UserAuth would have identified the caller.
func performAdminUserGet(t *testing.T, path string, viewer model.User) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", viewer.Id)
		c.Set("role", viewer.Role)
	})
	router.GET("/api/user/", GetAllUsers)
	router.GET("/api/user/search", SearchUsers)
	router.GET("/api/user/self", GetSelf)
	router.GET("/api/user/:id", GetUser)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	return recorder.Body.String()
}
