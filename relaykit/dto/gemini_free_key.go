package dto

import (
	"fmt"
	"strings"
)

// GeminiFreeKeyModelLimit is the free tier's limit of one model on a Gemini
// channel in free key mode. Aliases are other names Google counts in the same
// quota. Each limit is GeminiFreeKeyUnlimited, GeminiFreeKeyNotOnFreeTier or
// a positive number: requests per minute, input tokens per minute and
// requests per Pacific day of one key.
type GeminiFreeKeyModelLimit struct {
	Model   string   `json:"model"`
	Aliases []string `json:"aliases,omitempty"`
	RPM     int      `json:"rpm"`
	TPM     int      `json:"tpm"`
	RPD     int      `json:"rpd"`
}

const (
	// GeminiFreeKeyUnlimited is a limit that does not apply.
	GeminiFreeKeyUnlimited = -1
	// GeminiFreeKeyNotOnFreeTier is a limit of a model the free tier does not
	// serve: its requests are refused without reaching Google.
	GeminiFreeKeyNotOnFreeTier = 0
	// GeminiFreeKeyLimitMax bounds a limit.
	GeminiFreeKeyLimitMax = 1_000_000_000

	// GeminiFreeKeyTokenCounterCountTokens counts a request's input tokens
	// with Google's countTokens, falling back to the local estimate.
	GeminiFreeKeyTokenCounterCountTokens = "count_tokens"
	// GeminiFreeKeyTokenCounterLocal uses only the local estimate.
	GeminiFreeKeyTokenCounterLocal = "local"

	// GeminiFreeKeyMaxAttemptsDefault is how many keys a request may try when
	// the channel sets none; GeminiFreeKeyMaxAttemptsMax bounds it.
	GeminiFreeKeyMaxAttemptsDefault = 3
	GeminiFreeKeyMaxAttemptsMax     = 10

	// GeminiFreeKeyModelLimitsMax bounds the rows, GeminiFreeKeyAliasesMax
	// the aliases of a row and GeminiFreeKeyModelNameMax a name's length.
	GeminiFreeKeyModelLimitsMax = 300
	GeminiFreeKeyAliasesMax     = 20
	GeminiFreeKeyModelNameMax   = 128
)

// geminiFreeTierLimits is a row of DefaultGeminiFreeKeyModelLimits.
func geminiFreeTierLimits(model string, rpm, tpm, rpd int, aliases ...string) GeminiFreeKeyModelLimit {
	return GeminiFreeKeyModelLimit{Model: model, Aliases: aliases, RPM: rpm, TPM: tpm, RPD: rpd}
}

// DefaultGeminiFreeKeyModelLimits returns the AI Studio free tier limits a
// channel in free key mode uses unless it saves its own: the models the free
// tier serves, the Live models (unlimited requests, listed so that their
// names do not fall into a Flash model's limits), and the models it does not
// serve. It returns a new slice on each call.
func DefaultGeminiFreeKeyModelLimits() []GeminiFreeKeyModelLimit {
	const unlimited, notFree = GeminiFreeKeyUnlimited, GeminiFreeKeyNotOnFreeTier
	return []GeminiFreeKeyModelLimit{
		geminiFreeTierLimits("gemini-3.8-flash", 5, 250000, 20, "gemini-3.7-flash"),
		geminiFreeTierLimits("gemini-3.6-flash", 5, 250000, 20, "gemini-3.5-flash"),
		geminiFreeTierLimits("gemini-3-flash-preview", 5, 250000, 20, "gemini-3-flash"),
		geminiFreeTierLimits("gemini-2.5-flash", 5, 250000, 20),
		geminiFreeTierLimits("gemini-2.5-flash-lite", 10, 250000, 20),
		geminiFreeTierLimits("gemini-3.1-flash-lite", 15, 250000, 500, "gemini-3.1-flash-lite-preview"),
		geminiFreeTierLimits("gemini-3.5-flash-lite", 15, 250000, 500),
		geminiFreeTierLimits("gemini-4-hydrogen", 5, 250000, 20),
		geminiFreeTierLimits("gemini-robotics-er-2-preview", 5, 250000, 20),
		geminiFreeTierLimits("gemma-4-26b-a4b-it", 30, 16000, 14400, "gemma-4-26b"),
		geminiFreeTierLimits("gemma-4-31b-it", 30, 16000, 14400, "gemma-4-31b"),
		geminiFreeTierLimits("gemini-2.5-flash-preview-tts", 3, 10000, 10),
		geminiFreeTierLimits("gemini-3.1-flash-tts-preview", 3, 10000, 10),
		geminiFreeTierLimits("gemini-3.8-flash-tts", 3, 10000, 10),
		geminiFreeTierLimits("gemini-3.8-flash-lite-tts", 3, 10000, 10),
		geminiFreeTierLimits("gemini-3.5-transcribe", 3, 10000, 25),
		geminiFreeTierLimits("gemini-embedding-001", 100, 30000, 1000),
		geminiFreeTierLimits("gemini-embedding-2", 100, 30000, 1000, "gemini-embedding-2-preview"),
		geminiFreeTierLimits("gemini-decision-1", 200, 50000, 10000),
		geminiFreeTierLimits("antigravity-preview-09-2026", 60, 100000, 100, "antigravity-preview-05-2026"),

		geminiFreeTierLimits("gemini-2.5-flash-native-audio-preview-09-2025", unlimited, 1000000, unlimited, "gemini-2.5-flash-native-audio-preview-12-2025", "gemini-2.5-flash-native-audio-latest"),
		geminiFreeTierLimits("gemini-3.1-flash-live-preview", unlimited, 65000, unlimited),
		geminiFreeTierLimits("gemini-3.5-live-translate-preview", unlimited, 20000, unlimited),
		geminiFreeTierLimits("gemini-3.5-transcribe-live", unlimited, 20000, unlimited),
		geminiFreeTierLimits("gemini-3.8-live", unlimited, 65000, unlimited),
		geminiFreeTierLimits("gemini-3.8-live-extended-thinking", unlimited, 65000, unlimited),

		geminiFreeTierLimits("gemini-2.0-flash", notFree, notFree, notFree, "gemini-2.0-flash-001"),
		geminiFreeTierLimits("gemini-2.0-flash-lite", notFree, notFree, notFree, "gemini-2.0-flash-lite-001"),
		geminiFreeTierLimits("gemini-2.5-pro", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-2.5-pro-preview-tts", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-2.5-flash-image", notFree, notFree, notFree, "gemini-2.5-flash-image-preview", "gemini-2.5-flash-preview-image"),
		geminiFreeTierLimits("gemini-3-pro-image", notFree, notFree, notFree, "gemini-3-pro-image-preview", "nano-banana-pro-preview"),
		geminiFreeTierLimits("gemini-3.1-pro-preview", notFree, notFree, notFree, "gemini-3.1-pro-preview-customtools", "gemini-3.1-pro"),
		geminiFreeTierLimits("gemini-3.1-flash-image", notFree, notFree, notFree, "gemini-3.1-flash-image-preview"),
		geminiFreeTierLimits("gemini-3.1-flash-lite-image", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-4-argon", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-4-carbon", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-nano-banana-2.1", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-omni-1.1-flash", notFree, notFree, notFree, "gemini-omni-flash-preview"),
		geminiFreeTierLimits("gemini-omni-1.2-flash", notFree, notFree, notFree),
		geminiFreeTierLimits("gemini-2.5-computer-use-preview-10-2025", notFree, notFree, notFree),
		geminiFreeTierLimits("lyria-3-clip-preview", notFree, notFree, notFree),
		geminiFreeTierLimits("lyria-3-pro-preview", notFree, notFree, notFree),
		geminiFreeTierLimits("deep-research-pro-preview-12-2025", notFree, notFree, notFree),
	}
}

// EffectiveGeminiFreeKeyModelLimits is the channel's limits per model, or
// DefaultGeminiFreeKeyModelLimits when it saved none.
func (s *ChannelOtherSettings) EffectiveGeminiFreeKeyModelLimits() []GeminiFreeKeyModelLimit {
	if s == nil || len(s.GeminiFreeKeyModelLimits) == 0 {
		return DefaultGeminiFreeKeyModelLimits()
	}
	return s.GeminiFreeKeyModelLimits
}

// EffectiveGeminiFreeKeyTokenCounter is how the channel counts input tokens:
// GeminiFreeKeyTokenCounterLocal when it says so, otherwise
// GeminiFreeKeyTokenCounterCountTokens.
func (s *ChannelOtherSettings) EffectiveGeminiFreeKeyTokenCounter() string {
	if s != nil && strings.TrimSpace(s.GeminiFreeKeyTokenCounter) == GeminiFreeKeyTokenCounterLocal {
		return GeminiFreeKeyTokenCounterLocal
	}
	return GeminiFreeKeyTokenCounterCountTokens
}

// EffectiveGeminiFreeKeyMaxAttempts is how many keys a request may try on
// the channel: the default when unset or not positive, at most
// GeminiFreeKeyMaxAttemptsMax.
func (s *ChannelOtherSettings) EffectiveGeminiFreeKeyMaxAttempts() int {
	if s == nil || s.GeminiFreeKeyMaxAttempts <= 0 {
		return GeminiFreeKeyMaxAttemptsDefault
	}
	return min(s.GeminiFreeKeyMaxAttempts, GeminiFreeKeyMaxAttemptsMax)
}

// ValidateGeminiFreeKey rejects a token counter other than blank,
// count_tokens and local, attempts outside 1 to GeminiFreeKeyMaxAttemptsMax
// (0 keeps the default), and limits per model that are not GeminiFreeKey
// rows: at most GeminiFreeKeyModelLimitsMax rows of a model with at most
// GeminiFreeKeyAliasesMax aliases, every name non-empty, trimmed, at most
// GeminiFreeKeyModelNameMax characters and used once in the whole list
// ignoring case, and every limit from -1 to GeminiFreeKeyLimitMax.
func (s *ChannelOtherSettings) ValidateGeminiFreeKey() error {
	if s == nil {
		return nil
	}
	switch s.GeminiFreeKeyTokenCounter {
	case "", GeminiFreeKeyTokenCounterCountTokens, GeminiFreeKeyTokenCounterLocal:
	default:
		return fmt.Errorf("gemini_free_key_token_counter must be %s or %s", GeminiFreeKeyTokenCounterCountTokens, GeminiFreeKeyTokenCounterLocal)
	}
	if attempts := s.GeminiFreeKeyMaxAttempts; attempts < 0 || attempts > GeminiFreeKeyMaxAttemptsMax {
		return fmt.Errorf("gemini_free_key_max_attempts must be between 1 and %d", GeminiFreeKeyMaxAttemptsMax)
	}
	rows := s.GeminiFreeKeyModelLimits
	if len(rows) > GeminiFreeKeyModelLimitsMax {
		return fmt.Errorf("gemini_free_key_model_limits must have at most %d models", GeminiFreeKeyModelLimitsMax)
	}
	seen := make(map[string]bool)
	checkName := func(field, name string) error {
		switch {
		case name == "":
			return fmt.Errorf("%s must not be empty", field)
		case name != strings.TrimSpace(name):
			return fmt.Errorf("%s must not start or end with spaces", field)
		case len([]rune(name)) > GeminiFreeKeyModelNameMax:
			return fmt.Errorf("%s must be at most %d characters", field, GeminiFreeKeyModelNameMax)
		case seen[strings.ToLower(name)]:
			return fmt.Errorf("%s %q is listed more than once in gemini_free_key_model_limits", field, name)
		}
		seen[strings.ToLower(name)] = true
		return nil
	}
	for i, row := range rows {
		field := fmt.Sprintf("gemini_free_key_model_limits[%d]", i)
		if err := checkName(field+".model", row.Model); err != nil {
			return err
		}
		if len(row.Aliases) > GeminiFreeKeyAliasesMax {
			return fmt.Errorf("%s.aliases must have at most %d names", field, GeminiFreeKeyAliasesMax)
		}
		for j, alias := range row.Aliases {
			if err := checkName(fmt.Sprintf("%s.aliases[%d]", field, j), alias); err != nil {
				return err
			}
		}
		for _, limit := range []struct {
			name  string
			value int
		}{{"rpm", row.RPM}, {"tpm", row.TPM}, {"rpd", row.RPD}} {
			if limit.value < GeminiFreeKeyUnlimited || limit.value > GeminiFreeKeyLimitMax {
				return fmt.Errorf("%s.%s must be between -1 and %d", field, limit.name, GeminiFreeKeyLimitMax)
			}
		}
	}
	return nil
}
