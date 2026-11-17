package reasoning

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/samber/lo"
)

var EffortSuffixes = []string{"-max", "-xhigh", "-high", "-medium", "-low", "-minimal"}

var OpenAIEffortSuffixes = []string{"-max", "-xhigh", "-high", "-medium", "-low", "-minimal", "-none"}

var DeepSeekV4EffortSuffixes = []string{"-none", "-low", "-max"}

var (
	legacyOpenAIModelPattern = regexp.MustCompile(`^(gpt-[a-z0-9][a-z0-9._-]*|o[1-9][a-z0-9._-]*)$`)
	legacyClaudeModelPattern = regexp.MustCompile(`^claude-[a-z0-9][a-z0-9._-]*$`)
	legacyGeminiModelPattern = regexp.MustCompile(`^gemini-[a-z0-9][a-z0-9._-]*$`)
)

type ModelModifier struct {
	Key   string
	Value string
}

type ModelModifierSpec struct {
	Raw       string
	Base      string
	Modifiers []ModelModifier
}

func (s ModelModifierSpec) HasModifiers() bool {
	return len(s.Modifiers) > 0
}

// ParseModelModifiers removes only a contiguous trailing chain of @key:value
// segments. Other @ characters remain part of the opaque model name.
func ParseModelModifiers(modelName string) ModelModifierSpec {
	spec := ModelModifierSpec{Raw: modelName, Base: modelName}
	parts := strings.Split(modelName, "@")
	if len(parts) < 2 {
		return spec
	}

	firstModifier := len(parts)
	for i := len(parts) - 1; i > 0; i-- {
		key, value, ok := parseModelModifierSegment(parts[i])
		if !ok {
			break
		}
		firstModifier = i
		spec.Modifiers = append([]ModelModifier{{Key: key, Value: value}}, spec.Modifiers...)
	}
	if firstModifier == len(parts) {
		return spec
	}

	base := strings.Join(parts[:firstModifier], "@")
	if base == "" {
		return ModelModifierSpec{Raw: modelName, Base: modelName}
	}
	spec.Base = base
	return spec
}

func parseModelModifierSegment(segment string) (string, string, bool) {
	colon := strings.IndexByte(segment, ':')
	if colon <= 0 {
		return "", "", false
	}
	key := segment[:colon]
	for i, r := range key {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if i == 0 && !letter || i > 0 && !letter && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "", "", false
		}
	}
	return strings.ToLower(key), segment[colon+1:], true
}

// ParseThinkingModifier maps an explicit @thinking value onto a portable
// Intent. on/adaptive/off and integer budgets (including -1) are accepted;
// values below -1 are rejected.
func ParseThinkingModifier(raw string) (Intent, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "on":
		return Intent{Mode: ModeEnabled, Source: SourceSuffix}, true
	case "adaptive":
		return Intent{Mode: ModeAdaptive, Source: SourceSuffix}, true
	case "off":
		return Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix}, true
	}
	if budget, err := strconv.Atoi(value); err == nil {
		if budget < -1 {
			return Intent{}, false
		}
		if budget == 0 {
			return Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix}, true
		}
		return Intent{
			Mode:         ModeEnabled,
			BudgetTokens: &budget,
			Source:       SourceSuffix,
			BudgetSource: SourceSuffix,
		}, true
	}
	return Intent{}, false
}

func TrimEffortSuffixWithSuffixes(modelName string, suffixes []string) (string, string, bool) {
	suffix, found := lo.Find(suffixes, func(s string) bool {
		return strings.HasSuffix(modelName, s)
	})
	if !found {
		return modelName, "", false
	}
	return strings.TrimSuffix(modelName, suffix), strings.TrimPrefix(suffix, "-"), true
}

// ParseOpenAIReasoningEffortFromModelSuffix extracts an effort tail only from
// GPT and o-series model families. preserveEffortTail is consulted on the
// complete name first so real model IDs that already end in an effort word
// (for example gpt-5.1-codex-max) stay intact.
func ParseOpenAIReasoningEffortFromModelSuffix(modelName string, preserveEffortTail func(string) bool) (string, string) {
	if preserveEffortTail != nil && preserveEffortTail(modelName) {
		return "", modelName
	}
	baseModel, effort, ok := TrimEffortSuffixWithSuffixes(modelName, OpenAIEffortSuffixes)
	if !ok || !legacyOpenAIModelPattern.MatchString(lastModelPathSegment(baseModel)) {
		return "", modelName
	}
	return effort, baseModel
}

// ParseClaudeModelSuffix folds the naked claude-* aliases (-thinking,
// -nothinking, -thinking-<budget>, and effort tails such as -high) into an
// Intent. allowSuffixAlias is the host thinking-adapter toggle; when false the
// name is returned verbatim and no intent is produced.
func ParseClaudeModelSuffix(modelName string, allowSuffixAlias bool) (string, Intent, bool, error) {
	prefix, bare := splitModelNamespace(modelName)
	if !strings.HasPrefix(bare, "claude-") {
		return modelName, Intent{}, false, nil
	}
	base, intent, found, err := parseProviderModelSuffix(bare, "claude-", allowSuffixAlias, true)
	if err != nil || !found || !legacyClaudeModelPattern.MatchString(base) {
		return modelName, Intent{}, false, err
	}
	return prefix + base, intent, true, nil
}

func isKnownClaudeModel(modelName string) bool {
	baseModel, _, _ := TrimEffortSuffixWithSuffixes(modelName, []string{"-max", "-xhigh", "-high", "-medium", "-low", "-minimal", "-none"})
	if marker := strings.LastIndex(baseModel, "-thinking-"); marker >= 0 {
		baseModel = baseModel[:marker]
	} else {
		baseModel = strings.TrimSuffix(strings.TrimSuffix(baseModel, "-thinking"), "-nothinking")
	}
	knownPrefixes := []string{
		"claude-fable-5", "claude-mythos-5", "claude-mythos-preview",
		"claude-opus-5", "claude-sonnet-5", "claude-haiku-5", "claude-opus-4-8",
		"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
		"claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5",
		"claude-opus-4-1", "claude-opus-4-", "claude-sonnet-4-",
		"claude-3-7-sonnet",
	}
	for _, prefix := range knownPrefixes {
		if strings.HasPrefix(baseModel, prefix) {
			return true
		}
	}
	return false
}

// ParseGeminiModelSuffix is the gemini-* counterpart of ParseClaudeModelSuffix
// and honours allowSuffixAlias the same way.
func ParseGeminiModelSuffix(modelName string, allowSuffixAlias bool) (string, Intent, bool, error) {
	prefix, bare := splitModelNamespace(modelName)
	if !strings.HasPrefix(bare, "gemini-") {
		return modelName, Intent{}, false, nil
	}
	base, intent, found, err := parseProviderModelSuffix(bare, "gemini-", allowSuffixAlias, true)
	if err != nil || !found || !legacyGeminiModelPattern.MatchString(base) {
		return modelName, Intent{}, false, err
	}
	return prefix + base, intent, true, nil
}

// ParseKnownProviderModelSuffix extracts a canonical intent only when the
// origin identifies a provider family whose suffix vocabulary is defined by
// relaykit. Unknown OpenAI-compatible model names are deliberately untouched.
func ParseKnownProviderModelSuffix(modelName string, allowSuffixAlias bool) (string, Intent, bool, error) {
	bare := lastModelPathSegment(modelName)
	if strings.HasPrefix(bare, "claude-") {
		return ParseClaudeModelSuffix(modelName, allowSuffixAlias)
	}
	if strings.HasPrefix(bare, "gemini-") {
		return ParseGeminiModelSuffix(modelName, allowSuffixAlias)
	}
	return modelName, Intent{}, false, nil
}

func splitModelNamespace(modelName string) (string, string) {
	if slash := strings.LastIndex(modelName, "/"); slash >= 0 {
		return modelName[:slash+1], modelName[slash+1:]
	}
	return "", modelName
}

func lastModelPathSegment(modelName string) string {
	_, bare := splitModelNamespace(modelName)
	return bare
}

func TrimGeminiThinkingSuffix(modelName string) (string, bool) {
	baseModel, _, ok, err := ParseGeminiModelSuffix(modelName, true)
	return baseModel, ok && err == nil
}

// parseProviderModelSuffix recognises a provider family's naked model-name
// aliases. allowSuffixAlias gates all of them, including effort tails, so a
// disabled thinking adapter leaves the model name untouched.
func parseProviderModelSuffix(modelName string, requiredPrefix string, allowSuffixAlias bool, includeThoughts bool) (string, Intent, bool, error) {
	if !allowSuffixAlias {
		return modelName, Intent{}, false, nil
	}
	if marker := strings.LastIndex(modelName, "-thinking-"); marker >= 0 {
		baseModel := modelName[:marker]
		if !strings.HasPrefix(baseModel, requiredPrefix) {
			return modelName, Intent{}, false, nil
		}
		budget, err := strconv.Atoi(modelName[marker+len("-thinking-"):])
		if err != nil {
			return modelName, Intent{}, false, fmt.Errorf("invalid thinking budget suffix on model %q: %w", modelName, err)
		}
		intent := Intent{BudgetTokens: &budget, Source: SourceSuffix, BudgetSource: SourceSuffix}
		if includeThoughts {
			value := true
			intent.IncludeThoughts = &value
		}
		return baseModel, intent, true, nil
	}
	if baseModel, ok := strings.CutSuffix(modelName, "-nothinking"); ok {
		return baseModel, Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix}, true, nil
	}
	if baseModel, ok := strings.CutSuffix(modelName, "-thinking"); ok {
		intent := Intent{Mode: ModeEnabled, Source: SourceSuffix}
		if includeThoughts {
			value := true
			intent.IncludeThoughts = &value
		}
		return baseModel, intent, true, nil
	}

	suffixes := []string{"-max", "-xhigh", "-high", "-medium", "-low", "-minimal", "-none"}
	baseModel, rawEffort, ok := TrimEffortSuffixWithSuffixes(modelName, suffixes)
	if !ok || !strings.HasPrefix(baseModel, requiredPrefix) {
		return modelName, Intent{}, false, nil
	}
	effort, err := ParseEffort(rawEffort)
	if err != nil {
		return modelName, Intent{}, false, err
	}
	intent := Intent{Effort: effort, Mode: ModeEnabled, Source: SourceSuffix}
	if effort == EffortNone {
		intent.Mode = ModeDisabled
	} else if includeThoughts {
		value := true
		intent.IncludeThoughts = &value
	}
	return baseModel, intent, true, nil
}

// ParseDeepSeekV4ThinkingSuffix maps the -none/-low/-max tails of
// deepseek-flash and deepseek-v4-* onto DeepSeek's thinking switch and
// effort. A bare name keeps DeepSeek's default (thinking on, effort high).
func ParseDeepSeekV4ThinkingSuffix(modelName string) (baseModel string, thinkingType string, effort string, ok bool) {
	baseModel, suffix, ok := TrimEffortSuffixWithSuffixes(modelName, DeepSeekV4EffortSuffixes)
	if !ok || baseModel != "deepseek-flash" && !strings.HasPrefix(baseModel, "deepseek-v4-") {
		return modelName, "", "", false
	}
	switch suffix {
	case "none":
		return baseModel, "disabled", "", true
	case "low", "max":
		return baseModel, "enabled", suffix, true
	default:
		return modelName, "", "", false
	}
}

// ParseGeminiChannelThinkingSuffix parses the thinking-suffix grammar a Gemini
// channel can turn on: <gemini-base>[-<budget>|-<effort>][-nothinking].
//   - -<budget> (for example -128) is a thinking budget in tokens. Models that
//     only take a thinking level get the level that budget maps to (128 is
//     low).
//   - -minimal, -low, -medium, -high, -xhigh and -max are thinking levels;
//     the target model's nearest supported level is used.
//   - A trailing -nothinking keeps thinking on but asks for no thought parts
//     (includeThoughts false). Alone it means the default level, high.
//   - The legacy -thinking-<budget> and -thinking aliases read as -<budget>
//     and as the default level.
//
// Every recognised name thinks with its thoughts returned unless it ends in
// -nothinking. A budget tail is only recognised after a base whose thinking
// is configurable and whose own last segment is not numeric, and never with
// a leading zero, so versioned and dated ids such as gemini-2.0-flash-001 or
// gemini-2.5-flash-preview-09-2025 stay intact.
func ParseGeminiChannelThinkingSuffix(modelName string) (string, Intent, bool) {
	prefix, bare := splitModelNamespace(modelName)
	if !strings.HasPrefix(bare, "gemini-") {
		return modelName, Intent{}, false
	}
	base, hideThoughts := strings.CutSuffix(bare, "-nothinking")
	intent := Intent{Mode: ModeEnabled, Source: SourceSuffix}
	found := hideThoughts
	if marker := strings.LastIndex(base, "-thinking-"); marker >= 0 && isASCIIDigits(base[marker+len("-thinking-"):]) {
		// The legacy -thinking-<budget> alias reads the same as -<budget>.
		budget, err := strconv.Atoi(base[marker+len("-thinking-"):])
		if err != nil {
			return modelName, Intent{}, false
		}
		base = base[:marker]
		intent.BudgetTokens = &budget
		intent.BudgetSource = SourceSuffix
		found = true
	} else if trimmed, ok := strings.CutSuffix(base, "-thinking"); ok {
		// The legacy -thinking alias reads as the default level.
		base = trimmed
		found = true
	} else if trimmed, rawEffort, ok := TrimEffortSuffixWithSuffixes(base, EffortSuffixes); ok && legacyGeminiModelPattern.MatchString(trimmed) {
		if effort, err := ParseEffort(rawEffort); err == nil {
			base = trimmed
			intent.Effort = effort
			found = true
		}
	} else if trimmed, budget, ok := cutGeminiBudgetSuffix(base); ok {
		base = trimmed
		intent.BudgetTokens = &budget
		intent.BudgetSource = SourceSuffix
		found = true
	}
	if !found || !legacyGeminiModelPattern.MatchString(base) || strings.HasSuffix(base, "-thinking") {
		return modelName, Intent{}, false
	}
	if intent.Effort == "" && intent.BudgetTokens == nil {
		intent.Effort = EffortHigh
	}
	includeThoughts := !hideThoughts
	intent.IncludeThoughts = &includeThoughts
	return prefix + base, intent, true
}

// cutGeminiBudgetSuffix splits a trailing -<budget> off name. See
// ParseGeminiChannelThinkingSuffix for the rules that keep real ids intact.
func cutGeminiBudgetSuffix(name string) (string, int, bool) {
	dash := strings.LastIndexByte(name, '-')
	if dash <= 0 {
		return name, 0, false
	}
	digits := name[dash+1:]
	if digits == "" || len(digits) > 7 || digits[0] == '0' || !isASCIIDigits(digits) {
		return name, 0, false
	}
	base := name[:dash]
	if isASCIIDigits(base[strings.LastIndexByte(base, '-')+1:]) || !GeminiThinkingConfigurable(base) {
		return name, 0, false
	}
	budget, err := strconv.Atoi(digits)
	if err != nil {
		return name, 0, false
	}
	return base, budget, true
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
