package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// RegisterCaptchaCheck asks for the arithmetic captcha of the registration
// page (query parameters captcha_id and captcha) while RegisterCaptchaEnabled
// is on. A check that names an existing captcha uses it up, right or wrong.
//
// skipWhenEmailVerified is set on the registration submit: while email
// verification is on, registration needs an email code, and the code is only
// sent after a solved captcha, so one solve covers one account.
func RegisterCaptchaCheck(skipWhenEmailVerified bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.RegisterCaptchaEnabled || (skipWhenEmailVerified && common.EmailVerificationEnabled) {
			c.Next()
			return
		}
		if code := CheckRequestCaptcha(c); code != "" {
			WriteCaptchaRefusal(c, code)
			return
		}
		c.Next()
	}
}

// Codes of the captcha refusals.
const (
	CaptchaRequiredCode = "CAPTCHA_REQUIRED"
	CaptchaInvalidCode  = "CAPTCHA_INVALID"
)

// CheckRequestCaptcha checks the arithmetic captcha of the request (query
// parameters captcha_id and captcha) and uses it up. It returns "" when it is
// solved, otherwise CAPTCHA_REQUIRED or CAPTCHA_INVALID. The registration,
// forgot password and password login pages share the same captchas.
func CheckRequestCaptcha(c *gin.Context) string {
	captchaID := c.Query("captcha_id")
	answer := c.Query("captcha")
	if captchaID == "" || strings.TrimSpace(answer) == "" {
		return CaptchaRequiredCode
	}
	if !service.VerifyCaptcha(captchaID, answer) {
		return CaptchaInvalidCode
	}
	return ""
}

// WriteCaptchaRefusal answers a request whose captcha check failed (HTTP 200,
// success false, the code) and aborts the chain.
func WriteCaptchaRefusal(c *gin.Context, code string) {
	message := common.NewMessage("Captcha is incorrect or has expired. Please try a new one.")
	if code == CaptchaRequiredCode {
		message = common.NewMessage("Please complete the captcha.")
	}
	common.ApiErrorStatus(c, http.StatusOK, message, gin.H{"code": code})
	c.Abort()
}
