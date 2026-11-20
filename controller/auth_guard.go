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
// is not an email address is masked for every viewer but root: params and
// content show only "…" and its length (no part of the value), and the raw
// value goes to the root-only part of the row.
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
	return auditTypedValue{value: "…", raw: typed, length: length}
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

// recordAuthGuardAudit writes a login-category row of this feature. The row
// belongs to the account when it names one, else to no user with the common
// user role (a valid role, so administrators see the row).
func recordAuthGuardAudit(c *gin.Context, user *model.User, action string, params map[string]any, other model.AuditOther, success bool) {
	entry := model.LoginAuditEntry{ActorRole: common.RoleCommonUser, Action: action, Content: auditContentEN(action, params), Params: params, Other: other, Success: success}
	if user != nil && user.Id > 0 {
		entry.UserId, entry.Username, entry.ActorRole = user.Id, user.Username, user.Role
	}
	model.RecordLoginAudit(c, entry)
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

// writeLoginLocked answers a password login (or its two-factor step)
// refused by the login lockout: HTTP 200, LOGIN_LOCKED, the seconds and the
// whole minutes to wait. The answer is the same for known and unknown names.
func writeLoginLocked(c *gin.Context, waitSeconds int64) {
	minutes := max((waitSeconds+59)/60, 1)
	message := common.NewMessage("Too many failed sign-in attempts. Please try again in {{minutes}} minutes or reset your password.", map[string]any{"minutes": minutes})
	common.ApiErrorStatus(c, http.StatusOK, message, gin.H{"code": middleware.LoginLockedCode, "wait_seconds": waitSeconds})
}

// loginFailure is one refused password login.
type loginFailure struct {
	account     *model.User
	looked      bool   // the account was looked up already (account nil: no account)
	identifier  string // the typed username or email, trimmed
	reason      string // invalid_credentials, disabled, locked, captcha_failed, invalid_request
	code        string // captcha_failed only
	waitSeconds int64  // while a lock is in effect
	lockStarted bool   // this failure started the lock
}

// recordLoginFailureAudit writes the user.login_failed row of a refused
// password login. A row about an account belongs to it, so the console shows
// which account was attacked and rows about root stay root-only; refusals
// before the lookup (captcha, request) look the account up for that only.
// The identifier is kept as typed when it names an account or is an email
// address, else masked for all but root.
func recordLoginFailureAudit(c *gin.Context, failure loginFailure) {
	account := failure.account
	if !failure.looked && account == nil && failure.identifier != "" {
		account, _ = model.FindPasswordLoginUser(failure.identifier)
	}
	params := map[string]any{"method": "password", "reason": failure.reason}
	other := model.AuditOther{LoginMethod: "password", UserAgent: c.Request.UserAgent()}
	newAuditTypedValue(failure.identifier, auditIdentifierMaxRunes, account != nil).apply("identifier", params, &other)
	if failure.waitSeconds > 0 {
		params["wait_seconds"] = failure.waitSeconds
	}
	if failure.lockStarted {
		params["lock_started"] = true
	}
	if failure.code != "" {
		params["code"] = failure.code
	}
	recordAuthGuardAudit(c, account, "user.login_failed", params, other, false)
}

// recordLogin2FAFailureAudit writes the user.login_2fa_failed row of a
// refused two-factor step: reason invalid_code, twofa_locked or locked.
func recordLogin2FAFailureAudit(c *gin.Context, userID int, loginMethod string, reason string, waitSeconds int64, lockStarted bool) {
	account, err := model.GetUserById(userID, false)
	if err != nil {
		// The role is unknown, so the row stays root-only.
		account = &model.User{Id: userID}
	}
	params := map[string]any{"method": loginMethod, "reason": reason}
	if waitSeconds > 0 {
		params["wait_seconds"] = waitSeconds
	}
	if lockStarted {
		params["lock_started"] = true
	}
	other := model.AuditOther{LoginMethod: loginMethod, UserAgent: c.Request.UserAgent()}
	recordAuthGuardAudit(c, account, "user.login_2fa_failed", params, other, false)
}
