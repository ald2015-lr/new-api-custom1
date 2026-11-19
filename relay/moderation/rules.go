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
			keyword := dto.ModerationRuleView(dto.NormalizeModerationText(custom.Pattern))
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
	var newest *segment
	var system, older []*segment
	for _, seg := range ex.segments {
		if seg.role == roleSystem {
			system = append(system, seg)
			continue
		}
		if newest != nil {
			older = append(older, newest)
		}
		newest = seg
	}
	slices.Reverse(older)
	ordered := make([]*segment, 0, len(ex.segments))
	if newest != nil {
		ordered = append(ordered, newest)
	}
	ordered = append(ordered, system...)
	ordered = append(ordered, older...)
	left := ruleScanMaxBytes
	for _, seg := range ordered {
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
			for _, text := range texts {
				if hit = rule.find(text); hit != nil {
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

func (r *moderationRule) find(text scanText) *ruleHit {
	if r.re == nil {
		haystack, needle := text.lower, r.keyword
		if r.caseSens {
			haystack = text.view
		}
		at := strings.Index(haystack, needle)
		if at < 0 {
			return nil
		}
		match := haystack[at : at+len(needle)]
		if !r.caseSens && len(text.view) == len(text.lower) {
			match = text.view[at : at+len(needle)]
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
