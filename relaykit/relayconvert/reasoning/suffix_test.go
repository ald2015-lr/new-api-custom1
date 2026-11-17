package reasoning

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGeminiModelSuffixNoThinkingDisablesReasoning(t *testing.T) {
	t.Parallel()

	base, intent, found, err := ParseGeminiModelSuffix("gemini-2.5-flash-nothinking", true)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "gemini-2.5-flash", base)
	assert.Equal(t, ModeDisabled, intent.Mode)
	assert.Equal(t, EffortNone, intent.Effort)
	assert.Equal(t, SourceSuffix, intent.Source)
}

func TestParseKnownProviderModelSuffix(t *testing.T) {
	t.Parallel()

	preserveQwenMax := func(name string) bool { return name == "qwen-max" || name == "vendor/qwen-max" }

	tests := []struct {
		name               string
		model              string
		allowThinkingAlias bool
		wantBase           string
		wantFound          bool
		wantMode           Mode
		wantEffort         Effort
		wantBudget         *int
		wantErr            bool
	}{
		{
			name:               "claude thinking alias",
			model:              "claude-3-7-sonnet-thinking",
			allowThinkingAlias: true,
			wantBase:           "claude-3-7-sonnet",
			wantFound:          true,
			wantMode:           ModeEnabled,
		},
		{
			name:               "claude nothinking alias",
			model:              "claude-3-7-sonnet-nothinking",
			allowThinkingAlias: true,
			wantBase:           "claude-3-7-sonnet",
			wantFound:          true,
			wantMode:           ModeDisabled,
			wantEffort:         EffortNone,
		},
		{
			name:               "claude thinking budget",
			model:              "claude-3-7-sonnet-thinking-8192",
			allowThinkingAlias: true,
			wantBase:           "claude-3-7-sonnet",
			wantFound:          true,
			wantBudget:         intPtr(8192),
		},
		{
			name:               "claude effort tail",
			model:              "claude-opus-4-8-high",
			allowThinkingAlias: true,
			wantBase:           "claude-opus-4-8",
			wantFound:          true,
			wantMode:           ModeEnabled,
			wantEffort:         EffortHigh,
		},
		{
			name:               "gemini thinking alias",
			model:              "gemini-2.5-flash-thinking",
			allowThinkingAlias: true,
			wantBase:           "gemini-2.5-flash",
			wantFound:          true,
			wantMode:           ModeEnabled,
		},
		{
			name:               "malformed thinking budget",
			model:              "claude-3-7-sonnet-thinking-abc",
			allowThinkingAlias: true,
			wantErr:            true,
		},
		{
			name:      "unknown openai-compatible name is untouched",
			model:     "gpt-4o-mini",
			wantBase:  "gpt-4o-mini",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base, intent, found, err := ParseKnownProviderModelSuffix(tt.model, tt.allowThinkingAlias)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, tt.wantMode, intent.Mode)
			assert.Equal(t, tt.wantEffort, intent.Effort)
			if tt.wantBudget != nil {
				require.NotNil(t, intent.BudgetTokens)
				assert.Equal(t, *tt.wantBudget, *intent.BudgetTokens)
			} else {
				assert.Nil(t, intent.BudgetTokens)
			}
		})
	}

	t.Run("openai effort tail", func(t *testing.T) {
		t.Parallel()
		effort, base := ParseOpenAIReasoningEffortFromModelSuffix("gpt-5.6-sol-high", nil)
		assert.Equal(t, "high", effort)
		assert.Equal(t, "gpt-5.6-sol", base)
	})

	t.Run("preserve effort tail on real model id", func(t *testing.T) {
		t.Parallel()
		effort, base := ParseOpenAIReasoningEffortFromModelSuffix("qwen-max", preserveQwenMax)
		assert.Empty(t, effort)
		assert.Equal(t, "qwen-max", base)
	})

	t.Run("preserve effort tail with vendor prefix", func(t *testing.T) {
		t.Parallel()
		effort, base := ParseOpenAIReasoningEffortFromModelSuffix("vendor/qwen-max", preserveQwenMax)
		assert.Empty(t, effort)
		assert.Equal(t, "vendor/qwen-max", base)
	})

	t.Run("preserve gpt-5.1-codex-max with callback", func(t *testing.T) {
		t.Parallel()
		preserve := func(name string) bool { return name == "gpt-5.1-codex-max" }
		effort, base := ParseOpenAIReasoningEffortFromModelSuffix("gpt-5.1-codex-max", preserve)
		assert.Empty(t, effort)
		assert.Equal(t, "gpt-5.1-codex-max", base)
	})

	t.Run("splits gpt-5.1-codex-max without callback", func(t *testing.T) {
		t.Parallel()
		effort, base := ParseOpenAIReasoningEffortFromModelSuffix("gpt-5.1-codex-max", nil)
		assert.Equal(t, "max", effort)
		assert.Equal(t, "gpt-5.1-codex", base)
	})
}

func TestParseThinkingModifier(t *testing.T) {
	t.Parallel()

	on, ok := ParseThinkingModifier("on")
	require.True(t, ok)
	assert.Equal(t, ModeEnabled, on.Mode)

	adaptive, ok := ParseThinkingModifier("Adaptive")
	require.True(t, ok)
	assert.Equal(t, ModeAdaptive, adaptive.Mode)

	off, ok := ParseThinkingModifier("off")
	require.True(t, ok)
	assert.Equal(t, ModeDisabled, off.Mode)
	assert.Equal(t, EffortNone, off.Effort)

	zero, ok := ParseThinkingModifier("0")
	require.True(t, ok)
	assert.Equal(t, ModeDisabled, zero.Mode)

	budget, ok := ParseThinkingModifier("8192")
	require.True(t, ok)
	require.NotNil(t, budget.BudgetTokens)
	assert.Equal(t, 8192, *budget.BudgetTokens)
	assert.Equal(t, ModeEnabled, budget.Mode)

	dynamic, ok := ParseThinkingModifier("-1")
	require.True(t, ok)
	require.NotNil(t, dynamic.BudgetTokens)
	assert.Equal(t, -1, *dynamic.BudgetTokens)

	_, ok = ParseThinkingModifier("-2")
	assert.False(t, ok)
	_, ok = ParseThinkingModifier("enabled")
	assert.False(t, ok)
}

func intPtr(v int) *int {
	return &v
}

func TestParseGeminiChannelThinkingSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		model    string
		wantBase string
		// wantBudget and wantEffort describe the strength; nil budget and
		// empty effort mean the name is not a thinking suffix.
		wantBudget      *int
		wantEffort      Effort
		wantThoughts    bool
		wantUnrecognise bool
	}{
		{name: "budget", model: "gemini-3.8-flash-128", wantBase: "gemini-3.8-flash", wantBudget: intPtr(128), wantThoughts: true},
		{name: "budget without thoughts", model: "gemini-3.8-flash-128-nothinking", wantBase: "gemini-3.8-flash", wantBudget: intPtr(128)},
		{name: "nothinking alone keeps the default level", model: "gemini-3.6-flash-nothinking", wantBase: "gemini-3.6-flash", wantEffort: EffortHigh},
		{name: "level", model: "gemini-3.8-flash-low", wantBase: "gemini-3.8-flash", wantEffort: EffortLow, wantThoughts: true},
		{name: "xhigh is not read as high", model: "gemini-4-hydrogen-xhigh", wantBase: "gemini-4-hydrogen", wantEffort: EffortXHigh, wantThoughts: true},
		{name: "max", model: "gemini-3.6-flash-max", wantBase: "gemini-3.6-flash", wantEffort: EffortMax, wantThoughts: true},
		{name: "minimal", model: "gemini-3.1-flash-lite-minimal", wantBase: "gemini-3.1-flash-lite", wantEffort: EffortMinimal, wantThoughts: true},
		{name: "medium without thoughts", model: "gemini-3.8-flash-medium-nothinking", wantBase: "gemini-3.8-flash", wantEffort: EffortMedium},
		{name: "budget on gemini 2.5", model: "gemini-2.5-flash-1024", wantBase: "gemini-2.5-flash", wantBudget: intPtr(1024), wantThoughts: true},
		{name: "namespace kept", model: "models/gemini-2.5-pro-128", wantBase: "models/gemini-2.5-pro", wantBudget: intPtr(128), wantThoughts: true},
		{name: "legacy budget alias", model: "gemini-2.5-flash-thinking-512", wantBase: "gemini-2.5-flash", wantBudget: intPtr(512), wantThoughts: true},
		{name: "legacy thinking alias", model: "gemini-3.8-flash-thinking", wantBase: "gemini-3.8-flash", wantEffort: EffortHigh, wantThoughts: true},
		{name: "bare name has no suffix", model: "gemini-3.8-flash", wantUnrecognise: true},
		{name: "versioned id", model: "gemini-2.0-flash-001", wantUnrecognise: true},
		{name: "embedding id", model: "gemini-embedding-001", wantUnrecognise: true},
		{name: "dated preview id", model: "gemini-2.5-flash-preview-09-2025", wantUnrecognise: true},
		{name: "dated native audio id", model: "gemini-2.5-flash-native-audio-preview-12-2025", wantUnrecognise: true},
		{name: "number after an unknown model", model: "gemini-exp-1206", wantUnrecognise: true},
		{name: "leading zero", model: "gemini-3.8-flash-0128", wantUnrecognise: true},
		{name: "not a gemini name", model: "gemma-4-31b-it-high", wantUnrecognise: true},
		{name: "openai name", model: "gpt-5-high", wantUnrecognise: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base, intent, found := ParseGeminiChannelThinkingSuffix(tt.model)
			if tt.wantUnrecognise {
				assert.False(t, found)
				assert.Equal(t, tt.model, base)
				assert.True(t, intent.IsEmpty())
				return
			}
			require.True(t, found)
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, ModeEnabled, intent.Mode)
			assert.Equal(t, SourceSuffix, intent.Source)
			assert.Equal(t, tt.wantEffort, intent.Effort)
			if tt.wantBudget == nil {
				assert.Nil(t, intent.BudgetTokens)
			} else {
				require.NotNil(t, intent.BudgetTokens)
				assert.Equal(t, *tt.wantBudget, *intent.BudgetTokens)
			}
			require.NotNil(t, intent.IncludeThoughts)
			assert.Equal(t, tt.wantThoughts, *intent.IncludeThoughts)
		})
	}
}

func TestGeminiChannelThinkingSuffixRendersPerModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model      string
		wantLevel  string
		wantBudget *int
	}{
		// A budget is a budget where the model takes one…
		{model: "gemini-2.5-flash-128", wantBudget: intPtr(128)},
		{model: "gemini-2.5-pro-128", wantBudget: intPtr(128)},
		{model: "gemini-2.5-flash-lite-128", wantBudget: intPtr(512)},
		// …and a level where it only takes levels; 128 is low.
		{model: "gemini-3.8-flash-128-nothinking", wantLevel: "low"},
		{model: "gemini-4-hydrogen-128", wantLevel: "low"},
		{model: "gemini-3.6-flash-24576", wantLevel: "high"},
		{model: "gemini-3.8-flash-nothinking", wantLevel: "high"},
		{model: "gemini-3.8-flash-minimal", wantLevel: "low"},
		{model: "gemini-3.6-flash-minimal", wantLevel: "minimal"},
		{model: "gemini-4-hydrogen-max", wantLevel: "xhigh"},
		{model: "gemini-4-hydrogen-xhigh", wantLevel: "xhigh"},
		{model: "gemini-4-carbon-xhigh", wantLevel: "high"},
		{model: "gemini-3.8-flash-max", wantLevel: "high"},
		{model: "gemini-2.5-pro-max", wantBudget: intPtr(32768)},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
			base, intent, found := ParseGeminiChannelThinkingSuffix(tt.model)
			require.True(t, found)
			rendered, err := RenderGemini(base, intent, nil, 0)
			require.NoError(t, err)
			require.NotNil(t, rendered.Config)
			assert.Equal(t, tt.wantLevel, rendered.Config.ThinkingLevel)
			if tt.wantBudget == nil {
				assert.Nil(t, rendered.Config.ThinkingBudget)
			} else {
				require.NotNil(t, rendered.Config.ThinkingBudget)
				assert.Equal(t, *tt.wantBudget, *rendered.Config.ThinkingBudget)
			}
			require.NotNil(t, rendered.Config.IncludeThoughts)
			assert.Equal(t, !strings.HasSuffix(tt.model, "-nothinking"), *rendered.Config.IncludeThoughts)
		})
	}
}

func TestGeminiThinkingConfigurable(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-3.8-flash", "gemini-3.1-flash-lite", "gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon"} {
		assert.True(t, GeminiThinkingConfigurable(model), model)
	}
	for _, model := range []string{"gemini-2.5-flash-image", "gemini-3.8-flash-tts", "gemini-embedding-001", "gemini-exp", "gemma-4-31b-it", "gemini-2.0-flash"} {
		assert.False(t, GeminiThinkingConfigurable(model), model)
	}
}
