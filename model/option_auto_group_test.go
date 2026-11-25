package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOptionValueRejectsInvalidMaxTokenAutoGroups(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue("MaxTokenAutoGroups", value))
		})
	}
	require.NoError(t, validateOptionValue("MaxTokenAutoGroups", "999999"))
}

func TestValidateOptionValueRejectsPingIntervalOutsideRange(t *testing.T) {
	const key = "general_setting.ping_interval_seconds"
	for _, value := range []string{"", "0", "-1", "3601", "1.5", "ten"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue(key, value))
		})
	}
	for _, value := range []string{"1", "10", "3600"} {
		require.NoError(t, validateOptionValue(key, value))
	}
}

func TestValidateOptionValueGroupAccessRules(t *testing.T) {
	const key = "group_access_setting.rules"
	for name, value := range map[string]string{
		"not an array":     `{"group":"svip"}`,
		"empty group":      `[{"group":" ","min_topup":1}]`,
		"auto group":       `[{"group":"auto","min_topup":1}]`,
		"duplicate group":  `[{"group":"svip","min_topup":1},{"group":"svip","min_topup":2}]`,
		"negative minimum": `[{"group":"svip","min_topup":-1}]`,
		"minimum too high": `[{"group":"svip","min_topup":1000000001}]`,
		"unresolved user":  `[{"group":"svip","min_topup":1,"users":[{"username":"alice"}]}]`,
		"zero user id":     `[{"group":"svip","min_topup":1,"users":[{"id":0,"username":"alice"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateOptionValue(key, value))
		})
	}
	require.NoError(t, validateOptionValue(key, `[{"group":"svip","min_topup":0,"users":[{"id":3,"username":"alice"}]}]`))
	require.NoError(t, validateOptionValue(key, `[]`))
	assert.Error(t, validateOptionValue("group_access_setting.count_redemption", "yes"))
	require.NoError(t, validateOptionValue("group_access_setting.count_redemption", "false"))
}

func TestValidateOptionValueModelSquareSetting(t *testing.T) {
	const key = "model_square_setting.variant_rules"
	rules := make([]operation_setting.ModelVariantRule, 101)
	for i := range rules {
		rules[i] = operation_setting.ModelVariantRule{Suffix: fmt.Sprintf("-v%d", i), Label: "v"}
	}
	tooMany, err := common.Marshal(rules)
	require.NoError(t, err)
	maxRules, err := common.Marshal(rules[:100])
	require.NoError(t, err)

	for name, value := range map[string]string{
		"empty value":          "",
		"null":                 "null",
		"not an array":         `{"suffix":"-high","label":"high"}`,
		"rule not an object":   `["-high"]`,
		"empty suffix":         `[{"suffix":"","label":"high"}]`,
		"suffix with space":    `[{"suffix":"-thinking high","label":"high"}]`,
		"suffix too long":      `[{"suffix":"-` + strings.Repeat("a", 64) + `","label":"a"}]`,
		"duplicate suffix":     `[{"suffix":"-high","label":"high"},{"suffix":"-high","label":"高"}]`,
		"blank label":          `[{"suffix":"-high","label":"  "}]`,
		"label too long":       `[{"suffix":"-high","label":"` + strings.Repeat("高", 65) + `"}]`,
		"description too long": `[{"suffix":"-high","label":"high","description":"` + strings.Repeat("思", 201) + `"}]`,
		"too many rules":       string(tooMany),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateOptionValue(key, value))
		})
	}
	for _, value := range []string{
		`[]`,
		`[{"suffix":"-high","label":"high"}]`,
		`[{"suffix":"-` + strings.Repeat("a", 63) + `","label":"` + strings.Repeat("高", 64) + `","description":"` + strings.Repeat("思", 200) + `"}]`,
		string(maxRules),
		config.GlobalConfig.ExportAllConfigs()[key],
	} {
		require.NoError(t, validateOptionValue(key, value))
	}
	assert.Error(t, validateOptionValue("model_square_setting.variant_grouping_enabled", "yes"))
	require.NoError(t, validateOptionValue("model_square_setting.variant_grouping_enabled", "false"))
}

func TestValidateOptionValueModelSquareFamilyBases(t *testing.T) {
	const key = operation_setting.ModelSquareFamilyBasesOptionKey
	bases := make([]string, 501)
	for i := range bases {
		bases[i] = fmt.Sprintf("model-%d", i)
	}
	tooMany, err := common.Marshal(bases)
	require.NoError(t, err)
	maxBases, err := common.Marshal(bases[:500])
	require.NoError(t, err)

	for name, value := range map[string]string{
		"empty value":     "",
		"null":            "null",
		"not an array":    `"gemini-4-argon"`,
		"not strings":     `[{"base":"gemini-4-argon"}]`,
		"empty base":      `[""]`,
		"base with space": `["gemini 4"]`,
		"line tag":        `["[按次]gemini-4-argon"]`,
		"trailing dash":   `["gemini-4-argon-"]`,
		"too long":        `["` + strings.Repeat("a", 129) + `"]`,
		"duplicate":       `["gemini-4-argon","gemini-4-argon"]`,
		"too many":        string(tooMany),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateOptionValue(key, value))
		})
	}
	for _, value := range []string{
		`[]`,
		`["gemini-4-argon","gemini-4"]`,
		`["` + strings.Repeat("模", 128) + `"]`,
		string(maxBases),
		config.GlobalConfig.ExportAllConfigs()[key],
	} {
		require.NoError(t, validateOptionValue(key, value))
	}
}

func TestUpdateOptionMapAppliesModelSquareSetting(t *testing.T) {
	const enabledKey = operation_setting.ModelSquareVariantGroupingEnabledOptionKey
	const rulesKey = operation_setting.ModelSquareVariantRulesOptionKey
	const basesKey = operation_setting.ModelSquareFamilyBasesOptionKey
	const rules = `[{"suffix":"-fast","label":"fast"}]`
	const bases = `["gemini-4-argon"]`
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		common.OptionMap = originalMap
		operation_setting.LoadModelSquareOption(enabledKey, previousConfig[enabledKey])
		operation_setting.LoadModelSquareOption(rulesKey, previousConfig[rulesKey])
		operation_setting.LoadModelSquareOption(basesKey, previousConfig[basesKey])
	})

	require.NoError(t, updateOptionMap(enabledKey, "false"))
	require.NoError(t, updateOptionMap(rulesKey, rules))
	require.NoError(t, updateOptionMap(basesKey, bases))

	assert.Equal(t, operation_setting.ModelSquareDisplay{
		VariantGroupingEnabled: false,
		VariantRules:           []operation_setting.ModelVariantRule{{Suffix: "-fast", Label: "fast"}},
		FamilyBases:            []string{"gemini-4-argon"},
	}, operation_setting.GetModelSquareDisplay())
	assert.Equal(t, rules, config.GlobalConfig.ExportAllConfigs()[rulesKey])
	assert.Equal(t, bases, config.GlobalConfig.ExportAllConfigs()[basesKey])
}

func TestValidateOptionValueBonusSettings(t *testing.T) {
	invalid := map[string][]string{
		"checkin_setting.paid_threshold_quota":    {"", "-1", "1.5", fmt.Sprint(int64(common.MaxWalletQuota) + 1)},
		"InviteRechargeThresholdQuota":            {"-1", "abc"},
		"checkin_setting.paid_multiplier":         {"", "0.5", "100.5", "NaN", "Inf", "two", "1.125", "1.001"},
		"checkin_setting.free_expire_days":        {"-1", "3651", "1.5"},
		"checkin_setting.paid_expire_days":        {"3651"},
		"InviteRewardExpireDays":                  {"-1", "x"},
		"checkin_setting.expire_only_when_unused": {"yes", "1", ""},
		"InviteRewardExpireOnlyWhenUnused":        {"TRUE"},
		"InviteAbuseBanEnabled":                   {"yes", "1", ""},
		"InviteAbuseBanThreshold":                 {"", "0", "100001", "1.5", "five"},
		"InviteAbuseBanMinRedeemQuota":            {"", "-1", "1.5", fmt.Sprint(int64(common.MaxWalletQuota) + 1)},
		"InviteAbuseBanExemptPaidInviter":         {"yes", "1", "", "TRUE"},
	}
	for key, values := range invalid {
		for _, value := range values {
			assert.Error(t, validateOptionValue(key, value), "%s=%q", key, value)
		}
	}
	valid := map[string][]string{
		"checkin_setting.paid_threshold_quota":    {"0", "1000000", fmt.Sprint(int64(common.MaxWalletQuota))},
		"InviteRechargeThresholdQuota":            {"0", "500000"},
		"checkin_setting.paid_multiplier":         {"1", "1.5", "1.25", "1.15", "100"},
		"checkin_setting.free_expire_days":        {"0", "30", "3650"},
		"InviteRewardExpireDays":                  {"0", "3650"},
		"checkin_setting.expire_only_when_unused": {"true", "false"},
		"InviteRewardExpireOnlyWhenUnused":        {"true", "false"},
		"InviteAbuseBanEnabled":                   {"true", "false"},
		"InviteAbuseBanThreshold":                 {"1", "5", "100000"},
		"InviteAbuseBanMinRedeemQuota":            {"0", "500000", fmt.Sprint(int64(common.MaxWalletQuota))},
		"InviteAbuseBanExemptPaidInviter":         {"true", "false"},
	}
	for key, values := range valid {
		for _, value := range values {
			assert.NoError(t, validateOptionValue(key, value), "%s=%q", key, value)
		}
	}
	assert.EqualError(t, validateOptionValue("InviteAbuseBanThreshold", "0"), "Invite count threshold must be an integer between 1 and 100000")
	assert.EqualError(t, validateOptionValue("InviteAbuseBanMinRedeemQuota", "-1"), "Minimum redeemed quota must be a non-negative integer")
	assert.EqualError(t, validateOptionValue("InviteAbuseBanExemptPaidInviter", "yes"), "Value must be true or false")
}

// The invite reward options are plain options and the check-in fields are
// read through the config manager, so a saved value takes effect at once.
func TestBonusOptionsApplyWhenSaved(t *testing.T) {
	threshold, days, sliding := common.InviteRechargeThresholdQuota, common.InviteRewardExpireDays, common.InviteRewardExpireOnlyWhenUnused
	abuseEnabled, abuseThreshold, abuseMinRedeem := common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota
	abuseExemptPaid := common.InviteAbuseBanExemptPaidInviter
	checkin := *operation_setting.GetCheckinSetting()
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.OptionMap = originalMap
		common.InviteRechargeThresholdQuota, common.InviteRewardExpireDays, common.InviteRewardExpireOnlyWhenUnused = threshold, days, sliding
		common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold, common.InviteAbuseBanMinRedeemQuota = abuseEnabled, abuseThreshold, abuseMinRedeem
		common.InviteAbuseBanExemptPaidInviter = abuseExemptPaid
		*operation_setting.GetCheckinSetting() = checkin
	})

	for key, value := range map[string]string{
		"InviteRechargeThresholdQuota":            "500000",
		"InviteRewardExpireDays":                  "7",
		"InviteRewardExpireOnlyWhenUnused":        "true",
		"checkin_setting.paid_multiplier":         "1.5",
		"checkin_setting.paid_threshold_quota":    "0",
		"checkin_setting.paid_expire_days":        "14",
		"checkin_setting.expire_only_when_unused": "true",
		"InviteAbuseBanEnabled":                   "true",
		"InviteAbuseBanThreshold":                 "8",
		"InviteAbuseBanMinRedeemQuota":            "250000",
		"InviteAbuseBanExemptPaidInviter":         "false",
	} {
		require.NoError(t, updateOptionMap(key, value))
	}

	assert.Equal(t, 500000, common.InviteRechargeThresholdQuota)
	assert.Equal(t, 7, common.InviteRewardExpireDays)
	assert.True(t, common.InviteRewardExpireOnlyWhenUnused)
	assert.True(t, common.InviteAbuseBanEnabled)
	assert.Equal(t, InviteAbuseSettings{Threshold: 8, MinRedeemQuota: 250000, ExemptPaidInviter: false}, CurrentInviteAbuseSettings())
	setting := operation_setting.GetCheckinSetting()
	assert.Equal(t, 1.5, setting.PaidMultiplier)
	assert.Zero(t, setting.PaidThresholdQuota)
	assert.Equal(t, 14, operation_setting.CheckinExpireDays(true))
	assert.True(t, setting.ExpireOnlyWhenUnused)
	assert.True(t, operation_setting.IsPaidCheckinUser(1), "a threshold of 0 means any recharge")
	assert.False(t, operation_setting.IsPaidCheckinUser(0))
	exported := config.GlobalConfig.ExportAllConfigs()
	assert.Equal(t, "1.5", exported["checkin_setting.paid_multiplier"])
	assert.Equal(t, "true", exported["checkin_setting.expire_only_when_unused"])
}

// The registration anti-abuse options are plain options: they are validated
// before they are saved and take effect as soon as they are applied.
func TestRegisterProtectionOptionsValidateAndApply(t *testing.T) {
	assert.False(t, common.RegisterIPRateLimitEnabled, "the per-network limit stays off until an administrator turns it on")
	invalid := map[string][]string{
		"RegisterIPRateLimitNum":     {"", "0", "-1", "1001", "1.5", "five"},
		"RegisterIPRateLimitMinutes": {"", "0", "1441", "15m"},
		"RegisterIPRateLimitEnabled": {"", "yes"},
		"RegisterCaptchaEnabled":     {"1"},
		"EmailQQNumericOnlyEnabled":  {"TRUE"},
	}
	for key, values := range invalid {
		for _, value := range values {
			assert.Error(t, validateOptionValue(key, value), "%s=%q", key, value)
		}
	}
	assert.EqualError(t, validateOptionValue("RegisterIPRateLimitNum", "0"), "Registration rate limit count must be an integer between 1 and 1000")
	assert.EqualError(t, validateOptionValue("RegisterIPRateLimitMinutes", "1441"), "Registration rate limit window must be an integer between 1 and 1440 minutes")
	valid := map[string][]string{
		"RegisterIPRateLimitNum":     {"1", "5", "1000"},
		"RegisterIPRateLimitMinutes": {"1", "15", "1440"},
		"RegisterIPRateLimitEnabled": {"true", "false"},
		"RegisterCaptchaEnabled":     {"true", "false"},
		"EmailQQNumericOnlyEnabled":  {"true", "false"},
	}
	for key, values := range valid {
		for _, value := range values {
			assert.NoError(t, validateOptionValue(key, value), "%s=%q", key, value)
		}
	}

	captcha, qq, limitEnabled := common.RegisterCaptchaEnabled, common.EmailQQNumericOnlyEnabled, common.RegisterIPRateLimitEnabled
	limitNum, limitMinutes := common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.OptionMap = originalMap
		common.RegisterCaptchaEnabled, common.EmailQQNumericOnlyEnabled, common.RegisterIPRateLimitEnabled = captcha, qq, limitEnabled
		common.RegisterIPRateLimitNum, common.RegisterIPRateLimitMinutes = limitNum, limitMinutes
	})
	for key, value := range map[string]string{
		"RegisterCaptchaEnabled":     "false",
		"EmailQQNumericOnlyEnabled":  "false",
		"RegisterIPRateLimitEnabled": "true",
		"RegisterIPRateLimitNum":     "8",
		"RegisterIPRateLimitMinutes": "30",
	} {
		require.NoError(t, updateOptionMap(key, value))
	}
	assert.False(t, common.RegisterCaptchaEnabled)
	assert.False(t, common.EmailQQNumericOnlyEnabled)
	assert.True(t, common.RegisterIPRateLimitEnabled)
	assert.Equal(t, 8, common.RegisterIPRateLimitNum)
	assert.Equal(t, 30, common.RegisterIPRateLimitMinutes)
	assert.Equal(t, "30", common.OptionMap["RegisterIPRateLimitMinutes"])
}

// The password reset and login protections are on by default, validated
// before they are saved and applied at once.
func TestPasswordResetAndLoginProtectionOptionsValidateAndApply(t *testing.T) {
	assert.True(t, common.PasswordResetCaptchaEnabled)
	assert.True(t, common.PasswordResetEmailLimitEnabled)
	assert.Equal(t, 120, common.PasswordResetEmailCooldownSeconds)
	assert.Equal(t, 5, common.PasswordResetEmailDailyMax)
	assert.True(t, common.LoginCaptchaEnabled)
	assert.True(t, common.LoginLockoutEnabled)
	assert.Equal(t, 10, common.LoginLockoutMaxFailures)
	assert.Equal(t, 15, common.LoginLockoutMinutes)

	const (
		boolMessage     = "Value must be true or false"
		cooldownMessage = "Password reset email cooldown must be an integer between 30 and 3600 seconds"
		dailyMessage    = "Password reset email limit must be an integer between 1 and 100 per day"
		failureMessage  = "Login lockout failure count must be an integer between 3 and 100"
		minutesMessage  = "Login lockout duration must be an integer between 1 and 1440 minutes"
	)
	invalid := []struct{ key, value, message string }{
		{"PasswordResetCaptchaEnabled", "yes", boolMessage},
		{"PasswordResetEmailLimitEnabled", "", boolMessage},
		{"LoginCaptchaEnabled", "1", boolMessage},
		{"LoginLockoutEnabled", "TRUE", boolMessage},
		{"PasswordResetEmailCooldownSeconds", "29", cooldownMessage},
		{"PasswordResetEmailCooldownSeconds", "3601", cooldownMessage},
		{"PasswordResetEmailCooldownSeconds", "1.5", cooldownMessage},
		{"PasswordResetEmailDailyMax", "0", dailyMessage},
		{"PasswordResetEmailDailyMax", "101", dailyMessage},
		{"PasswordResetEmailDailyMax", "", dailyMessage},
		{"LoginLockoutMaxFailures", "2", failureMessage},
		{"LoginLockoutMaxFailures", "101", failureMessage},
		{"LoginLockoutMaxFailures", "ten", failureMessage},
		{"LoginLockoutMinutes", "0", minutesMessage},
		{"LoginLockoutMinutes", "1441", minutesMessage},
		{"LoginLockoutMinutes", "15m", minutesMessage},
	}
	for _, tc := range invalid {
		assert.EqualError(t, validateOptionValue(tc.key, tc.value), tc.message, "%s=%q", tc.key, tc.value)
	}
	for key, values := range map[string][]string{
		"PasswordResetCaptchaEnabled":       {"true", "false"},
		"PasswordResetEmailLimitEnabled":    {"true", "false"},
		"LoginCaptchaEnabled":               {"true", "false"},
		"LoginLockoutEnabled":               {"true", "false"},
		"PasswordResetEmailCooldownSeconds": {"30", "120", "3600"},
		"PasswordResetEmailDailyMax":        {"1", "5", "100"},
		"LoginLockoutMaxFailures":           {"3", "10", "100"},
		"LoginLockoutMinutes":               {"1", "15", "1440"},
	} {
		for _, value := range values {
			assert.NoError(t, validateOptionValue(key, value), "%s=%q", key, value)
		}
	}

	previous := []any{common.PasswordResetCaptchaEnabled, common.PasswordResetEmailLimitEnabled, common.PasswordResetEmailCooldownSeconds,
		common.PasswordResetEmailDailyMax, common.LoginCaptchaEnabled, common.LoginLockoutEnabled, common.LoginLockoutMaxFailures, common.LoginLockoutMinutes}
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.OptionMap = originalMap
		common.PasswordResetCaptchaEnabled, common.PasswordResetEmailLimitEnabled = previous[0].(bool), previous[1].(bool)
		common.PasswordResetEmailCooldownSeconds, common.PasswordResetEmailDailyMax = previous[2].(int), previous[3].(int)
		common.LoginCaptchaEnabled, common.LoginLockoutEnabled = previous[4].(bool), previous[5].(bool)
		common.LoginLockoutMaxFailures, common.LoginLockoutMinutes = previous[6].(int), previous[7].(int)
	})
	saved := map[string]string{
		"PasswordResetCaptchaEnabled":       "false",
		"PasswordResetEmailLimitEnabled":    "false",
		"PasswordResetEmailCooldownSeconds": "300",
		"PasswordResetEmailDailyMax":        "3",
		"LoginCaptchaEnabled":               "false",
		"LoginLockoutEnabled":               "false",
		"LoginLockoutMaxFailures":           "20",
		"LoginLockoutMinutes":               "60",
	}
	for key, value := range saved {
		require.NoError(t, updateOptionMap(key, value))
	}
	assert.False(t, common.PasswordResetCaptchaEnabled)
	assert.False(t, common.PasswordResetEmailLimitEnabled)
	assert.Equal(t, 300, common.PasswordResetEmailCooldownSeconds)
	assert.Equal(t, 3, common.PasswordResetEmailDailyMax)
	assert.False(t, common.LoginCaptchaEnabled)
	assert.False(t, common.LoginLockoutEnabled)
	assert.Equal(t, 20, common.LoginLockoutMaxFailures)
	assert.Equal(t, 60, common.LoginLockoutMinutes)
	for key, value := range saved {
		assert.Equal(t, value, common.OptionMap[key], key)
	}
}

func TestValidateOptionValueRejectsSystemTaskRetentionBelowOneDay(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue("system_task_setting.retention_days", value))
		})
	}
	require.NoError(t, validateOptionValue("system_task_setting.retention_days", "1"))
	require.NoError(t, validateOptionValue("system_task_setting.retention_days", "30"))
}
