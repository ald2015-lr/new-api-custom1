package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	authGuardTestIP        = "203.0.113.7"
	authGuardTestUserAgent = "auth-guard-test-agent"
	authGuardTestPassword  = "correct-password"
)

// authGuardTest is the fixture of the password reset and login protection
// tests: the database TEST_MANAGE_USER_DIALECT selects, every protection on
// with its default numbers, Turnstile off, and the in-memory limiter with a
// clock the test moves through now.
type authGuardTest struct {
	db     *gorm.DB
	router *gin.Engine
	now    *time.Time
}

func setupAuthGuardTest(t *testing.T) *authGuardTest {
	t.Helper()
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TwoFABackupCode{}, &model.UserOAuthBinding{}, &model.Option{}))
	previousBools := []*bool{&common.PasswordResetCaptchaEnabled, &common.PasswordResetEmailLimitEnabled, &common.LoginCaptchaEnabled,
		&common.LoginLockoutEnabled, &common.TurnstileCheckEnabled, &common.PasswordLoginEnabled, &common.PasswordLoginEncryptionEnabled}
	previousBoolValues := make([]bool, len(previousBools))
	for i, value := range previousBools {
		previousBoolValues[i] = *value
	}
	previousInts := []*int{&common.PasswordResetEmailCooldownSeconds, &common.PasswordResetEmailDailyMax, &common.LoginLockoutMaxFailures, &common.LoginLockoutMinutes}
	previousIntValues := make([]int, len(previousInts))
	for i, value := range previousInts {
		previousIntValues[i] = *value
	}
	common.PasswordResetCaptchaEnabled, common.PasswordResetEmailLimitEnabled = true, true
	common.PasswordResetEmailCooldownSeconds, common.PasswordResetEmailDailyMax = 120, 5
	common.LoginCaptchaEnabled, common.LoginLockoutEnabled = true, true
	common.LoginLockoutMaxFailures, common.LoginLockoutMinutes = 10, 15
	common.TurnstileCheckEnabled, common.PasswordLoginEnabled, common.PasswordLoginEncryptionEnabled = false, true, false
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	restoreGuard := middleware.UseAuthGuardForTest(func() time.Time { return now })
	t.Cleanup(func() {
		restoreGuard()
		for i, value := range previousBools {
			*value = previousBoolValues[i]
		}
		for i, value := range previousInts {
			*value = previousIntValues[i]
		}
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/reset_password", SendPasswordResetEmail)
	router.POST("/api/user/reset", ResetPassword)
	router.POST("/api/user/login", Login)
	router.POST("/api/user/login/2fa", Verify2FALogin)
	router.POST("/api/user/login/verify", VerifyLogin)
	return &authGuardTest{db: db, router: router, now: &now}
}

func (env *authGuardTest) advance(d time.Duration) {
	*env.now = env.now.Add(d)
}

func (env *authGuardTest) request(method string, target string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", authGuardTestUserAgent)
	request.RemoteAddr = authGuardTestIP + ":4321"
	env.router.ServeHTTP(recorder, request)
	return recorder
}

// solvedCaptcha returns the query of a captcha with a known, correct answer.
func solvedCaptcha() string {
	return captchaQuery(service.IssueCaptchaForTest(5), 5)
}

func (env *authGuardTest) requestReset(email string, captcha string) *httptest.ResponseRecorder {
	target := "/api/reset_password?email=" + url.QueryEscape(email)
	if captcha != "" {
		target += "&" + captcha
	}
	return env.request(http.MethodGet, target, "")
}

func createAuthGuardUser(t *testing.T, db *gorm.DB, username string, email string, role int) *model.User {
	t.Helper()
	hash, err := common.Password2Hash(authGuardTestPassword)
	require.NoError(t, err)
	user := &model.User{Username: username, Password: hash, Email: email, Role: role, Status: common.UserStatusEnabled, Group: "default", AffCode: username, AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	return user
}

// authGuardAudit is an audit row with its params decoded.
type authGuardAudit struct {
	model.AuditLog
	Params   map[string]any
	RootInfo map[string]any
}

func authGuardAudits(t *testing.T, action string) []authGuardAudit {
	t.Helper()
	var rows []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", action).Order("id asc").Find(&rows).Error)
	audits := make([]authGuardAudit, 0, len(rows))
	for _, row := range rows {
		audit := authGuardAudit{AuditLog: row}
		require.NotNil(t, row.Other.Op)
		encoded, err := common.Marshal(row.Other.Op.Params)
		require.NoError(t, err)
		require.NoError(t, common.Unmarshal(encoded, &audit.Params))
		if row.Other.RootInfo != nil {
			encoded, err = common.Marshal(row.Other.RootInfo)
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(encoded, &audit.RootInfo))
		}
		audits = append(audits, audit)
	}
	return audits
}

func lastAuthGuardAudit(t *testing.T, action string) authGuardAudit {
	t.Helper()
	audits := authGuardAudits(t, action)
	require.NotEmpty(t, audits, action)
	return audits[len(audits)-1]
}

func mailCount(mailbox *securityMailbox, receiver string) int {
	mailbox.mutex.Lock()
	defer mailbox.mutex.Unlock()
	return len(mailbox.mail[receiver])
}

// resetMailToken reads the token of the last reset link mailed to receiver.
func resetMailToken(t *testing.T, mailbox *securityMailbox, receiver string) string {
	t.Helper()
	mailbox.mutex.Lock()
	defer mailbox.mutex.Unlock()
	messages := mailbox.mail[receiver]
	require.NotEmpty(t, messages, "no reset email delivered to %s", receiver)
	match := regexp.MustCompile(`token=([0-9a-zA-Z-]+)`).FindStringSubmatch(messages[len(messages)-1])
	require.Len(t, match, 2)
	return match[1]
}

const (
	resetCooldownKey = "A password reset email was requested for this address recently. Please wait {{seconds}} seconds and try again. The email can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working."
	resetDailyKey    = "Too many password reset emails were requested for this address in the last 24 hours. Please try again later or contact the administrator. Emails already sent can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working."
)

func resetCooldownJSON(seconds int) string {
	message := strings.ReplaceAll(resetCooldownKey, "{{seconds}}", fmt.Sprint(seconds))
	return fmt.Sprintf(`{"success":false,"code":"PASSWORD_RESET_EMAIL_COOLDOWN","wait_seconds":%d,"message":%q,"message_key":%q,"message_params":{"seconds":%d}}`, seconds, message, resetCooldownKey, seconds)
}

func resetDailyJSON(seconds int) string {
	return fmt.Sprintf(`{"success":false,"code":"PASSWORD_RESET_EMAIL_DAILY_LIMIT","wait_seconds":%d,"message":%q,"message_key":%q}`, seconds, resetDailyKey, resetDailyKey)
}

// The forgot password request checks the captcha, the address and the
// per-email limit in that order, answers the same for registered and
// unregistered addresses, and writes one audit row per request.
func TestPasswordResetRequestOrderOutcomesAndAudit(t *testing.T) {
	env := setupAuthGuardTest(t)
	mailbox := newSecurityMailbox(t)
	bob := createAuthGuardUser(t, env.db, "reset-bob", "bob@example.com", common.RoleCommonUser)

	// No captcha, then a wrong one: refused, audited with the account, not counted.
	response := env.requestReset("bob@example.com", "")
	assert.JSONEq(t, `{"success":false,"code":"CAPTCHA_REQUIRED","message":"Please complete the captcha.","message_key":"Please complete the captcha."}`, response.Body.String())
	row := lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "bob@example.com", "outcome": "captcha_failed", "code": "CAPTCHA_REQUIRED"}, row.Params)
	assert.Equal(t, bob.Id, row.UserId)
	assert.False(t, row.Success)
	response = env.requestReset("bob@example.com", captchaQuery(service.IssueCaptchaForTest(5), 6))
	assert.JSONEq(t, `{"success":false,"code":"CAPTCHA_INVALID","message":"Captcha is incorrect or has expired. Please try a new one.","message_key":"Captcha is incorrect or has expired. Please try a new one."}`, response.Body.String())
	assert.Equal(t, "CAPTCHA_INVALID", lastAuthGuardAudit(t, "user.password_reset_request").Params["code"])

	// An invalid address: unchanged answer, the typed value masked for all but root.
	response = env.requestReset("not-an-address", solvedCaptcha())
	assert.JSONEq(t, `{"success":false,"message":"Invalid parameters","message_key":"Invalid parameters"}`, response.Body.String())
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "no…", "outcome": "invalid_email", "identifier_length": float64(14)}, row.Params)
	assert.Equal(t, map[string]any{"email": "not-an-address"}, row.RootInfo)
	assert.Zero(t, row.UserId)
	assert.Equal(t, "Password reset email request for no…: invalid_email", row.Content)

	// A registered address: one mail, a "sent" row owned by the account.
	registered := env.requestReset("Bob@Example.com ", solvedCaptcha())
	assert.JSONEq(t, `{"success":true,"message":""}`, registered.Body.String())
	assert.Equal(t, 1, mailCount(mailbox, "bob@example.com"))
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "bob@example.com", "outcome": "sent"}, row.Params)
	assert.Equal(t, bob.Id, row.UserId)
	assert.Equal(t, bob.Username, row.Username)
	assert.Equal(t, common.RoleCommonUser, row.ActorRole)
	assert.Equal(t, model.AuditCategoryLogin, row.Category)
	assert.Equal(t, authGuardTestIP, row.Ip)
	assert.Equal(t, authGuardTestUserAgent, row.UserAgent)
	assert.Equal(t, "/api/reset_password", row.Route)
	assert.True(t, row.Success)
	assert.Equal(t, "Password reset email request for bob@example.com: sent", row.Content)

	// Within the cooldown: the exact refusal, no mail.
	env.advance(10 * time.Second)
	response = env.requestReset("bob@example.com", solvedCaptcha())
	assert.JSONEq(t, resetCooldownJSON(110), response.Body.String())
	assert.Equal(t, 1, mailCount(mailbox, "bob@example.com"))
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "bob@example.com", "outcome": "cooldown", "code": "PASSWORD_RESET_EMAIL_COOLDOWN", "wait_seconds": float64(110)}, row.Params)
	assert.Equal(t, bob.Id, row.UserId)
	assert.False(t, row.Success)

	// An unregistered address: the same answer, no mail, and the same limit.
	unregistered := env.requestReset("ghost@example.com", solvedCaptcha())
	assert.Equal(t, registered.Body.String(), unregistered.Body.String())
	assert.Equal(t, registered.Code, unregistered.Code)
	assert.Zero(t, mailCount(mailbox, "ghost@example.com"))
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "ghost@example.com", "outcome": "not_registered"}, row.Params)
	assert.Zero(t, row.UserId)
	assert.Equal(t, common.RoleCommonUser, row.ActorRole)
	assert.False(t, row.Success)
	assert.JSONEq(t, resetCooldownJSON(120), env.requestReset("ghost@example.com", solvedCaptcha()).Body.String())

	// Five admitted requests in 24 hours; refusals in between do not use them up.
	for i := 1; i < 5; i++ {
		env.advance(110 * time.Second)
		require.JSONEq(t, `{"success":true,"message":""}`, env.requestReset("bob@example.com", solvedCaptcha()).Body.String(), "request %d", i+1)
		env.advance(10 * time.Second)
		if i < 4 {
			assert.JSONEq(t, resetCooldownJSON(110), env.requestReset("bob@example.com", solvedCaptcha()).Body.String())
		}
	}
	assert.Equal(t, 5, mailCount(mailbox, "bob@example.com"))
	response = env.requestReset("bob@example.com", solvedCaptcha())
	elapsed := int(env.now.Sub(time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)) / time.Second)
	assert.JSONEq(t, resetDailyJSON(86400-elapsed), response.Body.String())
	assert.Equal(t, "daily_limit", lastAuthGuardAudit(t, "user.password_reset_request").Params["outcome"])
	assert.Equal(t, 5, mailCount(mailbox, "bob@example.com"))

	// SMTP down: the same success answer, a send_failed row, and the request counts.
	createAuthGuardUser(t, env.db, "reset-smtp", "smtp@example.com", common.RoleCommonUser)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	previousPort := common.SMTPPort
	common.SMTPPort = closed.Addr().(*net.TCPAddr).Port
	require.NoError(t, closed.Close())
	response = env.requestReset("smtp@example.com", solvedCaptcha())
	common.SMTPPort = previousPort
	assert.JSONEq(t, `{"success":true,"message":""}`, response.Body.String())
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "smtp@example.com", "outcome": "send_failed", "detail": "smtp_error"}, row.Params)
	assert.NotZero(t, row.UserId)
	assert.Equal(t, "PASSWORD_RESET_EMAIL_COOLDOWN", gjsonCode(t, env.requestReset("smtp@example.com", solvedCaptcha())))

	// One address on two accounts: no mail, send_failed without an account.
	createAuthGuardUser(t, env.db, "reset-dup-1", "dup@example.com", common.RoleCommonUser)
	createAuthGuardUser(t, env.db, "reset-dup-2", "DUP@example.com", common.RoleCommonUser)
	assert.JSONEq(t, `{"success":true,"message":""}`, env.requestReset("dup@example.com", solvedCaptcha()).Body.String())
	assert.Zero(t, mailCount(mailbox, "dup@example.com"))
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "dup@example.com", "outcome": "send_failed", "detail": "email_ambiguous"}, row.Params)
	assert.Zero(t, row.UserId)

	// The captcha off: no captcha needed.
	common.PasswordResetCaptchaEnabled = false
	assert.JSONEq(t, `{"success":true,"message":""}`, env.requestReset("other@example.com", "").Body.String())
}

func gjsonCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
	return body.Code
}

// Addresses a database collation matches to an account but that differ from
// its stored address (accents, full-width letters) are not the account's: no
// token is issued for them, they have their own limit, and a link for them
// never resets the account. Meaningful on MySQL, whose default collations
// match such variants.
func TestPasswordResetIgnoresCollationVariants(t *testing.T) {
	env := setupAuthGuardTest(t)
	mailbox := newSecurityMailbox(t)
	common.PasswordResetCaptchaEnabled = false
	bob := createAuthGuardUser(t, env.db, "variant-bob", "bob@example.com", common.RoleCommonUser)

	for _, variant := range []string{"bob@exämple.com", "ｂob@example.com"} {
		response := env.requestReset(variant, "")
		assert.JSONEq(t, `{"success":true,"message":""}`, response.Body.String())
		assert.Zero(t, mailCount(mailbox, variant))
		assert.Zero(t, mailCount(mailbox, "bob@example.com"))
		row := lastAuthGuardAudit(t, "user.password_reset_request")
		assert.Equal(t, "not_registered", row.Params["outcome"], variant)
		assert.Zero(t, row.UserId, variant)

		// A token for the variant (as an older version mailed) resets nothing.
		common.RegisterVerificationCodeWithKey(model.NormalizeEmail(variant), "variant-token", common.PasswordResetPurpose)
		body, err := common.Marshal(map[string]string{"email": variant, "token": "variant-token"})
		require.NoError(t, err)
		response = env.request(http.MethodPost, "/api/user/reset", string(body))
		assert.JSONEq(t, `{"success":false,"message":"Password reset link is invalid or has expired","message_key":"Password reset link is invalid or has expired"}`, response.Body.String())
		failed := lastAuthGuardAudit(t, "user.password_reset_failed")
		assert.Equal(t, "invalid_link", failed.Params["reason"])
		assert.Zero(t, failed.UserId)
	}
	var stored model.User
	require.NoError(t, env.db.First(&stored, bob.Id).Error)
	assert.Equal(t, bob.Password, stored.Password)
	// The variants did not use up the limit of the real address.
	assert.JSONEq(t, `{"success":true,"message":""}`, env.requestReset("bob@example.com", "").Body.String())
	assert.Equal(t, 1, mailCount(mailbox, "bob@example.com"))
}

// Completing a reset through the email link is audited and ends the login
// lock of the account; failed completions are audited with the reason. The
// new password never enters a row.
func TestPasswordResetCompletionAuditsAndEndsTheLock(t *testing.T) {
	env := setupAuthGuardTest(t)
	mailbox := newSecurityMailbox(t)
	common.PasswordResetCaptchaEnabled = false
	common.LoginLockoutMaxFailures = 3
	carol := createAuthGuardUser(t, env.db, "reset-carol", "carol@example.com", common.RoleCommonUser)
	ctx := context.Background()
	for range 3 {
		attempt, wait := middleware.BeginLoginAttempt(ctx, middleware.LoginLockSubjects(carol.Id, carol.Username))
		require.NotNil(t, attempt)
		require.Zero(t, wait)
		attempt.Fail(ctx)
	}
	_, wait := middleware.BeginLoginAttempt(ctx, middleware.LoginLockSubjects(carol.Id, ""))
	require.Positive(t, wait, "the account is locked")

	// Failed completions.
	response := env.request(http.MethodPost, "/api/user/reset", `{"email":"carol@example.com","token":"wrong-token"}`)
	assert.JSONEq(t, `{"success":false,"message":"Password reset link is invalid or has expired","message_key":"Password reset link is invalid or has expired"}`, response.Body.String())
	row := lastAuthGuardAudit(t, "user.password_reset_failed")
	assert.Equal(t, map[string]any{"email": "carol@example.com", "reason": "invalid_link"}, row.Params)
	assert.Equal(t, carol.Id, row.UserId)
	assert.False(t, row.Success)
	assert.Equal(t, "Password reset through the email link failed for carol@example.com: invalid_link", row.Content)
	env.request(http.MethodPost, "/api/user/reset", `{}`)
	assert.Equal(t, map[string]any{"email": "", "reason": "invalid_request"}, lastAuthGuardAudit(t, "user.password_reset_failed").Params)
	env.request(http.MethodPost, "/api/user/reset", `{"email":`)
	assert.Equal(t, "invalid_request", lastAuthGuardAudit(t, "user.password_reset_failed").Params["reason"])
	assert.Len(t, authGuardAudits(t, "user.password_reset_failed"), 3)
	_, wait = middleware.BeginLoginAttempt(ctx, middleware.LoginLockSubjects(carol.Id, ""))
	require.Positive(t, wait, "a failed completion does not end the lock")

	// The email link: success, a row, and the lock is gone.
	require.JSONEq(t, `{"success":true,"message":""}`, env.requestReset("carol@example.com", "").Body.String())
	token := resetMailToken(t, mailbox, "carol@example.com")
	response = env.request(http.MethodPost, "/api/user/reset", fmt.Sprintf(`{"email":"carol@example.com","token":%q}`, token))
	var reset struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &reset))
	require.True(t, reset.Success, response.Body.String())
	row = lastAuthGuardAudit(t, "user.password_reset")
	assert.Equal(t, map[string]any{"email": "carol@example.com"}, row.Params)
	assert.Equal(t, carol.Id, row.UserId)
	assert.True(t, row.Success)
	assert.Equal(t, model.AuditCategoryLogin, row.Category)
	assert.Equal(t, "Reset the password through the email link for carol@example.com", row.Content)
	for _, subjects := range [][]string{middleware.LoginLockSubjects(carol.Id, ""), middleware.LoginLockSubjects(0, carol.Username), middleware.LoginLockSubjects(0, carol.Email)} {
		attempt, wait := middleware.BeginLoginAttempt(ctx, subjects)
		assert.NotNil(t, attempt)
		assert.Zero(t, wait)
		attempt.Release(ctx)
	}

	var rows []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&rows).Error)
	for _, row := range rows {
		encoded, err := common.Marshal(row)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), reset.Data, "the new password never enters an audit row")
		assert.NotContains(t, string(encoded), token, "the reset token never enters an audit row")
	}
}
