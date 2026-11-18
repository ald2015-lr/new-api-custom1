package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type registerTestResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	MessageKey string `json:"message_key"`
	Code       string `json:"code"`
}

// setupRegisterTest opens the database TEST_MANAGE_USER_DIALECT selects and
// sets the sign-up settings of one test: registration open, QQ number rule
// on, no domain or alias restriction, and the per-network limit off (the API
// router test covers it).
func setupRegisterTest(t *testing.T, emailVerification bool, captcha bool) *gorm.DB {
	t.Helper()
	db := setupManageUserTestDB(t)
	previousRegister, previousPassword, previousTurnstile := common.RegisterEnabled, common.PasswordRegisterEnabled, common.TurnstileCheckEnabled
	previousVerification, previousCaptcha, previousQQ := common.EmailVerificationEnabled, common.RegisterCaptchaEnabled, common.EmailQQNumericOnlyEnabled
	previousDomain, previousWhitelist, previousAlias := common.EmailDomainRestrictionEnabled, common.EmailDomainWhitelist, common.EmailAliasRestrictionEnabled
	previousLimit := common.RegisterIPRateLimitEnabled
	common.RegisterEnabled, common.PasswordRegisterEnabled, common.TurnstileCheckEnabled = true, true, false
	common.EmailVerificationEnabled, common.RegisterCaptchaEnabled, common.EmailQQNumericOnlyEnabled = emailVerification, captcha, true
	common.EmailDomainRestrictionEnabled, common.EmailAliasRestrictionEnabled = false, false
	common.RegisterIPRateLimitEnabled = false
	t.Cleanup(func() {
		common.RegisterEnabled, common.PasswordRegisterEnabled, common.TurnstileCheckEnabled = previousRegister, previousPassword, previousTurnstile
		common.EmailVerificationEnabled, common.RegisterCaptchaEnabled, common.EmailQQNumericOnlyEnabled = previousVerification, previousCaptcha, previousQQ
		common.EmailDomainRestrictionEnabled, common.EmailDomainWhitelist, common.EmailAliasRestrictionEnabled = previousDomain, previousWhitelist, previousAlias
		common.RegisterIPRateLimitEnabled = previousLimit
	})
	return db
}

// newRegisterTestRouter mounts the registration endpoints behind the captcha
// check the API router puts in front of them.
func newRegisterTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/user/register", middleware.RegisterCaptchaCheck(true), Register)
	router.GET("/api/verification", middleware.RegisterCaptchaCheck(false), SendEmailVerification)
	router.GET("/api/captcha", GetRegisterCaptcha)
	return router
}

func performRegisterTestRequest(t *testing.T, router http.Handler, method string, target string, body string) registerTestResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "register-test-agent")
	request.RemoteAddr = "203.0.113.7:4321"
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response registerTestResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

func registerTestBody(username string, email string, code string, affCode string) string {
	return fmt.Sprintf(`{"username":%q,"password":"register-password","email":%q,"verification_code":%q,"aff_code":%q}`, username, email, code, affCode)
}

func captchaQuery(id string, answer int) string {
	return "captcha_id=" + id + "&captcha=" + strconv.Itoa(answer)
}

func countUsersNamed(t *testing.T, db *gorm.DB, username string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", username).Count(&count).Error)
	return count
}

// registrationMailCode reads the code of the last registration mail sent to
// receiver. Registration codes are hexadecimal.
func registrationMailCode(t *testing.T, mailbox *securityMailbox, receiver string) string {
	t.Helper()
	mailbox.mutex.Lock()
	defer mailbox.mutex.Unlock()
	messages := mailbox.mail[receiver]
	require.NotEmpty(t, messages, "no verification email delivered to %s", receiver)
	match := regexp.MustCompile(`<strong>([0-9a-f]{6})</strong>`).FindStringSubmatch(messages[len(messages)-1])
	require.Len(t, match, 2)
	return match[1]
}

func assertRegisterAudit(t *testing.T, user model.User, method string) {
	t.Helper()
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ? AND user_id = ?", "user.register", user.Id).Find(&audits).Error)
	require.Len(t, audits, 1)
	audit := audits[0]
	assert.Equal(t, user.Username, audit.Username)
	assert.Equal(t, model.AuditCategoryLogin, audit.Category)
	assert.Equal(t, common.RoleCommonUser, audit.ActorRole)
	assert.Equal(t, "203.0.113.7", audit.Ip)
	assert.Equal(t, "register-test-agent", audit.UserAgent)
	assert.True(t, audit.Success)
	assert.Equal(t, "Registered a new account via "+method, audit.Content)
	require.NotNil(t, audit.Other.Op)
	params, err := common.Marshal(audit.Other.Op.Params)
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(`{"method":%q,"inviter_id":%d}`, method, user.InviterId), string(params))
}

func TestRegisterAsksForTheCaptchaWhileEmailVerificationIsOff(t *testing.T) {
	db := setupRegisterTest(t, false, true)
	router := newRegisterTestRouter()
	inviter := model.User{Username: "register-inviter", Password: "inviter-password", AffCode: "reg-inviter", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&inviter).Error)

	response := performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-captcha", "", "", "reg-inviter"))
	assert.False(t, response.Success)
	assert.Equal(t, "CAPTCHA_REQUIRED", response.Code)
	assert.Equal(t, "Please complete the captcha.", response.MessageKey)

	id := service.IssueCaptchaForTest(5)
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register?"+captchaQuery(id, 6), registerTestBody("reg-captcha", "", "", "reg-inviter"))
	assert.Equal(t, "CAPTCHA_INVALID", response.Code)
	assert.Equal(t, "Captcha is incorrect or has expired. Please try a new one.", response.MessageKey)
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register?"+captchaQuery(id, 5), registerTestBody("reg-captcha", "", "", "reg-inviter"))
	assert.Equal(t, "CAPTCHA_INVALID", response.Code, "a checked captcha is used up")
	assert.Zero(t, countUsersNamed(t, db, "reg-captcha"))

	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register?"+captchaQuery(service.IssueCaptchaForTest(5), 5), registerTestBody("reg-captcha", "", "", "reg-inviter"))
	require.True(t, response.Success, response.Message)
	var created model.User
	require.NoError(t, db.Where("username = ?", "reg-captcha").First(&created).Error)
	assert.Empty(t, created.Email)
	assert.Equal(t, inviter.Id, created.InviterId)
	assertRegisterAudit(t, created, "password")

	common.RegisterCaptchaEnabled = false
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-no-captcha", "", "", ""))
	require.True(t, response.Success, response.Message)
	assert.EqualValues(t, 1, countUsersNamed(t, db, "reg-no-captcha"))
}

func TestRegisterWithEmailVerificationAsksForTheCaptchaBeforeTheCode(t *testing.T) {
	db := setupRegisterTest(t, true, true)
	mailbox := newSecurityMailbox(t)
	router := newRegisterTestRouter()

	response := performRegisterTestRequest(t, router, http.MethodGet, "/api/verification?email=10001@qq.com", "")
	assert.Equal(t, "CAPTCHA_REQUIRED", response.Code)
	mailbox.mutex.Lock()
	assert.Empty(t, mailbox.mail["10001@qq.com"], "no code is mailed without a solved captcha")
	mailbox.mutex.Unlock()

	response = performRegisterTestRequest(t, router, http.MethodGet, "/api/verification?email=10001@qq.com&"+captchaQuery(service.IssueCaptchaForTest(9), 9), "")
	require.True(t, response.Success, response.Message)
	code := registrationMailCode(t, mailbox, "10001@qq.com")

	// The submit is not captcha-gated: its email code was issued behind one.
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-email", "10001@QQ.com", code, ""))
	require.True(t, response.Success, response.Message)
	var created model.User
	require.NoError(t, db.Where("username = ?", "reg-email").First(&created).Error)
	assert.Equal(t, "10001@qq.com", created.Email)
	assertRegisterAudit(t, created, "password")
	assert.False(t, common.VerifyCodeWithKey("10001@qq.com", code, common.EmailVerificationPurpose), "a registration code is single use")

	common.RegisterCaptchaEnabled = false
	response = performRegisterTestRequest(t, router, http.MethodGet, "/api/verification?email=10002@qq.com", "")
	require.True(t, response.Success, response.Message)
	assert.NotEmpty(t, registrationMailCode(t, mailbox, "10002@qq.com"))
}

func TestRegisterEmailCodeIsDeletedAfterFiveWrongGuesses(t *testing.T) {
	db := setupRegisterTest(t, true, false)
	router := newRegisterTestRouter()

	common.RegisterVerificationCodeWithKey("10003@qq.com", "a1b2c3", common.EmailVerificationPurpose)
	for range 4 {
		response := performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-guess", "10003@qq.com", "ffffff", ""))
		assert.Equal(t, "Verification code is incorrect or has expired", response.MessageKey)
	}
	response := performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-guess", "10003@qq.com", "a1b2c3", ""))
	require.True(t, response.Success, response.Message, "four wrong guesses leave the code usable")

	common.RegisterVerificationCodeWithKey("10004@qq.com", "d4e5f6", common.EmailVerificationPurpose)
	for range 5 {
		response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-locked", "10004@qq.com", "ffffff", ""))
		assert.Equal(t, "Verification code is incorrect or has expired", response.MessageKey)
	}
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-locked", "10004@qq.com", "d4e5f6", ""))
	assert.False(t, response.Success)
	assert.Equal(t, "Verification code is incorrect or has expired", response.MessageKey, "the fifth wrong guess deletes the code")
	assert.Zero(t, countUsersNamed(t, db, "reg-locked"))

	// A password reset token is too long to guess, and wrong guesses must not
	// let others cancel the owner's reset link.
	common.RegisterVerificationCodeWithKey("10004@qq.com", "0123456789abcdef0123456789abcdef", common.PasswordResetPurpose)
	for range 5 {
		assert.False(t, common.VerifyCodeWithKey("10004@qq.com", "ffffffffffffffffffffffffffffffff", common.PasswordResetPurpose))
	}
	assert.True(t, common.VerifyCodeWithKey("10004@qq.com", "0123456789abcdef0123456789abcdef", common.PasswordResetPurpose))
	common.DeleteKey("10004@qq.com", common.PasswordResetPurpose)
}

func TestRegistrationEmailPolicyAppliesWhenTheCodeIsSentAndWhenItIsUsed(t *testing.T) {
	db := setupRegisterTest(t, true, false)
	mailbox := newSecurityMailbox(t)
	router := newRegisterTestRouter()
	const qqNumberKey = "QQ mail addresses must use the QQ number, for example 123456789@qq.com."

	response := performRegisterTestRequest(t, router, http.MethodGet, "/api/verification?email=owner.alias@qq.com", "")
	assert.Equal(t, "EMAIL_ADDRESS_REJECTED", response.Code)
	assert.Equal(t, qqNumberKey, response.MessageKey)
	response = performRegisterTestRequest(t, router, http.MethodGet, "/api/verification?email="+url.QueryEscape("１０００６@qq.com"), "")
	assert.Equal(t, "EMAIL_ADDRESS_REJECTED", response.Code)
	assert.Equal(t, "Please enter a valid email address", response.MessageKey)
	mailbox.mutex.Lock()
	assert.Empty(t, mailbox.mail)
	mailbox.mutex.Unlock()

	// A code issued before the rule changed no longer registers the address.
	common.RegisterVerificationCodeWithKey("owneralias@qq.com", "a1b2c3", common.EmailVerificationPurpose)
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-alias", "owneralias@qq.com", "a1b2c3", ""))
	assert.Equal(t, "EMAIL_ADDRESS_REJECTED", response.Code)
	assert.Equal(t, qqNumberKey, response.MessageKey)
	common.EmailDomainRestrictionEnabled, common.EmailDomainWhitelist = true, []string{"qq.com"}
	common.RegisterVerificationCodeWithKey("10005@163.com", "d4e5f6", common.EmailVerificationPurpose)
	response = performRegisterTestRequest(t, router, http.MethodPost, "/api/user/register", registerTestBody("reg-domain", "10005@163.com", "d4e5f6", ""))
	assert.Equal(t, "EMAIL_ADDRESS_REJECTED", response.Code)
	assert.Equal(t, "This email address is not allowed by the administrator's email policy.", response.MessageKey)
	assert.True(t, common.VerifyCodeWithKey("10005@163.com", "d4e5f6", common.EmailVerificationPurpose), "a refused address does not spend a guess")
	assert.Zero(t, countUsersNamed(t, db, "reg-alias")+countUsersNamed(t, db, "reg-domain"))

	common.EmailQQNumericOnlyEnabled = false
	response = performRegisterTestRequest(t, router, http.MethodGet, "/api/verification?email=owneralias@qq.com", "")
	require.True(t, response.Success, response.Message)
	assert.NotEmpty(t, registrationMailCode(t, mailbox, "owneralias@qq.com"))
}

func TestValidateAccountEmailRejectsAddressVariantsAndQQAliases(t *testing.T) {
	previousDomain, previousWhitelist := common.EmailDomainRestrictionEnabled, common.EmailDomainWhitelist
	previousAlias, previousQQ := common.EmailAliasRestrictionEnabled, common.EmailQQNumericOnlyEnabled
	t.Cleanup(func() {
		common.EmailDomainRestrictionEnabled, common.EmailDomainWhitelist = previousDomain, previousWhitelist
		common.EmailAliasRestrictionEnabled, common.EmailQQNumericOnlyEnabled = previousAlias, previousQQ
	})
	for _, tc := range []struct {
		email     string
		qqRule    bool
		alias     bool
		whitelist []string
		want      string
		err       error
	}{
		{email: " 10001@QQ.com ", qqRule: true, want: "10001@qq.com"},
		{email: "123456789012@qq.com", qqRule: true, want: "123456789012@qq.com"},
		{email: "1234567890123@qq.com", qqRule: true, err: service.ErrAccountEmailQQNumberRequired},
		{email: "1234@qq.com", qqRule: true, err: service.ErrAccountEmailQQNumberRequired},
		{email: "01234@qq.com", qqRule: true, err: service.ErrAccountEmailQQNumberRequired},
		{email: "owner@qq.com", qqRule: true, err: service.ErrAccountEmailQQNumberRequired},
		{email: "owner.name@qq.com", qqRule: true, alias: true, err: service.ErrAccountEmailQQNumberRequired},
		{email: "owner@qq.com", want: "owner@qq.com"},
		{email: "owner@foxmail.com", qqRule: true, want: "owner@foxmail.com"},
		{email: "owner@vip.qq.com", qqRule: true, want: "owner@vip.qq.com"},
		{email: "first.last+tag@example.com", qqRule: true, want: "first.last+tag@example.com"},
		{email: "first.last@example.com", qqRule: true, alias: true, err: service.ErrAccountEmailRestricted},
		{email: "owner@163.com", qqRule: true, whitelist: []string{"qq.com"}, err: service.ErrAccountEmailRestricted},
		{email: "owner@qq.com", qqRule: true, whitelist: []string{"gmail.com"}, err: service.ErrAccountEmailRestricted},
		{email: "１２３４５@qq.com", qqRule: true, err: service.ErrAccountEmailInvalid},
		{email: "１２３４５@qq.com", err: service.ErrAccountEmailInvalid},
		{email: "1234​5@qq.com", err: service.ErrAccountEmailInvalid},
		{email: "ｑｑ@example.com", err: service.ErrAccountEmailInvalid},
		{email: `"12345"@qq.com`, err: service.ErrAccountEmailInvalid},
		{email: `"1\2345"@qq.com`, err: service.ErrAccountEmailInvalid},
		{email: `"a;b"@example.com`, err: service.ErrAccountEmailInvalid},
		{email: "12345@ｑｑ.com", err: service.ErrAccountEmailInvalid},
		{email: "12345@qq.com.", err: service.ErrAccountEmailInvalid},
		{email: "12345@-qq.com", err: service.ErrAccountEmailInvalid},
		{email: "not-an-email", err: service.ErrAccountEmailInvalid},
	} {
		t.Run(tc.email, func(t *testing.T) {
			common.EmailQQNumericOnlyEnabled, common.EmailAliasRestrictionEnabled = tc.qqRule, tc.alias
			common.EmailDomainRestrictionEnabled, common.EmailDomainWhitelist = tc.whitelist != nil, tc.whitelist
			email, err := service.ValidateAccountEmail(tc.email)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				assert.Empty(t, email)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, email)
		})
	}
}

func TestRegisterCaptchaAndStatusFlags(t *testing.T) {
	setupRegisterTest(t, false, false)
	common.EmailQQNumericOnlyEnabled = false
	router := newRegisterTestRouter()

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/captcha", nil))
	var captcha struct {
		Success bool            `json:"success"`
		Data    service.Captcha `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &captcha))
	require.True(t, captcha.Success, "a captcha is served while the option is off")
	assert.Regexp(t, `^[0-9a-f]{32}$`, captcha.Data.ID)
	assert.True(t, strings.HasPrefix(captcha.Data.Image, "data:image/png;base64,"))
	assert.Equal(t, 300, captcha.Data.ExpiresIn)

	for _, enabled := range []bool{false, true} {
		common.RegisterCaptchaEnabled, common.EmailQQNumericOnlyEnabled = enabled, enabled
		recorder = httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
		GetStatus(context)
		var status struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &status))
		assert.Equal(t, enabled, status.Data["register_captcha"])
		assert.Equal(t, enabled, status.Data["email_qq_numeric_only"])
	}
}
