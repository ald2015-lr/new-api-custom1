package operation_setting

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// CheckinSetting 签到功能配置
type CheckinSetting struct {
	Enabled  bool `json:"enabled"`   // 是否启用签到功能
	MinQuota int  `json:"min_quota"` // 签到最小额度奖励
	MaxQuota int  `json:"max_quota"` // 签到最大额度奖励
	// PaidThresholdQuota is the cumulative recharge (quota) that makes a user a
	// paid user; 0 means any recharge at all.
	PaidThresholdQuota int `json:"paid_threshold_quota"`
	// PaidMultiplier multiplies a paid user's check-in reward (rounded down).
	PaidMultiplier float64 `json:"paid_multiplier"`
	// FreeExpireDays / PaidExpireDays: lifetime of the reward; 0 = never expires.
	FreeExpireDays int `json:"free_expire_days"`
	PaidExpireDays int `json:"paid_expire_days"`
	// ExpireOnlyWhenUnused: a reward expires only after its expiry days pass
	// without any wallet spending; every use restarts the period.
	ExpireOnlyWhenUnused bool `json:"expire_only_when_unused"`
}

// 默认配置
var checkinSetting = CheckinSetting{
	Enabled:            false, // 默认关闭
	MinQuota:           1000,  // 默认最小额度 1000 (约 0.002 USD)
	MaxQuota:           10000, // 默认最大额度 10000 (约 0.02 USD)
	PaidThresholdQuota: int(2 * common.QuotaPerUnit),
	PaidMultiplier:     2,
	FreeExpireDays:     30,
	PaidExpireDays:     30,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("checkin_setting", &checkinSetting)
}

// GetCheckinSetting 获取签到配置
func GetCheckinSetting() *CheckinSetting {
	return &checkinSetting
}

// IsCheckinEnabled 是否启用签到功能
func IsCheckinEnabled() bool {
	return checkinSetting.Enabled
}

// GetCheckinQuotaRange 获取签到额度范围
func GetCheckinQuotaRange() (min, max int) {
	return checkinSetting.MinQuota, checkinSetting.MaxQuota
}

// IsPaidCheckinUser reports whether a cumulative recharge makes the user a
// paid user for check-in rewards. A threshold of 0 means any recharge.
func IsPaidCheckinUser(recharged int64) bool {
	return recharged > 0 && recharged >= int64(checkinSetting.PaidThresholdQuota)
}

// CheckinExpireDays returns the lifetime in days of a free or paid check-in
// reward; 0 means it never expires.
func CheckinExpireDays(paid bool) int {
	if paid {
		return checkinSetting.PaidExpireDays
	}
	return checkinSetting.FreeExpireDays
}
