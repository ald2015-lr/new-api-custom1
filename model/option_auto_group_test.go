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
