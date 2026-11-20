package operation_setting

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
)

// Option keys of the registration, password reset and login anti-abuse
// settings. Their values live in the common package next to the other
// sign-up and sign-in switches.
const (
	RegisterCaptchaEnabledKey     = "RegisterCaptchaEnabled"
	EmailQQNumericOnlyEnabledKey  = "EmailQQNumericOnlyEnabled"
	RegisterIPRateLimitEnabledKey = "RegisterIPRateLimitEnabled"
	RegisterIPRateLimitNumKey     = "RegisterIPRateLimitNum"
	RegisterIPRateLimitMinutesKey = "RegisterIPRateLimitMinutes"

	PasswordResetCaptchaEnabledKey       = "PasswordResetCaptchaEnabled"
	PasswordResetEmailLimitEnabledKey    = "PasswordResetEmailLimitEnabled"
	PasswordResetEmailCooldownSecondsKey = "PasswordResetEmailCooldownSeconds"
	PasswordResetEmailDailyMaxKey        = "PasswordResetEmailDailyMax"
	LoginCaptchaEnabledKey               = "LoginCaptchaEnabled"
	LoginLockoutEnabledKey               = "LoginLockoutEnabled"
	LoginLockoutMaxFailuresKey           = "LoginLockoutMaxFailures"
	LoginLockoutMinutesKey               = "LoginLockoutMinutes"
)

// Accepted ranges of the per-network registration limit. The longest window
// is one day, which the in-memory fallback keeps idle keys for.
const (
	RegisterIPRateLimitMaxNum     = 1000
	RegisterIPRateLimitMaxMinutes = 1440
)

// Accepted ranges of the password reset email limit (the daily maximum counts
// over a rolling 24 hours) and of the login lockout (the lock lasts as long
// as the window in which failures are counted).
const (
	PasswordResetEmailCooldownMinSeconds = 30
	PasswordResetEmailCooldownMaxSeconds = 3600
	PasswordResetEmailDailyMaxLimit      = 100
	LoginLockoutMinFailures              = 3
	LoginLockoutMaxFailuresLimit         = 100
	LoginLockoutMaxMinutes               = 1440
)

// ValidateRegisterProtectionOption checks a registration, password reset or
// login anti-abuse option before it is saved. Keys it does not know are
// accepted.
func ValidateRegisterProtectionOption(key string, value string) error {
	switch key {
	case RegisterCaptchaEnabledKey, EmailQQNumericOnlyEnabledKey, RegisterIPRateLimitEnabledKey,
		PasswordResetCaptchaEnabledKey, PasswordResetEmailLimitEnabledKey, LoginCaptchaEnabledKey, LoginLockoutEnabledKey:
		if value != "true" && value != "false" {
			return common.NewMessage("Value must be true or false")
		}
	case RegisterIPRateLimitNumKey:
		count, err := strconv.Atoi(value)
		if err != nil || count < 1 || count > RegisterIPRateLimitMaxNum {
			return common.NewMessage("Registration rate limit count must be an integer between 1 and 1000")
		}
	case RegisterIPRateLimitMinutesKey:
		minutes, err := strconv.Atoi(value)
		if err != nil || minutes < 1 || minutes > RegisterIPRateLimitMaxMinutes {
			return common.NewMessage("Registration rate limit window must be an integer between 1 and 1440 minutes")
		}
	case PasswordResetEmailCooldownSecondsKey:
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < PasswordResetEmailCooldownMinSeconds || seconds > PasswordResetEmailCooldownMaxSeconds {
			return common.NewMessage("Password reset email cooldown must be an integer between 30 and 3600 seconds")
		}
	case PasswordResetEmailDailyMaxKey:
		count, err := strconv.Atoi(value)
		if err != nil || count < 1 || count > PasswordResetEmailDailyMaxLimit {
			return common.NewMessage("Password reset email limit must be an integer between 1 and 100 per day")
		}
	case LoginLockoutMaxFailuresKey:
		count, err := strconv.Atoi(value)
		if err != nil || count < LoginLockoutMinFailures || count > LoginLockoutMaxFailuresLimit {
			return common.NewMessage("Login lockout failure count must be an integer between 3 and 100")
		}
	case LoginLockoutMinutesKey:
		minutes, err := strconv.Atoi(value)
		if err != nil || minutes < 1 || minutes > LoginLockoutMaxMinutes {
			return common.NewMessage("Login lockout duration must be an integer between 1 and 1440 minutes")
		}
	}
	return nil
}
