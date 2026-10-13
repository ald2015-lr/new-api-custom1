package operation_setting

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// ---------------------------------------------------------------------------
// Model square display (root-admin configurable)
// DB keys: model_square_setting.variant_grouping_enabled,
//          model_square_setting.variant_rules (JSON array of variant rules)
//
// The public model square groups models such as "[按次]gemini-3-pro-preview"
// and "[按次]gemini-3-pro-preview-128-nothinking" into one family card. The
// rules name the suffixes that mark a variant; the frontend strips them to
// find the family. Display only: model names used for calls and billing are
// never changed.
// ---------------------------------------------------------------------------

const (
	ModelSquareVariantGroupingEnabledOptionKey = "model_square_setting.variant_grouping_enabled"
	ModelSquareVariantRulesOptionKey           = "model_square_setting.variant_rules"

	maxModelVariantRules             = 100
	maxModelVariantSuffixLength      = 64
	maxModelVariantLabelLength       = 64
	maxModelVariantDescriptionLength = 200
)

// ModelVariantRule maps a model name suffix to the variant label shown in the
// model square. Rule order is the display order of variants.
type ModelVariantRule struct {
	Suffix      string `json:"suffix"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ModelSquareSetting is registered with the config manager so the options are
// exported to the option map. VariantRules stays a JSON string so GET
// /api/option returns it verbatim; reads go through the parsed snapshot below.
type ModelSquareSetting struct {
	VariantGroupingEnabled bool   `json:"variant_grouping_enabled"`
	VariantRules           string `json:"variant_rules"`
}

// ModelSquareDisplay is the parsed model square configuration in effect, as
// served by the public pricing endpoint.
type ModelSquareDisplay struct {
	VariantGroupingEnabled bool               `json:"variant_grouping_enabled"`
	VariantRules           []ModelVariantRule `json:"variant_rules"`
}

// defaultModelVariantRules leaves out generic effort suffixes such as -max or
// -high: they fold distinct models (gpt-5.1-codex-max, qwen-max). Admins can
// still add them. Keep in sync with DEFAULT_MODEL_VARIANT_RULES in
// web/src/features/pricing/lib/model-variants.ts.
var defaultModelVariantRules = []ModelVariantRule{
	{Suffix: "-thinking", Label: "thinking"},
	{Suffix: "-thinking-minimal", Label: "thinking-minimal"},
	{Suffix: "-thinking-low", Label: "thinking-low"},
	{Suffix: "-thinking-medium", Label: "thinking-medium"},
	{Suffix: "-thinking-high", Label: "thinking-high"},
	{Suffix: "-thinking-xhigh", Label: "thinking-xhigh"},
	{Suffix: "-thinking-max", Label: "thinking-max"},
	{Suffix: "-nothinking", Label: "nothinking", Description: "会思考，但不返回思考内容"},
	{Suffix: "-128", Label: "128", Description: "思考预算 128（最低思考强度）"},
}

var modelSquareSetting = ModelSquareSetting{VariantGroupingEnabled: true}

// modelSquareDisplay is replaced atomically so the public pricing endpoint
// reads a consistent, already parsed configuration without locking. Its rule
// slice is never modified after it is stored.
var modelSquareDisplay atomic.Pointer[ModelSquareDisplay]

func init() {
	// An empty value reads back as the defaults, so a failed encode is harmless.
	if defaultRules, err := common.Marshal(defaultModelVariantRules); err == nil {
		modelSquareSetting.VariantRules = string(defaultRules)
	}
	config.GlobalConfig.Register("model_square_setting", &modelSquareSetting)
	modelSquareDisplay.Store(&ModelSquareDisplay{
		VariantGroupingEnabled: modelSquareSetting.VariantGroupingEnabled,
		VariantRules:           defaultModelVariantRules,
	})
}

// ParseModelVariantRules decodes and validates a variant rule list; it touches
// no global state. An empty array is valid: no suffix is folded, but the lines
// ([..] tags) of the same model are still grouped. variant_grouping_enabled
// turns grouping off.
func ParseModelVariantRules(value string) ([]ModelVariantRule, error) {
	if common.GetJsonType([]byte(value)) != "array" {
		return nil, fmt.Errorf("模型变体规则必须是 JSON 数组")
	}
	rules := []ModelVariantRule{}
	if err := common.UnmarshalJsonStr(value, &rules); err != nil {
		return nil, fmt.Errorf("模型变体规则必须是由 suffix、label、description 组成的对象数组: %w", err)
	}
	if len(rules) > maxModelVariantRules {
		return nil, fmt.Errorf("模型变体规则最多 %d 条", maxModelVariantRules)
	}
	suffixes := make([]string, 0, len(rules))
	for i, rule := range rules {
		position := i + 1
		if rule.Suffix == "" {
			return nil, fmt.Errorf("第 %d 条模型变体规则的后缀不能为空", position)
		}
		if utf8.RuneCountInString(rule.Suffix) > maxModelVariantSuffixLength {
			return nil, fmt.Errorf("第 %d 条模型变体规则的后缀不能超过 %d 个字符", position, maxModelVariantSuffixLength)
		}
		if strings.ContainsFunc(rule.Suffix, unicode.IsSpace) {
			return nil, fmt.Errorf("第 %d 条模型变体规则的后缀不能包含空白字符", position)
		}
		if slices.Contains(suffixes, rule.Suffix) {
			return nil, fmt.Errorf("模型变体规则的后缀 %q 重复", rule.Suffix)
		}
		suffixes = append(suffixes, rule.Suffix)
		if strings.TrimSpace(rule.Label) == "" {
			return nil, fmt.Errorf("第 %d 条模型变体规则的显示名不能为空", position)
		}
		if utf8.RuneCountInString(rule.Label) > maxModelVariantLabelLength {
			return nil, fmt.Errorf("第 %d 条模型变体规则的显示名不能超过 %d 个字符", position, maxModelVariantLabelLength)
		}
		if utf8.RuneCountInString(rule.Description) > maxModelVariantDescriptionLength {
			return nil, fmt.Errorf("第 %d 条模型变体规则的说明不能超过 %d 个字符", position, maxModelVariantDescriptionLength)
		}
	}
	return rules, nil
}

func IsModelSquareOptionKey(key string) bool {
	return key == ModelSquareVariantGroupingEnabledOptionKey || key == ModelSquareVariantRulesOptionKey
}

// ValidateModelSquareOption validates a value before it is stored.
func ValidateModelSquareOption(key string, value string) error {
	switch key {
	case ModelSquareVariantGroupingEnabledOptionKey:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("是否合并模型变体必须是 true 或 false")
		}
	case ModelSquareVariantRulesOptionKey:
		_, err := ParseModelVariantRules(value)
		return err
	}
	return nil
}

// LoadModelSquareOption applies a stored value and publishes a new snapshot.
// An invalid value is logged and replaced by the built-in default. Writes must
// be serialized by the caller; the option map update holds
// common.OptionMapRWMutex.
func LoadModelSquareOption(key string, value string) {
	next := *modelSquareDisplay.Load()
	switch key {
	case ModelSquareVariantGroupingEnabledOptionKey:
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			common.SysError("加载模型广场变体合并开关失败，使用默认值（开启）: " + err.Error())
			enabled = true
		}
		modelSquareSetting.VariantGroupingEnabled = enabled
		next.VariantGroupingEnabled = enabled
	case ModelSquareVariantRulesOptionKey:
		rules, err := ParseModelVariantRules(value)
		if err != nil {
			common.SysError("加载模型变体规则失败，使用内置默认规则: " + err.Error())
			rules = defaultModelVariantRules
		}
		modelSquareSetting.VariantRules = value
		next.VariantRules = rules
	default:
		return
	}
	modelSquareDisplay.Store(&next)
}

// GetModelSquareDisplay returns the model square configuration in effect. The
// returned rule slice is a copy.
func GetModelSquareDisplay() ModelSquareDisplay {
	display := *modelSquareDisplay.Load()
	display.VariantRules = slices.Clone(display.VariantRules)
	return display
}
