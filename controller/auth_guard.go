package controller

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// Longest typed values kept in an audit row, in runes.
const (
	auditIdentifierMaxRunes = 64
	auditEmailMaxRunes      = 254
)

// auditTypedValue is a login name or an email address a client typed, as an
// audit row keeps it. Users sometimes type their password into the name box,
// and such a value matches no account, so a value that names no account and
// is not an email address is masked for every viewer but root: the row keeps
// its first two runes and its length, and the raw value goes to the
// root-only part of the row.
type auditTypedValue struct {
	value  string // what params and content show
	raw    string // the value before masking; set only when masked
	length int    // runes of the trimmed value; set only when masked
}

func newAuditTypedValue(typed string, maxRunes int, matchesAccount bool) auditTypedValue {
	typed = strings.TrimSpace(typed)
	length := utf8.RuneCountInString(typed)
	isEmail := common.Validate.Var(typed, "email") == nil
	if length > maxRunes {
		typed = string([]rune(typed)[:maxRunes])
	}
	if typed == "" || matchesAccount || isEmail {
		return auditTypedValue{value: typed}
	}
	return auditTypedValue{value: maskAuditIdentifier(typed), raw: typed, length: length}
}

// maskAuditIdentifier keeps the first two runes of a value; values of two
// runes or fewer become "…".
func maskAuditIdentifier(value string) string {
	runes := []rune(value)
	if len(runes) <= 2 {
		return "…"
	}
	return string(runes[:2]) + "…"
}

// apply stores the value under key in params and, when masked, its length in
// params and the raw value in the root-only part of the row.
func (typed auditTypedValue) apply(key string, params map[string]any, other *model.AuditOther) {
	params[key] = typed.value
	if typed.raw == "" {
		return
	}
	params["identifier_length"] = typed.length
	other.RootInfo = model.AuditFields{key: typed.raw}
}

// auditAccountFields returns the owner of a sign-in or password reset row:
// the account when the row names one, else no user with the common user role
// (a valid role, so administrators see the row).
func auditAccountFields(user *model.User) (int, string, int) {
	if user == nil || user.Id <= 0 {
		return 0, "", common.RoleCommonUser
	}
	return user.Id, user.Username, user.Role
}

// lookupResetAccount returns the account with exactly this address, or nil.
func lookupResetAccount(email string) *model.User {
	if common.Validate.Var(email, "required,email") != nil {
		return nil
	}
	user, err := model.GetUniqueUserByEmail(email)
	if err != nil {
		return nil
	}
	return user
}

// passwordResetRequest collects the outcome of one GET /api/reset_password
// request; record writes its audit row.
type passwordResetRequest struct {
	email       string
	user        *model.User
	outcome     string // sent, not_registered, send_failed, cooldown, daily_limit, captcha_failed, invalid_email
	detail      string // send_failed only: smtp_error, email_ambiguous, lookup_error
	code        string // refusal code
	waitSeconds int64
}

func (request *passwordResetRequest) record(c *gin.Context) {
	if request.outcome == "" {
		return
	}
	user := request.user
	isAddress := common.Validate.Var(request.email, "required,email") == nil
	if user == nil && isAddress {
		user = lookupResetAccount(request.email)
	}
	params := map[string]any{"outcome": request.outcome}
	other := model.AuditOther{UserAgent: c.Request.UserAgent()}
	newAuditTypedValue(request.email, auditEmailMaxRunes, isAddress).apply("email", params, &other)
	if request.detail != "" {
		params["detail"] = request.detail
	}
	if request.code != "" {
		params["code"] = request.code
	}
	if request.waitSeconds > 0 {
		params["wait_seconds"] = request.waitSeconds
	}
	recordAuthGuardAudit(c, user, "user.password_reset_request", params, other, request.outcome == "sent")
}

// recordPasswordResetAudit writes the row of one POST /api/user/reset
// request: user.password_reset on success, else user.password_reset_failed
// with the reason (invalid_request, invalid_link, server_error). The new
// password never enters the row.
func recordPasswordResetAudit(c *gin.Context, email string, user *model.User, reason string) {
	isAddress := common.Validate.Var(email, "required,email") == nil
	if user == nil && reason != "" && isAddress {
		user = lookupResetAccount(email)
	}
	params := map[string]any{}
	other := model.AuditOther{UserAgent: c.Request.UserAgent()}
	newAuditTypedValue(email, auditEmailMaxRunes, isAddress).apply("email", params, &other)
	action := "user.password_reset"
	if reason != "" {
		action = "user.password_reset_failed"
		params["reason"] = reason
	}
	recordAuthGuardAudit(c, user, action, params, other, reason == "")
}

func recordAuthGuardAudit(c *gin.Context, user *model.User, action string, params map[string]any, other model.AuditOther, success bool) {
	userID, username, role := auditAccountFields(user)
	model.RecordLoginAudit(c, model.LoginAuditEntry{
		UserId: userID, Username: username, ActorRole: role,
		Action: action, Content: auditContentEN(action, params), Params: params, Other: other, Success: success,
	})
}

// writePasswordResetLimited answers a reset email request the per-email
// limit refused (HTTP 200, the code and the seconds to wait). The texts say
// the email may come late or land in the spam folder, and that only the
// newest link works, so users stop clicking again.
func writePasswordResetLimited(c *gin.Context, refusal *middleware.PasswordResetEmailRefusal) {
	message := common.NewMessage("A password reset email was requested for this address recently. Please wait {{seconds}} seconds and try again. The email can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working.",
		map[string]any{"seconds": refusal.WaitSeconds})
	if refusal.Code == middleware.PasswordResetEmailDailyLimitCode {
		message = common.NewMessage("Too many password reset emails were requested for this address in the last 24 hours. Please try again later or contact the administrator. Emails already sent can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working.")
	}
	common.ApiErrorStatus(c, http.StatusOK, message, gin.H{"code": refusal.Code, "wait_seconds": refusal.WaitSeconds})
}
