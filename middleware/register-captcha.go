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
		captchaID := c.Query("captcha_id")
		answer := c.Query("captcha")
		if captchaID == "" || strings.TrimSpace(answer) == "" {
			common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Please complete the captcha."), gin.H{"code": "CAPTCHA_REQUIRED"})
			c.Abort()
			return
		}
		if !service.VerifyCaptcha(captchaID, answer) {
			common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Captcha is incorrect or has expired. Please try a new one."), gin.H{"code": "CAPTCHA_INVALID"})
			c.Abort()
			return
		}
		c.Next()
	}
}
