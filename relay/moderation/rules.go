package moderation

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const (
	// ruleSegmentMaxBytes and ruleScanMaxBytes bound the text one attempt's
	// rules scan: per segment and in total.
	ruleSegmentMaxBytes = 128 << 10
	ruleScanMaxBytes    = 256 << 10
	// negationWindowBytes is how much text before a preset match its
	// negation guard reads.
	negationWindowBytes = 64
	// matchMaxRunes bounds the matched text kept in the audit.
	matchMaxRunes = 64
)

// scanText is one segment's text as the rules scan it.
type scanText struct {
	view  string
	lower string
	// keywordView and keywordLower are view and lower as keywords read them,
	// made on first use: a line break counts as a space.
	keywordView, keywordLower *string
}

// keywordText is the text a keyword rule searches. Folding "\n" into " "
// keeps every byte offset, so a match maps back onto view or lower.
func (t *scanText) keywordText(caseSensitive bool) string {
	text, folded := t.lower, &t.keywordLower
	if caseSensitive {
		text, folded = t.view, &t.keywordView
	}
	if *folded == nil {
		text = strings.ReplaceAll(text, "\n", " ") // no copy without a line break
		*folded = &text
	}
	return **folded
}

// viewOfLower is the text of view that lower holds at bytes [at, end).
// strings.ToLower maps text rune for rune, so the runes line up even where
// lower-casing changes a rune's length (Ⱥ grows, İ shrinks) and the byte
// offsets do not.
func (t *scanText) viewOfLower(at, end int) string {
	start := runeOffset(t.view, 0, utf8.RuneCountInString(t.lower[:at]))
	stop := runeOffset(t.view, start, utf8.RuneCountInString(t.lower[at:end]))
	return t.view[start:stop]
}

// runeOffset is the byte offset of the rune n runes after byte from of s.
func runeOffset(s string, from, n int) int {
	for ; n > 0 && from < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[from:])
		from += size
	}
	return from
}

type ruleHit struct {
	match string
}

// moderationRule is a preset or custom rule ready to run.
type moderationRule struct {
	key      string // memo key: kind, type, case flag and pattern
	audit    relaycommon.ModerationAuditRule
	keyword  string // normalised (and lower-cased unless case-sensitive)
	caseSens bool
	re       *regexp.Regexp
	hints    []string
	guard    *regexp.Regexp
}

var (
	presetsOnce     sync.Once
	compiledPresets map[string]*moderationRule
	regexCache      sync.Map // "caseSensitive\x00pattern" -> *regexp.Regexp (nil when invalid)
)

func presetRules() map[string]*moderationRule {
	presetsOnce.Do(func() {
		guards := map[string]*regexp.Regexp{
			dto.ModerationNegationEnglish: regexp.MustCompile(dto.ModerationNegationGuardEnglish),
			dto.ModerationNegationChinese: regexp.MustCompile(dto.ModerationNegationGuardChinese),
		}
		compiledPresets = make(map[string]*moderationRule)
		for _, preset := range dto.ModerationRulePresets() {
			re, err := dto.CompileModerationRegex(preset.Pattern, preset.CaseSensitive)
			if err != nil {
				continue
			}
			compiledPresets[preset.ID] = &moderationRule{
				key:      "preset\x00" + dto.ModerationRuleTypeRegex + "\x00" + strconv.FormatBool(preset.CaseSensitive) + "\x00" + preset.Pattern + "\x00" + preset.Negation,
				audit:    relaycommon.ModerationAuditRule{Kind: relaycommon.ModerationRuleKindPreset, ID: preset.ID, Type: dto.ModerationRuleTypeRegex},
				caseSens: preset.CaseSensitive,
				re:       re,
				hints:    preset.Hints,
				guard:    guards[preset.Negation],
			}
		}
	})
	return compiledPresets
}

// channelRules are the channel's presets in list order, then its custom
// rules in index order. Unknown preset IDs and custom regexes that do not
// compile are skipped.
func channelRules(c *gin.Context, channelID int, settings *dto.ChannelOtherSettings) []*moderationRule {
	presets := presetRules()
	var rules []*moderationRule
	for _, id := range settings.EffectiveModerationRulePresets() {
		if rule, ok := presets[id]; ok {
			rules = append(rules, rule)
		}
	}
	for i, custom := range settings.ModerationRules {
		index := i
		rule := &moderationRule{
			key:      "custom\x00" + custom.Type + "\x00" + strconv.FormatBool(custom.CaseSensitive) + "\x00" + custom.Pattern,
			audit:    relaycommon.ModerationAuditRule{Kind: relaycommon.ModerationRuleKindCustom, Index: &index, Name: custom.Name, Type: custom.Type},
			caseSens: custom.CaseSensitive,
		}
		switch custom.Type {
		case dto.ModerationRuleTypeKeyword:
			// A keyword matches across line breaks: both sides read "\n" as " ".
			keyword := strings.ReplaceAll(dto.ModerationRuleView(dto.NormalizeModerationText(custom.Pattern)), "\n", " ")
			if keyword == "" {
				continue // would match every text
			}
			if !custom.CaseSensitive {
				keyword = strings.ToLower(keyword)
			}
			rule.keyword = keyword
		case dto.ModerationRuleTypeRegex:
			rule.re = compileCustomRegex(c, channelID, custom.Pattern, custom.CaseSensitive)
			if rule.re == nil {
				continue
			}
		default:
			continue
		}
		rules = append(rules, rule)
	}
	return rules
}

func compileCustomRegex(c *gin.Context, channelID int, pattern string, caseSensitive bool) *regexp.Regexp {
	key := strconv.FormatBool(caseSensitive) + "\x00" + pattern
	if cached, ok := regexCache.Load(key); ok {
		re, _ := cached.(*regexp.Regexp)
		return re
	}
	re, err := dto.CompileModerationRegex(pattern, caseSensitive)
	if err != nil {
		re = nil
		logger.LogWarn(c, common.LogText("content moderation rule of channel #%d is not a valid regular expression and is skipped", channelID))
	}
	regexCache.Store(key, re)
	return re
}

// ruleScan is the text the rules see, in the order that keeps either side
// from switching the rules off for the other: the newest user message, the
// system text in request order, then the older user messages from the
// newest. Each segment gives at most ruleSegmentMaxBytes and all together at
// most ruleScanMaxBytes; a segment that does not fit keeps its head and tail.
// The scan ends early at the first segment that the extraction's
// normalisation budget no longer covers.
func (ex *extraction) ruleScan() ([]scanText, bool) {
	if ex.scanBuilt {
		return ex.scan, ex.scanTruncated
	}
	ex.scanBuilt = true
	left := ruleScanMaxBytes
	for _, seg := range ex.orderedSegments() {
		if left <= 0 {
			ex.scanTruncated = true
			break
		}
		norm := ex.normalize(seg)
		if norm == nil {
			ex.scanTruncated = true
			break
		}
		view := norm.view
		share := min(ruleSegmentMaxBytes, left)
		if len(view) > share {
			view, _ = cutHeadTail(view, share)
			ex.scanTruncated = true
		}
		left -= len(view)
		ex.scan = append(ex.scan, scanText{view: view, lower: strings.ToLower(view)})
	}
	return ex.scan, ex.scanTruncated
}

// firstRuleHit runs the rules in order and returns the first that hits.
// Each rule scans a request's text once; a retry on another channel reuses
// the result.
func (ex *extraction) firstRuleHit(rules []*moderationRule) (*moderationRule, *ruleHit) {
	if len(rules) == 0 {
		return nil, nil
	}
	texts, _ := ex.ruleScan()
	if ex.ruleResults == nil {
		ex.ruleResults = make(map[string]*ruleHit)
	}
	for _, rule := range rules {
		hit, done := ex.ruleResults[rule.key]
		if !done {
			for i := range texts {
				if hit = rule.find(&texts[i]); hit != nil {
					break
				}
			}
			ex.ruleResults[rule.key] = hit
		}
		if hit != nil {
			return rule, hit
		}
	}
	return nil, nil
}

// find runs the rule on one segment. Keywords read a line break as a space;
// presets, custom regexes and the negation guards keep it, as a sentence
// boundary.
func (r *moderationRule) find(text *scanText) *ruleHit {
	if r.re == nil {
		at := strings.Index(text.keywordText(r.caseSens), r.keyword)
		if at < 0 {
			return nil
		}
		end := at + len(r.keyword)
		match := text.view[at:end]
		if !r.caseSens {
			match = text.viewOfLower(at, end)
		}
		return &ruleHit{match: cutRunes(match, matchMaxRunes)}
	}
	if len(r.hints) > 0 && !slices.ContainsFunc(r.hints, func(hint string) bool { return strings.Contains(text.lower, hint) }) {
		return nil
	}
	if r.guard == nil {
		loc := r.re.FindStringIndex(text.view)
		if loc == nil {
			return nil
		}
		return &ruleHit{match: cutRunes(text.view[loc[0]:loc[1]], matchMaxRunes)}
	}
	// A match right after a negation is skipped and the search restarts one
	// rune after its start, so a negated match cannot hide a later request.
	view := text.view
	for pos := 0; pos <= len(view); {
		loc := r.re.FindStringIndex(view[pos:])
		if loc == nil {
			return nil
		}
		start, end := pos+loc[0], pos+loc[1]
		if !r.negated(view[:start]) {
			return &ruleHit{match: cutRunes(view[start:end], matchMaxRunes)}
		}
		_, size := utf8.DecodeRuneInString(view[start:])
		pos = start + max(size, 1)
	}
	return nil
}

func (r *moderationRule) negated(before string) bool {
	if len(before) > negationWindowBytes {
		cut := len(before) - negationWindowBytes
		for cut < len(before) && !utf8.RuneStart(before[cut]) {
			cut++
		}
		before = before[cut:]
	}
	return r.guard.MatchString(before)
}
