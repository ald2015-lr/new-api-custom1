package helper

import (
	"context"
	"fmt"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/gin-gonic/gin"
)

// ApplyReasoningModelSuffix parses host-private reasoning suffixes from the
// origin and mapped model names, attaches the resulting intent to RelayInfo,
// and normalizes UpstreamModelName to the unsuffixed base. Optional outbound
// requests are the DeepCopy the handler will send upstream; they must be
// synced here because info.Request is the original, not that copy. Explicit
// model modifiers override request fields; mapped-model modifiers override
// origin-model modifiers. Pass-through (global or channel) is a no-op so the
// request body stays byte-identical. c is used only to correlate diagnostics.
func ApplyReasoningModelSuffix(c *gin.Context, info *relaycommon.RelayInfo, outbound ...dto.Request) error {
	if info == nil {
		return nil
	}
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled ||
		info.ChannelMeta != nil && info.ChannelSetting.PassThroughBodyEnabled {
		return nil
	}

	opts := info.ConvOptions()
	origin := info.GetOriginModelName()
	upstream := ""
	if info.ChannelMeta != nil {
		upstream = info.UpstreamModelName
	}
	originParsed, err := parseRequestModelName(origin, opts)
	if err != nil {
		return reasoning.AsClientError(err)
	}
	upstreamParsed := originParsed
	if upstream != origin {
		upstreamParsed, err = parseRequestModelName(upstream, opts)
		if err != nil {
			return reasoning.AsClientError(err)
		}
	}
	diagnostics := append([]types.ConversionDiagnostic(nil), originParsed.diagnostics...)
	if upstream != origin {
		diagnostics = append(diagnostics, upstreamParsed.diagnostics...)
	}

	selected := originParsed
	if upstream != origin {
		selected, diagnostics = overlayMappedModelModifiers(selected, upstreamParsed, diagnostics)
	}
	if !selected.hasThinking && opts.Gemini.ChannelThinkingSuffixEnabled {
		if intent, ok := geminiChannelDefaultThinking(selected.base, info.Request); ok {
			selected.intent = intent
			selected.hasThinking = true
		}
	}

	if selected.hasThinking {
		explicit, explicitDiagnostics, err := explicitIntentFromRequest(info.Request)
		if err != nil {
			return reasoning.AsClientError(err)
		}
		diagnostics = append(diagnostics, explicitDiagnostics...)
		diagnostics = append(diagnostics, modifierRequestOverrideDiagnostics(explicit, selected.intent)...)
		if selected.intent.IncludeThoughts == nil {
			selected.intent.IncludeThoughts = explicit.IncludeThoughts
		}
		info.ReasoningConversion = reasoning.StateFromIntent(selected.intent)
	}
	if info.ChannelMeta != nil {
		info.UpstreamModelName = selected.base
	}

	if selected.hasTemperature {
		if current, exists := extractTemperature(info.Request); exists && *selected.temperature != current {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"model_modifier_overrode_request",
				"temperature",
				fmt.Sprintf("model temperature modifier overrides request temperature %v", current),
			))
		}
	}
	if selected.hasTopP {
		if current, exists := extractTopP(info.Request); exists && *selected.topP != current {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"model_modifier_overrode_request",
				"topp",
				fmt.Sprintf("model topp modifier overrides request top_p %v", current),
			))
		}
	}

	// Handlers DeepCopy before this helper; info.Request is the original.
	// Sync every outbound copy the caller is about to send upstream.
	for _, outbound := range outbound {
		if outbound == nil {
			continue
		}
		if err := applyModelControls(outbound, selected); err != nil {
			return reasoning.AsClientError(err)
		}
		outbound.SetModelName(info.UpstreamModelName)
	}
	if info.Request != nil {
		info.Request.SetModelName(info.UpstreamModelName)
	}
	if selected.hasThinking {
		info.SetReasoningEffort(string(reasoning.EffectiveEffort(selected.intent)))
	}
	for i := range diagnostics {
		diagnostics[i].From = info.RelayFormat
	}
	diagnosticContext := context.Background()
	if c != nil {
		diagnosticContext = c
	}
	info.RecordConversionDiagnostics(diagnosticContext, diagnostics)
	return nil
}

func parseRequestModelName(name string, opts *convmeta.Options) (parsedModelModifiers, error) {
	if opts.ShouldPreserveThinkingSuffix(name) {
		return parsedModelModifiers{base: name}, nil
	}
	parsed, err := parseExplicitModelModifiers(name)
	if err != nil {
		return parsedModelModifiers{}, err
	}
	if opts.ShouldPreserveThinkingSuffix(parsed.base) {
		return parsed, nil
	}
	legacyBase, legacyIntent, legacyFound, err := parseHostModelSuffix(parsed.base, opts)
	if err != nil {
		return parsedModelModifiers{}, err
	}
	parsed.base = legacyBase
	if legacyFound && !parsed.hasThinking {
		parsed.intent = legacyIntent
		parsed.hasThinking = true
	}
	return parsed, nil
}

func overlayMappedModelModifiers(origin parsedModelModifiers, mapped parsedModelModifiers, diagnostics []types.ConversionDiagnostic) (parsedModelModifiers, []types.ConversionDiagnostic) {
	origin.base = mapped.base
	origin.hasSyntax = origin.hasSyntax || mapped.hasSyntax
	origin.diagnostics = nil
	if mapped.hasThinking {
		if origin.hasThinking && !sameModifierIntent(origin.intent, mapped.intent) {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"mapped_model_modifier_override",
				"thinking",
				"mapped-model thinking modifier overrides the origin-model modifier",
			))
		}
		origin.intent = mapped.intent
		origin.hasThinking = true
	}
	if mapped.hasTemperature {
		if origin.hasTemperature && *origin.temperature != *mapped.temperature {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"mapped_model_modifier_override",
				"temperature",
				"mapped-model temperature modifier overrides the origin-model modifier",
			))
		}
		origin.temperature = mapped.temperature
		origin.hasTemperature = true
	}
	if mapped.hasTopP {
		if origin.hasTopP && *origin.topP != *mapped.topP {
			diagnostics = append(diagnostics, modelModifierDiagnostic(
				"mapped_model_modifier_override",
				"topp",
				"mapped-model topp modifier overrides the origin-model modifier",
			))
		}
		origin.topP = mapped.topP
		origin.hasTopP = true
	}
	return origin, diagnostics
}

func modifierRequestOverrideDiagnostics(explicit reasoning.Intent, modifier reasoning.Intent) []types.ConversionDiagnostic {
	if !explicit.HasStrength() || sameModifierIntent(explicit, modifier) {
		return nil
	}
	return []types.ConversionDiagnostic{modelModifierDiagnostic(
		"model_modifier_overrode_request",
		"thinking",
		"model thinking modifier overrides structured request reasoning fields",
	)}
}

func sameModifierIntent(left reasoning.Intent, right reasoning.Intent) bool {
	if left.Mode != right.Mode || left.Effort != right.Effort {
		return false
	}
	if left.BudgetTokens == nil || right.BudgetTokens == nil {
		return left.BudgetTokens == nil && right.BudgetTokens == nil
	}
	return *left.BudgetTokens == *right.BudgetTokens
}

func parseHostModelSuffix(name string, opts *convmeta.Options) (string, reasoning.Intent, bool, error) {
	if name == "" {
		return name, reasoning.Intent{}, false, nil
	}
	if opts.Gemini.ChannelThinkingSuffixEnabled {
		if base, intent, found := reasoning.ParseGeminiChannelThinkingSuffix(name); found {
			return base, intent, true, nil
		}
	}
	return hostreasoning.ParseLegacyModelSuffix(
		name,
		opts.Claude.ThinkingAdapterEnabled,
		opts.Gemini.ThinkingAdapterEnabled,
	)
}

func explicitIntentFromRequest(req dto.Request) (reasoning.Intent, []types.ConversionDiagnostic, error) {
	switch r := req.(type) {
	case *dto.ClaudeRequest:
		return reasoning.FromClaude(r)
	case *dto.GeminiChatRequest:
		return reasoning.FromGemini(r)
	case *dto.GeneralOpenAIRequest:
		return reasoning.FromOpenAIChat(r)
	case *dto.OpenAIResponsesRequest:
		return reasoning.FromOpenAIResponses(r)
	default:
		return reasoning.Intent{}, nil, nil
	}
}

// geminiChannelDefaultThinking is the thinking a Gemini channel with thinking
// suffixes on gives a gemini-* name that has none: high, with thoughts
// returned. A request that sets its own thinking strength keeps it, and a
// request's own includeThoughts is kept too. Models whose thinking is not
// configurable (or unknown) get nothing.
func geminiChannelDefaultThinking(base string, req dto.Request) (reasoning.Intent, bool) {
	bare := base
	if slash := strings.LastIndex(bare, "/"); slash >= 0 {
		bare = bare[slash+1:]
	}
	if !strings.HasPrefix(bare, "gemini-") || !reasoning.GeminiThinkingConfigurable(bare) {
		return reasoning.Intent{}, false
	}
	explicit, _, err := explicitIntentFromRequest(req)
	if err != nil || explicit.HasStrength() {
		return reasoning.Intent{}, false
	}
	includeThoughts := true
	if explicit.IncludeThoughts != nil {
		includeThoughts = *explicit.IncludeThoughts
	}
	return reasoning.Intent{
		Mode:            reasoning.ModeEnabled,
		Effort:          reasoning.EffortHigh,
		IncludeThoughts: &includeThoughts,
		Source:          reasoning.SourceSuffix,
	}, true
}
