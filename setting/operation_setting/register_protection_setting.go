package operation_setting

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
)

// Option keys of the registration anti-abuse settings. Their values live in
// the common package next to the other sign-up switches.
const (
	RegisterCaptchaEnabledKey     = "RegisterCaptchaEnabled"
	EmailQQNumericOnlyEnabledKey  = "EmailQQNumericOnlyEnabled"
	RegisterIPRateLimitEnabledKey = "RegisterIPRateLimitEnabled"
	RegisterIPRateLimitNumKey     = "RegisterIPRateLimitNum"
	RegisterIPRateLimitMinutesKey = "RegisterIPRateLimitMinutes"
)

// Accepted ranges of the per-network registration limit. The longest window
// is one day, which the in-memory fallback keeps idle keys for.
const (
	RegisterIPRateLimitMaxNum     = 1000
	RegisterIPRateLimitMaxMinutes = 1440
)

// ValidateRegisterProtectionOption checks a registration anti-abuse option
// before it is saved. Keys it does not know are accepted.
func ValidateRegisterProtectionOption(key string, value string) error {
	switch key {
	case RegisterCaptchaEnabledKey, EmailQQNumericOnlyEnabledKey, RegisterIPRateLimitEnabledKey:
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
	}
	return nil
}
