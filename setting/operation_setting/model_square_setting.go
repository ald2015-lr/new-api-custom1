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
//          model_square_setting.version_spelling_fold_enabled,
//          model_square_setting.snapshot_fold_enabled,
//          model_square_setting.variant_rules (JSON array of variant rules),
//          model_square_setting.family_bases (JSON array of base model names),
//          model_square_setting.show_group_filter, .show_vendor_filter,
//          .show_tag_filter, .show_quota_type_filter, .show_endpoint_type_filter
// and upstream's Recommended order (model_square_setting.sort_mode,
// .sort_metric, .sort_days, .sort_models; GetModelSquareSetting), which the
// generic config path loads: IsModelSquareOptionKey covers only the display
// options above.
//
// The public model square groups models such as "[按次]gemini-3-pro-preview"
// and "[按次]gemini-3-pro-preview-128-nothinking" into one family card. The
// rules name the suffixes that mark a variant; the frontend strips them to
// find the family. Family bases name base models outright: every model named
// "<base>-<anything>" joins that base's card, so suffixes no rule covers
// (gemini-4-argon-low, -medium) still fold. While grouping is on, two more
// folds join names into one family: version spellings (claude-opus-4-5 and
// claude-opus-4.5, letter case, one leading "org/" segment) and dated
// snapshots (gpt-4o-mini-2024-07-18 and tts-1-1106 join gpt-4o-mini and
// tts-1, the date becomes the variant). The show_* switches show or hide each
// section of the filter sidebar (and the mobile filter drawer); a hidden
// section's filter is not applied. Tags and endpoint types are hidden unless
// an admin turns them on. Display only: model names used for calls and
// billing are never changed.
// ---------------------------------------------------------------------------

const (
	ModelSquareVariantGroupingEnabledOptionKey = "model_square_setting.variant_grouping_enabled"
	ModelSquareVersionSpellingFoldOptionKey    = "model_square_setting.version_spelling_fold_enabled"
	ModelSquareSnapshotFoldOptionKey           = "model_square_setting.snapshot_fold_enabled"
	ModelSquareVariantRulesOptionKey           = "model_square_setting.variant_rules"
	ModelSquareFamilyBasesOptionKey            = "model_square_setting.family_bases"
	ModelSquareShowGroupFilterOptionKey        = "model_square_setting.show_group_filter"
	ModelSquareShowVendorFilterOptionKey       = "model_square_setting.show_vendor_filter"
	ModelSquareShowTagFilterOptionKey          = "model_square_setting.show_tag_filter"
	ModelSquareShowQuotaTypeFilterOptionKey    = "model_square_setting.show_quota_type_filter"
	ModelSquareShowEndpointTypeFilterOptionKey = "model_square_setting.show_endpoint_type_filter"

	maxModelVariantRules             = 100
	maxModelVariantSuffixLength      = 64
	maxModelVariantLabelLength       = 64
	maxModelVariantDescriptionLength = 200
	maxModelFamilyBases              = 500
	maxModelFamilyBaseLength         = 128
)

const (
	ModelSquareSortManual = "manual"
	ModelSquareSortUsage  = "usage"
	ModelSquareSortMixed  = "mixed"

	ModelSquareMetricRequests = "requests"
	ModelSquareMetricTokens   = "tokens"
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
// The Sort* fields are upstream's Recommended order of the model square; the
// config manager loads them and GetModelSquareSetting reads them.
type ModelSquareSetting struct {
	VariantGroupingEnabled     bool   `json:"variant_grouping_enabled"`
	VersionSpellingFoldEnabled bool   `json:"version_spelling_fold_enabled"`
	SnapshotFoldEnabled        bool   `json:"snapshot_fold_enabled"`
	VariantRules               string `json:"variant_rules"`
	FamilyBases                string `json:"family_bases"`
	ShowGroupFilter            bool   `json:"show_group_filter"`
	ShowVendorFilter           bool   `json:"show_vendor_filter"`
	ShowTagFilter              bool   `json:"show_tag_filter"`
	ShowQuotaTypeFilter        bool   `json:"show_quota_type_filter"`
	ShowEndpointTypeFilter     bool   `json:"show_endpoint_type_filter"`

	SortMode   string   `json:"sort_mode"`   // manual, usage or mixed
	SortMetric string   `json:"sort_metric"` // usage metric: requests or tokens
	SortDays   int      `json:"sort_days"`   // usage period in days: 1, 7 or 30
	SortModels []string `json:"sort_models"` // manual order, or the pinned models in mixed mode
}

// ModelSquareFilterSections says which sections the model square's filter
// sidebar and mobile filter drawer show. A hidden section is not rendered and
// its filter is not applied.
type ModelSquareFilterSections struct {
	Groups        bool `json:"groups"`
	Vendors       bool `json:"vendors"`
	Tags          bool `json:"tags"`
	QuotaTypes    bool `json:"quota_types"`
	EndpointTypes bool `json:"endpoint_types"`
}

// ModelSquareDisplay is the parsed model square configuration in effect, as
// served by the public pricing endpoint.
type ModelSquareDisplay struct {
	VariantGroupingEnabled     bool                      `json:"variant_grouping_enabled"`
	VersionSpellingFoldEnabled bool                      `json:"version_spelling_fold_enabled"`
	SnapshotFoldEnabled        bool                      `json:"snapshot_fold_enabled"`
	VariantRules               []ModelVariantRule        `json:"variant_rules"`
	FamilyBases                []string                  `json:"family_bases"`
	FilterSections             ModelSquareFilterSections `json:"filter_sections"`
}

// modelSquareBoolOption describes one switch of the model square settings, so
// validation, loading and the defaults cannot drift apart.
type modelSquareBoolOption struct {
	defaultValue bool
	label        string // validation: label + "必须是 true 或 false"
	loadName     string // load error: "加载" + loadName + "失败，使用默认值（开启|关闭）: " + err
	apply        func(setting *ModelSquareSetting, display *ModelSquareDisplay, value bool)
}

// modelSquareBoolOptions holds every switch. A key without a stored row keeps
// its default, so an upgraded site gets these defaults too.
var modelSquareBoolOptions = map[string]modelSquareBoolOption{
	ModelSquareVariantGroupingEnabledOptionKey: {true, "是否合并模型变体", "模型广场变体合并开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.VariantGroupingEnabled = v
		d.VariantGroupingEnabled = v
	}},
	ModelSquareVersionSpellingFoldOptionKey: {true, "是否合并版本号写法", "模型广场版本号写法合并开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.VersionSpellingFoldEnabled = v
		d.VersionSpellingFoldEnabled = v
	}},
	ModelSquareSnapshotFoldOptionKey: {true, "是否合并带日期的快照", "模型广场日期快照合并开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.SnapshotFoldEnabled = v
		d.SnapshotFoldEnabled = v
	}},
	ModelSquareShowGroupFilterOptionKey: {true, "是否显示分组筛选", "模型广场分组筛选显示开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.ShowGroupFilter = v
		d.FilterSections.Groups = v
	}},
	ModelSquareShowVendorFilterOptionKey: {true, "是否显示供应商筛选", "模型广场供应商筛选显示开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.ShowVendorFilter = v
		d.FilterSections.Vendors = v
	}},
	ModelSquareShowTagFilterOptionKey: {false, "是否显示模型标签筛选", "模型广场模型标签筛选显示开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.ShowTagFilter = v
		d.FilterSections.Tags = v
	}},
	ModelSquareShowQuotaTypeFilterOptionKey: {true, "是否显示定价类型筛选", "模型广场定价类型筛选显示开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.ShowQuotaTypeFilter = v
		d.FilterSections.QuotaTypes = v
	}},
	ModelSquareShowEndpointTypeFilterOptionKey: {false, "是否显示端点类型筛选", "模型广场端点类型筛选显示开关", func(s *ModelSquareSetting, d *ModelSquareDisplay, v bool) {
		s.ShowEndpointTypeFilter = v
		d.FilterSections.EndpointTypes = v
	}},
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

// modelSquareSetting gets the switches' defaults from modelSquareBoolOptions
// in init. An empty manual list keeps the model square in name order.
var modelSquareSetting = ModelSquareSetting{
	FamilyBases: "[]",
	SortMode:    ModelSquareSortManual,
	SortMetric:  ModelSquareMetricTokens,
	SortDays:    7,
	SortModels:  []string{},
}

// modelSquareDisplay is replaced atomically so the public pricing endpoint
// reads a consistent, already parsed configuration without locking. Its rule
// slice is never modified after it is stored.
var modelSquareDisplay atomic.Pointer[ModelSquareDisplay]

func init() {
	// An empty value reads back as the defaults, so a failed encode is harmless.
	if defaultRules, err := common.Marshal(defaultModelVariantRules); err == nil {
		modelSquareSetting.VariantRules = string(defaultRules)
	}
	display := ModelSquareDisplay{VariantRules: defaultModelVariantRules, FamilyBases: []string{}}
	for _, option := range modelSquareBoolOptions {
		option.apply(&modelSquareSetting, &display, option.defaultValue)
	}
	config.GlobalConfig.Register("model_square_setting", &modelSquareSetting)
	modelSquareDisplay.Store(&display)
}

// GetModelSquareSetting returns the registered settings; upstream's model
// square order reads its Sort* fields.
func GetModelSquareSetting() *ModelSquareSetting {
	return &modelSquareSetting
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

// ParseModelFamilyBases decodes and validates the base model list; it touches
// no global state. Bases are matched against model names without their [..]
// line tag, so a base must not carry one.
func ParseModelFamilyBases(value string) ([]string, error) {
	if common.GetJsonType([]byte(value)) != "array" {
		return nil, fmt.Errorf("合并的基础模型必须是 JSON 数组")
	}
	bases := []string{}
	if err := common.UnmarshalJsonStr(value, &bases); err != nil {
		return nil, fmt.Errorf("合并的基础模型必须是字符串数组: %w", err)
	}
	if len(bases) > maxModelFamilyBases {
		return nil, fmt.Errorf("合并的基础模型最多 %d 个", maxModelFamilyBases)
	}
	for i, base := range bases {
		position := i + 1
		if base == "" {
			return nil, fmt.Errorf("第 %d 个基础模型不能为空", position)
		}
		if utf8.RuneCountInString(base) > maxModelFamilyBaseLength {
			return nil, fmt.Errorf("第 %d 个基础模型不能超过 %d 个字符", position, maxModelFamilyBaseLength)
		}
		if strings.ContainsFunc(base, unicode.IsSpace) {
			return nil, fmt.Errorf("第 %d 个基础模型不能包含空白字符", position)
		}
		if strings.HasPrefix(base, "[") {
			return nil, fmt.Errorf("第 %d 个基础模型不要带开头的 [..] 线路标签", position)
		}
		if strings.HasSuffix(base, "-") {
			return nil, fmt.Errorf("第 %d 个基础模型不能以 - 结尾", position)
		}
		if slices.Contains(bases[:i], base) {
			return nil, fmt.Errorf("基础模型 %q 重复", base)
		}
	}
	return bases, nil
}

func IsModelSquareOptionKey(key string) bool {
	return IsModelSquareSwitchKey(key) || key == ModelSquareVariantRulesOptionKey || key == ModelSquareFamilyBasesOptionKey
}

// IsModelSquareSwitchKey reports whether key is one of the model square's
// on/off switches, whose values strconv.ParseBool reads.
func IsModelSquareSwitchKey(key string) bool {
	_, ok := modelSquareBoolOptions[key]
	return ok
}

// ValidateModelSquareOption validates a value before it is stored: the
// display options above, then upstream's Recommended order values. Any other
// model_square_setting.* key is refused, as upstream does.
func ValidateModelSquareOption(key string, value string) error {
	if option, ok := modelSquareBoolOptions[key]; ok {
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s必须是 true 或 false", option.label)
		}
		return nil
	}
	switch key {
	case ModelSquareVariantRulesOptionKey:
		_, err := ParseModelVariantRules(value)
		return err
	case ModelSquareFamilyBasesOptionKey:
		_, err := ParseModelFamilyBases(value)
		return err
	}
	setting, ok := strings.CutPrefix(key, "model_square_setting.")
	if !ok {
		return nil
	}
	valid := false
	switch setting {
	case "sort_mode":
		valid = slices.Contains([]string{ModelSquareSortManual, ModelSquareSortUsage, ModelSquareSortMixed}, value)
	case "sort_metric":
		valid = value == ModelSquareMetricRequests || value == ModelSquareMetricTokens
	case "sort_days":
		valid = slices.Contains([]string{"1", "7", "30"}, value)
	case "sort_models":
		var names []string
		valid = common.UnmarshalJsonStr(value, &names) == nil
	}
	if !valid {
		return common.NewMessage("Invalid model square order setting")
	}
	return nil
}

// LoadModelSquareOption applies a stored value and publishes a new snapshot.
// An invalid value is logged and replaced by the built-in default. A switch is
// also written back to the option map as "true" or "false", the state applied.
// Writes must be serialized by the caller; the option map update holds
// common.OptionMapRWMutex.
func LoadModelSquareOption(key string, value string) {
	next := *modelSquareDisplay.Load()
	if option, ok := modelSquareBoolOptions[key]; ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			defaultName := "关闭"
			if option.defaultValue {
				defaultName = "开启"
			}
			common.SysError("加载" + option.loadName + "失败，使用默认值（" + defaultName + "）: " + err.Error())
			enabled = option.defaultValue
		}
		option.apply(&modelSquareSetting, &next, enabled)
		modelSquareDisplay.Store(&next)
		// The settings page reads a switch as on only for "true" or "1", so
		// the option map (GET /api/option/) holds the state applied here,
		// however the row was written: PUT /api/option/ stores "True" or "t"
		// as sent, and other nodes reload the stored row. The caller holds
		// common.OptionMapRWMutex.
		if common.OptionMap != nil {
			common.OptionMap[key] = strconv.FormatBool(enabled)
		}
		return
	}
	switch key {
	case ModelSquareVariantRulesOptionKey:
		rules, err := ParseModelVariantRules(value)
		if err != nil {
			common.SysError("加载模型变体规则失败，使用内置默认规则: " + err.Error())
			rules = defaultModelVariantRules
		}
		modelSquareSetting.VariantRules = value
		next.VariantRules = rules
	case ModelSquareFamilyBasesOptionKey:
		bases, err := ParseModelFamilyBases(value)
		if err != nil {
			common.SysError("加载合并的基础模型失败，按未设置处理: " + err.Error())
			bases = []string{}
		}
		modelSquareSetting.FamilyBases = value
		next.FamilyBases = bases
	default:
		return
	}
	modelSquareDisplay.Store(&next)
}

// GetModelSquareDisplay returns the model square configuration in effect. The
// returned slices are copies.
func GetModelSquareDisplay() ModelSquareDisplay {
	display := *modelSquareDisplay.Load()
	display.VariantRules = slices.Clone(display.VariantRules)
	display.FamilyBases = slices.Clone(display.FamilyBases)
	return display
}
