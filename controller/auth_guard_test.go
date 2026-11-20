package controller

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
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
	ip     string // client IP of the requests
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
	viewer := func(c *gin.Context) {
		id, _ := strconv.Atoi(c.GetHeader("X-Test-User"))
		role, _ := strconv.Atoi(c.GetHeader("X-Test-Role"))
		c.Set("id", id)
		c.Set("role", role)
	}
	router.GET("/api/audit", viewer, GetAuditLogs)
	router.GET("/api/audit/self", viewer, GetAuditLogs)
	return &authGuardTest{db: db, router: router, now: &now, ip: authGuardTestIP}
}

func (env *authGuardTest) advance(d time.Duration) {
	*env.now = env.now.Add(d)
}

func (env *authGuardTest) request(method string, target string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", authGuardTestUserAgent)
	request.RemoteAddr = env.ip + ":4321"
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

	// An invalid address: unchanged answer, the typed value not stored.
	response = env.requestReset("not-an-address", solvedCaptcha())
	assert.JSONEq(t, `{"success":false,"message":"Invalid parameters","message_key":"Invalid parameters"}`, response.Body.String())
	row = lastAuthGuardAudit(t, "user.password_reset_request")
	assert.Equal(t, map[string]any{"email": "…", "outcome": "invalid_email", "identifier_length": float64(14),
		"identifier_fingerprint": auditIdentifierFingerprint("not-an-address")}, row.Params)
	assert.Nil(t, row.RootInfo)
	assert.Zero(t, row.UserId)
	assert.Equal(t, "Password reset email request for …: invalid_email", row.Content)

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
	require.Equal(t, "LOGIN_LOCKED", gjsonCode(t, env.login("carol@example.com", authGuardTestPassword, solvedCaptcha())))

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
	response = env.login("reset-carol", reset.Data, solvedCaptcha())
	assert.True(t, loginSucceeded(t, response), "the new password signs in at once: %s", response.Body.String())

	var rows []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&rows).Error)
	for _, row := range rows {
		encoded, err := common.Marshal(row)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), reset.Data, "the new password never enters an audit row")
		assert.NotContains(t, string(encoded), token, "the reset token never enters an audit row")
	}
}

func (env *authGuardTest) login(identifier string, password string, captcha string) *httptest.ResponseRecorder {
	body, _ := common.Marshal(map[string]string{"username": identifier, "password": password})
	target := "/api/user/login"
	if captcha != "" {
		target += "?" + captcha
	}
	return env.request(http.MethodPost, target, string(body))
}

func loginSucceeded(t *testing.T, recorder *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken         string `json:"access_token"`
			RequireVerification bool   `json:"require_verification"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
	return body.Success && (body.Data.AccessToken != "" || body.Data.RequireVerification)
}

const (
	loginGenericJSON = `{"success":false,"message":"Username or password is incorrect, or user has been banned","message_key":"Username or password is incorrect, or user has been banned"}`
	loginLockedKey   = "Too many failed sign-in attempts. Please try again in {{minutes}} minutes or reset your password."
)

func loginLockedJSON(seconds int64) string {
	minutes := max((seconds+59)/60, 1)
	message := strings.ReplaceAll(loginLockedKey, "{{minutes}}", fmt.Sprint(minutes))
	return fmt.Sprintf(`{"success":false,"code":"LOGIN_LOCKED","wait_seconds":%d,"message":%q,"message_key":%q,"message_params":{"minutes":%d}}`, seconds, message, loginLockedKey, minutes)
}

// encryptLoginPassword encrypts a password the way the login page does
// while password login encryption is on.
func encryptLoginPassword(t *testing.T, password string) (string, string) {
	t.Helper()
	keyID, publicPEM := common.PasswordEncryptionPublicKey()
	if keyID == "" {
		privatePEM, err := common.GeneratePasswordEncryptionPrivateKey()
		require.NoError(t, err)
		require.NoError(t, common.LoadPasswordEncryptionPrivateKey(privatePEM))
		keyID, publicPEM = common.PasswordEncryptionPublicKey()
	}
	block, _ := pem.Decode([]byte(publicPEM))
	require.NotNil(t, block)
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	publicKey, ok := parsed.(*rsa.PublicKey)
	require.True(t, ok)
	key, nonce := make([]byte, 32), make([]byte, 12)
	_, err = rand.Read(key)
	require.NoError(t, err)
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, key, []byte("password-v2"))
	require.NoError(t, err)
	aesBlock, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(aesBlock)
	require.NoError(t, err)
	ciphertext := gcm.Seal(nil, nonce, []byte(password), []byte("password-v2:"+keyID))
	return strings.Join([]string{"v2", base64.StdEncoding.EncodeToString(wrappedKey), base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ciphertext)}, "."), keyID
}

// The login captcha is checked before the lockout and the password: a
// missing captcha is refused and audited with the account, but never counts
// as a failed password, also with password encryption.
func TestLoginCaptchaComesBeforeLockAndPassword(t *testing.T) {
	env := setupAuthGuardTest(t)
	dave := createAuthGuardUser(t, env.db, "captcha-dave", "dave@example.com", common.RoleCommonUser)

	for range 25 {
		require.JSONEq(t, `{"success":false,"code":"CAPTCHA_REQUIRED","message":"Please complete the captcha.","message_key":"Please complete the captcha."}`,
			env.login("captcha-dave", authGuardTestPassword, "").Body.String())
	}
	rows := authGuardAudits(t, "user.login_failed")
	require.Len(t, rows, 25)
	assert.Equal(t, map[string]any{"method": "password", "identifier": "captcha-dave", "reason": "captcha_failed", "code": "CAPTCHA_REQUIRED"}, rows[0].Params)
	assert.Equal(t, dave.Id, rows[0].UserId, "the row names the account, for attribution only")
	assert.Equal(t, "password", rows[0].Other.LoginMethod)
	assert.Equal(t, "Failed login via password as captcha-dave: captcha_failed", rows[0].Content)
	assert.JSONEq(t, `{"success":false,"code":"CAPTCHA_INVALID","message":"Captcha is incorrect or has expired. Please try a new one.","message_key":"Captcha is incorrect or has expired. Please try a new one."}`,
		env.login("captcha-dave", "wrong-password", captchaQuery(service.IssueCaptchaForTest(5), 4)).Body.String())
	assert.Equal(t, "CAPTCHA_INVALID", lastAuthGuardAudit(t, "user.login_failed").Params["code"])

	// The encrypted body needs the captcha too; with it the login succeeds.
	common.PasswordLoginEncryptionEnabled = true
	encrypted, keyID := encryptLoginPassword(t, authGuardTestPassword)
	body, err := common.Marshal(LoginRequest{Username: "captcha-dave", PasswordEncrypted: encrypted, EncryptionKeyID: keyID})
	require.NoError(t, err)
	assert.Equal(t, "CAPTCHA_REQUIRED", gjsonCode(t, env.request(http.MethodPost, "/api/user/login", string(body))))
	response := env.request(http.MethodPost, "/api/user/login?"+solvedCaptcha(), string(body))
	require.True(t, loginSucceeded(t, response), response.Body.String())
	common.PasswordLoginEncryptionEnabled = false

	response = env.login(" captcha-dave ", authGuardTestPassword, solvedCaptcha())
	require.True(t, loginSucceeded(t, response), response.Body.String())
	login := lastAuthGuardAudit(t, "login")
	assert.Equal(t, dave.Id, login.UserId)
	assert.True(t, login.Success)
	assert.Len(t, authGuardAudits(t, "user.login_failed"), 27, "successful logins write no failure row")
}

// Wrong passwords count per account (username and email share one counter)
// and per typed name, so unknown names are locked the same way. The lock
// answer is the same for both, and holds even for the right password.
func TestLoginLockoutSharesOneCounterAndIsEnumerationSafe(t *testing.T) {
	env := setupAuthGuardTest(t)
	common.LoginCaptchaEnabled = false
	common.LoginLockoutMaxFailures = 3
	erin := createAuthGuardUser(t, env.db, "lock-erin", "erin@example.com", common.RoleCommonUser)

	assert.JSONEq(t, loginGenericJSON, env.login("lock-erin", "wrong-1", "").Body.String())
	assert.JSONEq(t, loginGenericJSON, env.login("lock-erin", "wrong-2", "").Body.String())
	known := env.login("erin@example.com", "wrong-3", "")
	assert.JSONEq(t, loginLockedJSON(900), known.Body.String(), "the failure that reaches the maximum locks at once")
	row := lastAuthGuardAudit(t, "user.login_failed")
	assert.Equal(t, map[string]any{"method": "password", "identifier": "erin@example.com", "reason": "invalid_credentials", "wait_seconds": float64(900), "lock_started": true}, row.Params)
	assert.Equal(t, erin.Id, row.UserId)
	env.advance(30 * time.Second)
	assert.JSONEq(t, loginLockedJSON(870), env.login("lock-erin", authGuardTestPassword, "").Body.String(), "locked even with the right password")
	row = lastAuthGuardAudit(t, "user.login_failed")
	assert.Equal(t, map[string]any{"method": "password", "identifier": "lock-erin", "reason": "locked", "wait_seconds": float64(870)}, row.Params)

	// An unknown name: counted and locked the same way, with the same answers.
	env.advance(-30 * time.Second)
	assert.JSONEq(t, loginGenericJSON, env.login("ghost", "wrong-1", "").Body.String())
	assert.JSONEq(t, loginGenericJSON, env.login("GHOST", "wrong-2", "").Body.String())
	unknown := env.login("ghost", "wrong-3", "")
	assert.Equal(t, known.Body.String(), unknown.Body.String())
	row = lastAuthGuardAudit(t, "user.login_failed")
	assert.Zero(t, row.UserId)
	assert.Equal(t, "…", row.Params["identifier"])
	env.advance(30 * time.Second)
	assert.JSONEq(t, loginLockedJSON(870), env.login("ghost", "wrong-4", "").Body.String())

	// A disabled account with the right password: the generic answer,
	// audited, and counted like a wrong password, so the moment the lock
	// starts does not reveal whether the password is right.
	frank := createAuthGuardUser(t, env.db, "lock-frank", "frank@example.com", common.RoleCommonUser)
	require.NoError(t, env.db.Model(frank).Update("status", common.UserStatusDisabled).Error)
	for range 2 {
		assert.JSONEq(t, loginGenericJSON, env.login("lock-frank", authGuardTestPassword, "").Body.String())
	}
	row = lastAuthGuardAudit(t, "user.login_failed")
	assert.Equal(t, map[string]any{"method": "password", "identifier": "lock-frank", "reason": "disabled"}, row.Params)
	assert.Equal(t, frank.Id, row.UserId)
	assert.JSONEq(t, loginLockedJSON(900), env.login("lock-frank", authGuardTestPassword, "").Body.String())
	row = lastAuthGuardAudit(t, "user.login_failed")
	assert.Equal(t, map[string]any{"method": "password", "identifier": "lock-frank", "reason": "disabled", "wait_seconds": float64(900), "lock_started": true}, row.Params)
	assert.JSONEq(t, loginLockedJSON(900), env.login("lock-frank", "wrong", "").Body.String())
	assert.Equal(t, "locked", lastAuthGuardAudit(t, "user.login_failed").Params["reason"])

	// A completed login clears the count.
	createAuthGuardUser(t, env.db, "lock-gina", "gina@example.com", common.RoleCommonUser)
	for range 2 {
		env.login("lock-gina", "wrong", "")
	}
	require.True(t, loginSucceeded(t, env.login("lock-gina", authGuardTestPassword, "")))
	for range 2 {
		assert.JSONEq(t, loginGenericJSON, env.login("lock-gina", "wrong", "").Body.String())
	}
	require.True(t, loginSucceeded(t, env.login("gina@example.com", authGuardTestPassword, "")))

	// The right password with two-factor verification pending is not a
	// failure, and does not clear the count either.
	hank := createAuthGuardUser(t, env.db, "lock-hank", "hank@example.com", common.RoleCommonUser)
	require.NoError(t, env.db.Create(&model.TwoFA{UserId: hank.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
	env.login("lock-hank", "wrong", "")
	for range 3 {
		response := env.login("lock-hank", authGuardTestPassword, "")
		require.True(t, loginSucceeded(t, response), response.Body.String())
		assert.Contains(t, response.Body.String(), `"require_verification":true`)
	}
	env.login("lock-hank", "wrong", "")
	assert.Equal(t, "LOGIN_LOCKED", gjsonCode(t, env.login("lock-hank", "wrong", "")), "two failures before and one after the pending logins lock")

	// An unknown name is not stored; its length and fingerprint are of the
	// whole trimmed value.
	long := strings.Repeat("é", 70)
	env.login("  "+long+"  ", "wrong", "")
	row = lastAuthGuardAudit(t, "user.login_failed")
	assert.Equal(t, "…", row.Params["identifier"])
	assert.Equal(t, float64(70), row.Params["identifier_length"])
	assert.Equal(t, auditIdentifierFingerprint(long), row.Params["identifier_fingerprint"])
	assert.Nil(t, row.RootInfo)
	// An email address is stored trimmed and cut to 64 runes.
	address := strings.Repeat("a", 60) + "@" + strings.Repeat("b", 20) + ".com"
	env.login(" "+address+" ", "wrong", "")
	row = lastAuthGuardAudit(t, "user.login_failed")
	assert.Equal(t, address[:64], row.Params["identifier"])
	assert.NotContains(t, row.Params, "identifier_fingerprint")
	env.login("  lock-gina ", "wrong", "")
	assert.Equal(t, "lock-gina", lastAuthGuardAudit(t, "user.login_failed").Params["identifier"])
}

// A database error on the account lookup is logged with the fingerprint of
// the typed name, never the name, which may be a password.
func TestLoginLookupErrorLogsNoTypedName(t *testing.T) {
	env := setupAuthGuardTest(t)
	common.LoginCaptchaEnabled = false
	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previous := gin.DefaultWriter
	gin.DefaultWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = previous
		common.LogWriterMu.Unlock()
	})
	require.NoError(t, env.db.Migrator().DropTable(&model.User{}))

	response := env.login(" S3cret!pass ", "wrong", "")
	assert.Contains(t, response.Body.String(), "Database error, please contact the administrator")
	assert.Contains(t, output.String(), "Login database error (identifier fingerprint "+auditIdentifierFingerprint("S3cret!pass")+"): ")
	assert.NotContains(t, output.String(), "S3cret!pass")
}

// Concurrent wrong passwords for one account: at most the maximum of them
// reach the password check; the rest are refused as locked.
func TestLoginLockoutHoldsUnderConcurrentRequests(t *testing.T) {
	env := setupAuthGuardTest(t)
	common.LoginCaptchaEnabled = false
	createAuthGuardUser(t, env.db, "burst-ivan", "ivan@example.com", common.RoleCommonUser)
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() { env.login("burst-ivan", "wrong-password", "") })
	}
	wg.Wait()
	reasons := map[any]int{}
	for _, row := range authGuardAudits(t, "user.login_failed") {
		reasons[row.Params["reason"]]++
	}
	assert.Equal(t, map[any]int{"invalid_credentials": 10, "locked": 30}, reasons)
	assert.Equal(t, "LOGIN_LOCKED", gjsonCode(t, env.login("burst-ivan", authGuardTestPassword, "")))
}

func (env *authGuardTest) loginChallenge(t *testing.T, identifier string) string {
	t.Helper()
	response := env.login(identifier, authGuardTestPassword, "")
	var body struct {
		Success bool                   `json:"success"`
		Data    service.LoginChallenge `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success && body.Data.RequireVerification, response.Body.String())
	return body.Data.FlowToken
}

func (env *authGuardTest) verifyCode(path string, flowToken string, code string) *httptest.ResponseRecorder {
	body, _ := common.Marshal(map[string]string{"flow_token": flowToken, "code": code})
	return env.request(http.MethodPost, path, string(body))
}

// In a password login flow, wrong two-factor codes count toward the login
// lockout of the account, on both routes of the step, and the lock applies
// to that step. Flows after a third-party login keep only the two-factor
// lock. The count is cleared when the login completes, not when the
// password alone was right.
func TestLoginTwoFactorFailuresCountInPasswordFlows(t *testing.T) {
	env := setupAuthGuardTest(t)
	common.LoginCaptchaEnabled = false
	common.LoginLockoutMaxFailures = 3
	jane := createAuthGuardUser(t, env.db, "twofa-jane", "jane@example.com", common.RoleCommonUser)
	factor := &model.TwoFA{UserId: jane.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
	require.NoError(t, env.db.Create(factor).Error)
	rightCode := func() string {
		code, err := totp.GenerateCode(factor.Secret, time.Now())
		require.NoError(t, err)
		return code
	}
	resetFactor := func() {
		require.NoError(t, env.db.Model(&model.TwoFA{}).Where("id = ?", factor.Id).Updates(map[string]any{"failed_attempts": 0, "locked_until": nil}).Error)
		middleware.ClearLoginLockout(context.Background(), jane.Id, jane.Username, jane.Email)
	}

	for _, path := range []string{"/api/user/login/verify", "/api/user/login/2fa"} {
		resetFactor()
		token := env.loginChallenge(t, "twofa-jane")
		for range 2 {
			assert.Equal(t, "SECURITY_VERIFICATION_FAILED", gjsonCode(t, env.verifyCode(path, token, "not-a-code")), path)
		}
		row := lastAuthGuardAudit(t, "user.login_2fa_failed")
		assert.Equal(t, map[string]any{"method": "password", "reason": "invalid_code"}, row.Params)
		assert.Equal(t, jane.Id, row.UserId)
		assert.Equal(t, model.AuditCategoryLogin, row.Category)
		assert.Equal(t, "Failed two-factor verification at login via password: invalid_code", row.Content)
		assert.JSONEq(t, loginLockedJSON(900), env.verifyCode(path, token, "not-a-code").Body.String(), path)
		assert.Equal(t, map[string]any{"method": "password", "reason": "invalid_code", "wait_seconds": float64(900), "lock_started": true},
			lastAuthGuardAudit(t, "user.login_2fa_failed").Params)
		assert.JSONEq(t, loginLockedJSON(900), env.verifyCode(path, token, rightCode()).Body.String(), "the right code while locked")
		assert.Equal(t, "locked", lastAuthGuardAudit(t, "user.login_2fa_failed").Params["reason"])
		assert.Equal(t, "LOGIN_LOCKED", gjsonCode(t, env.login("twofa-jane", authGuardTestPassword, "")))
	}

	// The two-factor lock (5 wrong codes) is unchanged; once it holds, the
	// step is refused before any check and nothing more is counted.
	resetFactor()
	common.LoginLockoutMaxFailures = 10
	token := env.loginChallenge(t, "twofa-jane")
	for range 5 {
		assert.Equal(t, "SECURITY_VERIFICATION_FAILED", gjsonCode(t, env.verifyCode("/api/user/login/verify", token, "not-a-code")))
	}
	rows := len(authGuardAudits(t, "user.login_2fa_failed"))
	assert.Equal(t, "SECURITY_METHOD_UNAVAILABLE", gjsonCode(t, env.verifyCode("/api/user/login/verify", token, rightCode())))
	assert.Len(t, authGuardAudits(t, "user.login_2fa_failed"), rows)
	require.NoError(t, env.db.Model(&model.TwoFA{}).Where("id = ?", factor.Id).Updates(map[string]any{"failed_attempts": 0, "locked_until": nil}).Error)
	for range 4 {
		assert.Equal(t, "SECURITY_VERIFICATION_FAILED", gjsonCode(t, env.verifyCode("/api/user/login/verify", token, "not-a-code")))
	}
	assert.Equal(t, "LOGIN_LOCKED", gjsonCode(t, env.verifyCode("/api/user/login/verify", token, "not-a-code")), "the refused step was not counted: 5 + 5 failures lock")

	// A flow after a third-party login is neither counted nor locked, and
	// completing it clears the password lock like any completed login.
	require.NoError(t, env.db.Model(&model.TwoFA{}).Where("id = ?", factor.Id).Updates(map[string]any{"failed_attempts": 0, "locked_until": nil}).Error)
	pending, err := service.StartLoginVerification(jane, "oauth:github", nil)
	require.NoError(t, err)
	assert.Equal(t, "SECURITY_VERIFICATION_FAILED", gjsonCode(t, env.verifyCode("/api/user/login/verify", pending.FlowToken, "not-a-code")))
	assert.Equal(t, map[string]any{"method": "oauth:github", "reason": "invalid_code"}, lastAuthGuardAudit(t, "user.login_2fa_failed").Params)
	response := env.verifyCode("/api/user/login/verify", pending.FlowToken, rightCode())
	require.True(t, loginSucceeded(t, response), response.Body.String())
	response = env.login("twofa-jane", authGuardTestPassword, "")
	assert.Contains(t, response.Body.String(), `"require_verification":true`, "a completed third-party login clears the password lock too")

	// The right password alone does not clear the count; a completed login does.
	resetFactor()
	common.LoginLockoutMaxFailures = 3
	for range 2 {
		env.login("twofa-jane", "wrong", "")
	}
	token = env.loginChallenge(t, "twofa-jane")
	assert.Equal(t, "LOGIN_LOCKED", gjsonCode(t, env.login("twofa-jane", "wrong", "")))
	resetFactor()
	for range 2 {
		env.login("twofa-jane", "wrong", "")
	}
	token = env.loginChallenge(t, "twofa-jane")
	response = env.verifyCode("/api/user/login/verify", token, rightCode())
	require.True(t, loginSucceeded(t, response), response.Body.String())
	for range 2 {
		assert.JSONEq(t, loginGenericJSON, env.login("twofa-jane", "wrong", "").Body.String())
	}
}

// Passkey and third-party logins do not check the password lock. Like any
// completed login, a completed one clears it; a third-party login still
// waiting for its verification step does not.
func TestPasskeyAndOAuthLoginIgnoreTheLock(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	previousMax := common.LoginLockoutMaxFailures
	common.LoginLockoutMaxFailures = 3
	t.Cleanup(func() { common.LoginLockoutMaxFailures = previousMax })
	key := newSecurityLoginPasskey(t, user.Id)
	login := func(password string) *httptest.ResponseRecorder {
		return securityEnrollmentRequest(http.MethodPost, "/api/user/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user.Username, password), "", service.AuthIdentity{}, Login)
	}
	for range 3 {
		login("wrong-password")
	}
	require.Equal(t, "LOGIN_LOCKED", gjsonCode(t, login("enrollment-password")))

	// A third-party login reaches its verification step (the passkey).
	const slug = "auth-guard-oauth"
	oauth.Register(slug, &boundLoginOAuthProvider{userID: user.Id})
	t.Cleanup(func() { oauth.Unregister(slug) })
	token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: model.AuthFlowPurposeOAuth, Provider: slug, Intent: model.AuthFlowIntentLogin, Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute)})
	require.NoError(t, err)
	oauthRouter := gin.New()
	oauthRouter.GET("/api/oauth/:provider", HandleOAuth)
	response := httptest.NewRecorder()
	oauthRouter.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/oauth/"+slug+"?state="+token+"&code=provider-code", nil))
	assert.Contains(t, response.Body.String(), `"require_verification":true`, response.Body.String())

	assert.Equal(t, "LOGIN_LOCKED", gjsonCode(t, login("enrollment-password")), "an unfinished login leaves the lock")

	// A primary passkey login.
	begin := securityEnrollmentRequest(http.MethodPost, "/api/user/passkey/login/begin", "", "", service.AuthIdentity{}, PasskeyLoginBegin)
	var started struct {
		Data struct {
			FlowToken string `json:"flow_token"`
			Options   struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
				} `json:"publicKey"`
			} `json:"options"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(begin.Body.Bytes(), &started))
	var assertion map[string]any
	require.NoError(t, common.Unmarshal(securityPasskeyResponse(t, key, started.Data.Options.PublicKey.Challenge, false, 0, true), &assertion))
	assertion["response"].(map[string]any)["userHandle"] = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(user.Id)))
	body, err := common.Marshal(map[string]any{"flow_token": started.Data.FlowToken, "credential": assertion})
	require.NoError(t, err)
	passkeyRouter := gin.New()
	passkeyRouter.POST("/api/user/passkey/login/finish", PasskeyLoginFinish)
	response = httptest.NewRecorder()
	passkeyRouter.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/user/passkey/login/finish", strings.NewReader(string(body))))
	assert.Contains(t, response.Body.String(), `"access_token"`, response.Body.String())

	unlocked := login("enrollment-password")
	assert.NotContains(t, unlocked.Body.String(), "LOGIN_LOCKED", "the completed passkey login cleared the lock")
	assert.Contains(t, unlocked.Body.String(), `"require_verification":true`, unlocked.Body.String())
}

type auditListResponse struct {
	Success bool `json:"success"`
	Message string
	Data    struct {
		Total int
		Items []struct {
			model.AuditLog
			Other struct {
				Op struct {
					Params map[string]any `json:"params"`
				} `json:"op"`
				RootInfo map[string]any `json:"root_info"`
			} `json:"other"`
		}
	} `json:"data"`
}

func (env *authGuardTest) audits(t *testing.T, path string, viewer *model.User) auditListResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-Test-User", strconv.Itoa(viewer.Id))
	request.Header.Set("X-Test-Role", strconv.Itoa(viewer.Role))
	env.router.ServeHTTP(recorder, request)
	var response auditListResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

// The new rows follow the visibility of the login rows: rows about root are
// root-only, anonymous rows are visible to administrators, and typed values
// that name no account and are not addresses are not stored, for any viewer.
// The console can filter them by action, IP and reset address.
func TestAuthGuardAuditVisibilityMaskingAndFilters(t *testing.T) {
	env := setupAuthGuardTest(t)
	root := createAuthGuardUser(t, env.db, "root", "root@example.com", common.RoleRootUser)
	admin := createAuthGuardUser(t, env.db, "audit-admin", "admin@example.com", common.RoleAdminUser)
	kim := createAuthGuardUser(t, env.db, "audit-kim", "kim@example.com", common.RoleCommonUser)

	env.login("root", authGuardTestPassword, "") // captcha missing: a row about root
	common.LoginCaptchaEnabled, common.PasswordResetCaptchaEnabled = false, false
	env.login("S3cret!pass", "wrong", "")
	env.login("  S3cret!pass ", "wrong", "") // the same value
	env.login("S3cret!pasS", "wrong", "")    // another value
	env.login("nobody@example.com", "wrong", "")
	env.login("audit-kim", "wrong", "")
	env.ip = "198.51.100.9"
	env.login("audit-kim", "wrong", "")
	env.ip = authGuardTestIP
	for _, email := range []string{"kim@example.com", "jimkim@example.com", "kim_x@example.com", "kimax@example.com"} {
		env.requestReset(email, "")
	}
	env.request(http.MethodPost, "/api/user/reset", `{"email":"kim@example.com","token":"wrong"}`)
	env.request(http.MethodPost, "/api/user/reset", `{"email":"kim_x@example.com","token":"wrong"}`)
	// Reset inputs that are not addresses: not stored either.
	env.requestReset("S3cret!pass", "")
	env.request(http.MethodPost, "/api/user/reset", `{"email":"S3cret!pass","token":"wrong"}`)
	// A valid quoted address that contains kim's address: written without a
	// captcha, and must not show up under kim's address.
	common.PasswordResetCaptchaEnabled = true
	env.requestReset(`"x for kim@example.com: sent"@evil.com`, "")
	common.PasswordResetCaptchaEnabled = false
	require.Equal(t, "captcha_failed", lastAuthGuardAudit(t, "user.password_reset_request").Params["outcome"])

	// A value that names no account and is not an address is nowhere in the
	// stored rows: not in params, content or any part of other (root_info
	// included). The rows keep "…", its length and its fingerprint: 8 hex
	// characters, the same for the same value and different for another.
	var stored []model.AuditLog
	require.NoError(t, model.LOG_DB.Order("id asc").Find(&stored).Error)
	for _, row := range stored {
		encoded, err := common.Marshal(row)
		require.NoError(t, err)
		assert.NotContains(t, strings.ToLower(string(encoded)), "s3cret!pas", "row %d (%s)", row.Id, row.Action)
		assert.Nil(t, row.Other.RootInfo, "row %d (%s)", row.Id, row.Action)
	}
	fingerprint := regexp.MustCompile(`^[0-9a-f]{8}$`)
	var loginPrints []any
	for _, row := range authGuardAudits(t, "user.login_failed") {
		switch row.Params["identifier"] {
		case "…":
			assert.Equal(t, float64(11), row.Params["identifier_length"])
			assert.Regexp(t, fingerprint, row.Params["identifier_fingerprint"])
			assert.Equal(t, "Failed login via password as …: invalid_credentials", row.Content)
			loginPrints = append(loginPrints, row.Params["identifier_fingerprint"])
		case "nobody@example.com", "audit-kim", "root":
			assert.NotContains(t, row.Params, "identifier_length", "a matched name or an email address is kept as typed")
			assert.NotContains(t, row.Params, "identifier_fingerprint", "a matched name or an email address is kept as typed")
		default:
			t.Errorf("unexpected identifier %v", row.Params["identifier"])
		}
	}
	require.Len(t, loginPrints, 3)
	assert.Equal(t, loginPrints[0], loginPrints[1], "the same value")
	assert.NotEqual(t, loginPrints[0], loginPrints[2], "another value")
	requests := authGuardAudits(t, "user.password_reset_request")
	request, failed := requests[4], lastAuthGuardAudit(t, "user.password_reset_failed")
	for _, row := range []authGuardAudit{request, failed} {
		assert.Equal(t, "…", row.Params["email"], row.Action)
		assert.Equal(t, float64(11), row.Params["identifier_length"], row.Action)
		assert.Regexp(t, fingerprint, row.Params["identifier_fingerprint"], row.Action)
	}
	assert.Equal(t, request.Params["identifier_fingerprint"], failed.Params["identifier_fingerprint"], "the same reset input")
	assert.Equal(t, "kim@example.com", requests[0].Params["email"], "a valid address is kept as typed")
	assert.NotContains(t, requests[0].Params, "identifier_fingerprint")

	all := env.audits(t, "/api/audit?action=user.login_failed", root)
	require.True(t, all.Success, all.Message)
	assert.Equal(t, 7, all.Data.Total)
	adminView := env.audits(t, "/api/audit?action=user.login_failed", admin)
	assert.Equal(t, 6, adminView.Data.Total, "the captcha row about root is root-only")
	for _, item := range adminView.Data.Items {
		assert.NotEqual(t, root.Id, item.UserId)
		assert.Nil(t, item.Other.RootInfo)
		assert.NotContains(t, item.Content, "S3cret!pass")
	}
	masked := env.audits(t, "/api/audit?action=user.login_failed&username=", admin)
	var found bool
	for _, item := range masked.Data.Items {
		if item.Other.Op.Params["identifier"] == "…" {
			found = true
			assert.Equal(t, float64(11), item.Other.Op.Params["identifier_length"])
			assert.Regexp(t, fingerprint, item.Other.Op.Params["identifier_fingerprint"])
			assert.Zero(t, item.UserId)
		}
	}
	assert.True(t, found)
	for _, item := range all.Data.Items {
		assert.Nil(t, item.Other.RootInfo, "root gets no typed value either")
	}

	// The user's own view shows the rows about the account, without root info.
	self := env.audits(t, "/api/audit/self?category=login", kim)
	assert.Equal(t, 4, self.Data.Total, "two failed logins, a reset request and a failed reset")
	for _, item := range self.Data.Items {
		assert.Equal(t, kim.Id, item.UserId)
	}

	// Filters.
	byIP := env.audits(t, "/api/audit?action=user.login_failed&ip=198.51.100.9", root)
	assert.Equal(t, 1, byIP.Data.Total)
	assert.Equal(t, "198.51.100.9", byIP.Data.Items[0].Ip)
	byEmail := env.audits(t, "/api/audit?email=KIM@example.com", root)
	assert.Equal(t, 2, byEmail.Data.Total, "kim's reset request and failed reset, not jimkim, kim_x or the quoted address")
	for _, item := range byEmail.Data.Items {
		assert.Equal(t, "kim@example.com", item.Other.Op.Params["email"])
	}
	escaped := env.audits(t, "/api/audit?email=kim_x@example.com", root)
	assert.Equal(t, 2, escaped.Data.Total, "an underscore matches itself only, not kimax")
	for _, item := range escaped.Data.Items {
		assert.Equal(t, "kim_x@example.com", item.Other.Op.Params["email"])
	}
	for _, filter := range []string{"ip=not-an-ip", "action=Bad%20Action", "email=nope"} {
		response := env.audits(t, "/api/audit?"+filter, root)
		assert.False(t, response.Success, filter)
		assert.Equal(t, "Invalid audit filters", response.Message, filter)
	}
}
