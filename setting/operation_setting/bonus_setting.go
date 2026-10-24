package operation_setting

import (
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/common"
)

// Option keys of the check-in and invite reward settings that need validation.
const (
	CheckinPaidThresholdQuotaOptionKey   = "checkin_setting.paid_threshold_quota"
	CheckinPaidMultiplierOptionKey       = "checkin_setting.paid_multiplier"
	CheckinFreeExpireDaysOptionKey       = "checkin_setting.free_expire_days"
	CheckinPaidExpireDaysOptionKey       = "checkin_setting.paid_expire_days"
	CheckinExpireOnlyWhenUnusedOptionKey = "checkin_setting.expire_only_when_unused"
	InviteRechargeThresholdQuotaKey      = "InviteRechargeThresholdQuota"
	InviteRewardExpireDaysKey            = "InviteRewardExpireDays"
	InviteRewardExpireOnlyWhenUnusedKey  = "InviteRewardExpireOnlyWhenUnused"
)

const (
	bonusMaxExpireDays     = 3650
	bonusMaxPaidMultiplier = 100
)

// ValidateBonusOption checks a check-in or invite reward option before it is
// saved. Keys it does not know are accepted.
func ValidateBonusOption(key string, value string) error {
	switch key {
	case CheckinPaidThresholdQuotaOptionKey, InviteRechargeThresholdQuotaKey:
		quota, err := strconv.ParseInt(value, 10, 64)
		if err != nil || quota < 0 || quota > common.MaxWalletQuota {
			return common.NewMessage("Quota threshold must be an integer between 0 and {{max}}", map[string]any{"max": common.MaxWalletQuota})
		}
	case CheckinPaidMultiplierOptionKey:
		// At most two decimals, so the multiplier the pages show (rounded to two
		// decimals) is the one the reward is computed with.
		multiplier, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier < 1 || multiplier > bonusMaxPaidMultiplier ||
			math.Abs(multiplier*100-math.Round(multiplier*100)) > 1e-9 {
			return common.NewMessage("Paid check-in multiplier must be a number between 1 and 100 with at most 2 decimal places")
		}
	case CheckinFreeExpireDaysOptionKey, CheckinPaidExpireDaysOptionKey, InviteRewardExpireDaysKey:
		days, err := strconv.Atoi(value)
		if err != nil || days < 0 || days > bonusMaxExpireDays {
			return common.NewMessage("Expiry days must be an integer between 0 and 3650")
		}
	case CheckinExpireOnlyWhenUnusedOptionKey, InviteRewardExpireOnlyWhenUnusedKey:
		if value != "true" && value != "false" {
			return common.NewMessage("Value must be true or false")
		}
	}
	return nil
}
