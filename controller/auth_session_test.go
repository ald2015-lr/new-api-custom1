package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuthLogoutRejectsRefreshCookieSessionMismatch(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "auth-logout-mismatch-test-secret"
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})

	user := &model.User{
		Username: "logout-mismatch-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	sessionA, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-a")
	require.NoError(t, err)
	sessionB, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-b")
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	c.Request.Header.Set("Authorization", "Bearer "+sessionA.AccessToken)
	c.Request.Header.Set("X-Auth-Session", sessionA.Session.SID)
	c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: sessionB.RefreshToken})

	AuthLogout(c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, "AUTH_SESSION_MISMATCH", response.Code)
	for _, sid := range []string{sessionA.Session.SID, sessionB.Session.SID} {
		stored, err := model.GetUserSessionBySID(sid)
		require.NoError(t, err)
		assert.Equal(t, model.UserSessionStatusActive, stored.Status)
	}
}

func TestWriteAuthSessionErrorMapsSessionGrowthLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "active session limit",
			err:            model.ErrUserSessionLimit,
			expectedStatus: http.StatusConflict,
			expectedCode:   "AUTH_SESSION_LIMIT",
		},
		{
			name:           "issuance limit",
			err:            model.ErrUserSessionIssuanceLimit,
			expectedStatus: http.StatusTooManyRequests,
			expectedCode:   "AUTH_SESSION_ISSUANCE_LIMIT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			writeAuthSessionError(c, test.err)

			assert.Equal(t, test.expectedStatus, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, test.expectedCode, response.Code)
		})
	}
}

func TestSessionLimitDoesNotRecordRejectedLoginAsSuccessful(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.PasskeyCredential{}))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 100
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
	})

	const previousLastLoginAt = int64(123)
	user := &model.User{
		Username: "rejected-login-audit-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, LastLoginAt: previousLastLoginAt,
	}
	require.NoError(t, db.Create(user).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{
		SID: "existing-active-session", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
		Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	setupLogin(user, nil, c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var stored model.User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.Equal(t, previousLastLoginAt, stored.LastLoginAt)
}

// startWithKeptLoginKey does to the secrets what a start without
// SESSION_SECRET does on model.DB: InitEnv leaves one random key, or
// cryptoSecretEnv as CRYPTO_SECRET when it is not empty, then
// model.InitSessionSecret makes the key kept in server_secrets the login
// signing key. It returns that key and restores the secrets after the test.
func startWithKeptLoginKey(t *testing.T, cryptoSecretEnv string) string {
	t.Helper()
	previousSessionSecret, previousCryptoSecret := common.SessionSecret, common.CryptoSecret
	previousSource, previousCryptoFromEnv := common.SessionSecretSource, common.CryptoSecretFromEnv
	t.Cleanup(func() {
		common.SessionSecret, common.CryptoSecret = previousSessionSecret, previousCryptoSecret
		common.SessionSecretSource, common.CryptoSecretFromEnv = previousSource, previousCryptoFromEnv
	})
	common.SessionSecret, common.SessionSecretSource = "random-at-start", common.SessionSecretSourceRandom
	common.CryptoSecret, common.CryptoSecretFromEnv = "random-at-start", false
	if cryptoSecretEnv != "" {
		common.CryptoSecret, common.CryptoSecretFromEnv = cryptoSecretEnv, true
	}
	model.InitSessionSecret()
	var kept model.ServerSecret
	require.NoError(t, model.DB.Where("name = ?", "session_secret").First(&kept).Error)
	require.Equal(t, kept.Value, common.SessionSecret)
	require.Equal(t, common.SessionSecretSourceDatabase, common.SessionSecretSource)
	return kept.Value
}

func TestOptionsNeverReturnTheLoginSigningKey(t *testing.T) {
	previousDB, previousRedis := model.DB, common.RedisEnabled
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{
		model.KeepLoginAcrossRestartOptionKey: "true",
		// A stray stored value must never be echoed.
		model.KeepLoginAcrossRestartSourceOptionKey: "x",
	}
	common.OptionMapRWMutex.Unlock()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.ServerSecret{}, &model.SystemTask{}))
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() {
		model.DB, common.RedisEnabled = previousDB, previousRedis
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
		_ = sqlDB.Close()
	})
	kept := startWithKeptLoginKey(t, "")

	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(c)
	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	assert.Contains(t, body, `{"key":"KeepLoginAcrossRestartEnabled","value":"true"}`)
	assert.Contains(t, body, `{"key":"KeepLoginAcrossRestartSource","value":"database"}`)
	assert.Equal(t, 1, strings.Count(body, `"key":"KeepLoginAcrossRestartSource"`))
	for i := range len(kept) - 15 {
		require.NotContains(t, body, kept[i:i+16], "no 16-character piece of the login signing key may be returned")
	}

	put := func(request string) string {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(request))
		UpdateOption(c)
		return response.Body.String()
	}
	readOnly := put(`{"key":"KeepLoginAcrossRestartSource","value":"env"}`)
	assert.Contains(t, readOnly, `"success":false`)
	assert.Contains(t, readOnly, `"message_key":"The source of the login signing key is read-only"`)
	invalid := put(`{"key":"KeepLoginAcrossRestartEnabled","value":"maybe"}`)
	assert.Contains(t, invalid, `"success":false`)
	assert.Contains(t, invalid, `"message_key":"Value must be true or false"`)
	assert.Contains(t, put(`{"key":"KeepLoginAcrossRestartEnabled","value":false}`), `"success":true`)

	var saved []model.Option
	require.NoError(t, db.Order("key").Find(&saved).Error)
	assert.Equal(t, []model.Option{{Key: model.KeepLoginAcrossRestartOptionKey, Value: "false"}}, saved)
	assert.Equal(t, kept, common.SessionSecret, "saving the switch changes nothing until the next restart")
	assert.Equal(t, common.SessionSecretSourceDatabase, common.SessionSecretSource)
	var keptRows int64
	require.NoError(t, db.Model(&model.ServerSecret{}).Count(&keptRows).Error)
	assert.Equal(t, int64(1), keptRows, "the next start deletes the key, not the save")
}
