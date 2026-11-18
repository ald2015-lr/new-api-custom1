package moderation

import (
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// segmentMaxBytes bounds a segment before and after normalisation: a longer
// text keeps its first and last half (NFKC can expand text up to 18 times).
const segmentMaxBytes = 1 << 20

// normalized is a segment as content moderation sees it.
type normalized struct {
	text      string // sent to OpenAI and hashed
	view      string // what the rules match: text without combining marks
	lower     string // view lower-cased once, for case-insensitive keywords and hints
	truncated bool   // the normalised text was longer than segmentMaxBytes
}

func normalizeSegment(raw string) *normalized {
	text, truncated := cutHeadTail(dto.NormalizeModerationText(raw), segmentMaxBytes)
	view := dto.ModerationRuleView(text)
	return &normalized{text: text, view: view, lower: strings.ToLower(view), truncated: truncated}
}

// cutHeadTail keeps the first and last maxBytes/2 bytes of a longer text,
// cut at rune boundaries and joined by "\n".
func cutHeadTail(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	half := maxBytes / 2
	headEnd := half
	for headEnd > 0 && !utf8.RuneStart(text[headEnd]) {
		headEnd--
	}
	tailStart := len(text) - half
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return text[:headEnd] + "\n" + text[tailStart:], true
}

// cutRunes keeps at most n runes of text.
func cutRunes(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n])
}
