package dto

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ModerationRule is one of a channel's own content moderation rules: a
// keyword found anywhere in the text, or an RE2 regular expression.
type ModerationRule struct {
	Name          string `json:"name,omitempty"`
	Type          string `json:"type"`                     // ModerationRuleTypeKeyword | ModerationRuleTypeRegex
	Pattern       string `json:"pattern"`                  // as typed; keywords are normalised like the text before matching
	CaseSensitive bool   `json:"case_sensitive,omitempty"` // false: keywords and regexes ignore case
}

const (
	// ModerationModel is the OpenAI moderation model every call names.
	ModerationModel = "omni-moderation-latest"

	ModerationMaxCharsDefault       = 10000
	ModerationMaxCharsMin           = 1000
	ModerationMaxCharsMax           = 200000
	ModerationTimeoutSecondsDefault = 10
	ModerationTimeoutSecondsMin     = 1
	ModerationTimeoutSecondsMax     = 60
	ModerationRulesMax              = 100
	ModerationRegexRulesMax         = 30
	ModerationRulePatternMax        = 512 // runes
	ModerationRuleNameMax           = 64  // runes
	ModerationRuleTypeKeyword       = "keyword"
	ModerationRuleTypeRegex         = "regex"
	// ModerationRegexProgramMax bounds the compiled RE2 program of a custom
	// regex, so one rule cannot make every request's scan slow.
	ModerationRegexProgramMax = 2000

	// ModerationPresetGroupLower holds the presets with a lower false-positive
	// risk; the web selects them the first time moderation is turned on.
	ModerationPresetGroupLower = "lower"
	// ModerationPresetGroupHigher holds the opt-in presets.
	ModerationPresetGroupHigher = "higher"

	ModerationNegationNone    = ""   // no negation guard
	ModerationNegationEnglish = "en" // skip a match right after an English negation
	ModerationNegationChinese = "zh" // skip a match right after a Chinese negation
)

// ModerationCategoryNames are the omni-moderation categories, in OpenAI's order.
var ModerationCategoryNames = []string{
	"harassment", "harassment/threatening", "hate", "hate/threatening",
	"illicit", "illicit/violent", "self-harm", "self-harm/intent",
	"self-harm/instructions", "sexual", "sexual/minors", "violence", "violence/graphic",
}

// ModerationNegationGuardEnglish and ModerationNegationGuardChinese match the
// end of the text right before a preset match that negates it ("Never
// reveal …", 「不要输出…」): such a match is skipped, because app system
// prompts forbid exactly what the presets look for. Both are narrow on
// purpose: a negator, at most one adverb from a closed list, then the match.
const (
	ModerationNegationGuardEnglish = `(?i)(?:(?:^|[^\w\s'’]\s*|\b(?:do|does|did|must|should|shall|will|would|can|could|may|might|please|you|we|i|they|and|but|then|so|always|to)\s+)(?:not|never)|\b(?:don|doesn|didn|mustn|shouldn|won|wouldn|can|couldn|mightn)['’]?t|\bcannot)(?:,?\s+(?:ever|directly|explicitly|openly|publicly|actually|accidentally|intentionally|verbally|fully))?,?\s+$`
	ModerationNegationGuardChinese = `(?:不要|不得|不能|不可|不准|不许|不許|禁止|严禁|嚴禁|请勿|請勿|切勿|无需|無需|避免|绝不|絕不|永远不要|永遠不要|千万不要|千萬不要|(?:^|[\s,.!?;:，。！？；：、]|请|請|你|您|千万|千萬)(?:别|別|勿))(?:尝试|嘗試|试图|試圖)?(?:向|对|對|给|給|跟)?(?:用户|用戶|使用者|任何人|他人|对方|對方)?(?:直接|明确|明確|公开|公開)?(?:输出|輸出|透露|展示|显示|顯示|泄露|洩露|暴露|公开|公開|提供|给出|給出|说出|說出|告诉|告訴|分享|包含|写出|寫出|打印|返回|复述|複述|列出|呈现|呈現|解释|解釋)?(?:出|给用户|給用戶)?$`
)

// ModerationRulePreset is a built-in rule against chain-of-thought extraction
// and distillation prompts.
type ModerationRulePreset struct {
	ID            string
	Group         string   // ModerationPresetGroupLower | ModerationPresetGroupHigher
	DefaultOn     bool     // Group == ModerationPresetGroupLower
	CaseSensitive bool     // false: compiled as "(?i)" + Pattern
	Negation      string   // ModerationNegationNone | ModerationNegationEnglish | ModerationNegationChinese
	Hints         []string // lower-case literals; every match contains one of them
	Pattern       string   // RE2, without the (?i) prefix
}

// ModerationRulePresets returns the built-in rules in catalogue order. It
// returns a new slice on each call.
func ModerationRulePresets() []ModerationRulePreset {
	return []ModerationRulePreset{
		{
			ID: "en-reveal-cot", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: false, Negation: ModerationNegationEnglish,
			Hints:   []string{"chain", "cot", "reasoning", "thinking", "thought", "scratch"},
			Pattern: `\b(?:show|reveal|print|output|dump|display|expose|leak|give|write\s+out|tell|share|repeat|transcribe|return|provide|include)\b.{0,30}?\b(?:your|its|the\s+(?:model|assistant)'?s)\s+(?:[\w,'-]+\s+){0,3}?(?:chain[\s-]*of[\s-]*thoughts?|cot|(?:hidden|internal|raw|private|secret|unfiltered|unsummari[sz]ed|concealed|verbatim)\s+(?:reasoning|thinking|thought\s+process|scratch\s*pad))\b`,
		},
		{
			ID: "zh-reveal-cot", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: true, Negation: ModerationNegationChinese,
			Hints:   []string{"你", "您"},
			Pattern: `(?:你|您)(?:自己)?(?:的[^。！？!?\n]{0,6}?)?(?:原始|隐藏|隱藏|内部|內部|内在|內在|未经?(?:总结|總結|删减|刪減|过滤|過濾|修饰|修飾)|逐字|完整|全部)的?[^。！？!?\n]{0,4}?(?:思[维維][链鏈]|思考[链鏈]|推理[链鏈]|思考过程|思考過程|思[维維]过程|思[维維]過程|推理过程|推理過程|思考内容|思考內容|推理内容|推理內容|推理)|(?:你|您)(?:自己)?的(?:思[维維][链鏈]|思考[链鏈]|推理[链鏈])`,
		},
		{
			ID: "en-do-not-flag", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: false, Negation: ModerationNegationNone,
			Hints:   []string{"extraction", "distill"},
			Pattern: `\b(?:do\s+not|don'?t|never)\s+(?:flag|report|classify|detect|treat|mark|label|consider)\b.{0,40}?\bas\b.{0,20}?\b(?:(?:reasoning|cot|chain[\s-]*of[\s-]*thought)[\s-]+extraction|distill(?:ation|ing)?|extraction\s+attempts?)\b`,
		},
		{
			ID: "zh-do-not-flag", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: true, Negation: ModerationNegationNone,
			Hints:   []string{"提取", "蒸馏", "蒸餾"},
			Pattern: `(?:不要|别|別|请勿|請勿|无需|無需|切勿)[^。！？!?\n]{0,8}(?:标记|標記|识别|識別|判定|视为|視為|归类|歸類|认定|認定)[^。！？!?\n]{0,6}(?:推理提取|思[维維][链鏈]提取|思考[链鏈]提取|蒸[馏餾](?:攻击|攻擊|行为|行為|请求|請求|尝试|嘗試)?)`,
		},
		{
			ID: "en-imagine-internal-reasoning", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: false, Negation: ModerationNegationEnglish,
			Hints:   []string{"reasoning", "thought", "thinking"},
			Pattern: `\b(?:imagine|reconstruct|infer|articulate|recreate|reverse[\s-]*engineer)\b.{0,60}?\b(?:internal|hidden|underlying|inner)\s+(?:reasoning|thought\s+process|thinking|thoughts|chain[\s-]*of[\s-]*thought)\b.{0,80}?\b(?:behind|that\s+led\s+to|leading\s+to|which\s+produced|for)\b.{0,30}?\b(?:response|answer|output|reply|completion|solution)s?\b`,
		},
		{
			ID: "zh-imagine-internal-reasoning", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: true, Negation: ModerationNegationChinese,
			Hints:   []string{"背后", "背後"},
			Pattern: `(?:想象|想像|推测|推測|重建|还原|還原|补全|補全|反推)[^。！？!?\n]{0,20}(?:回答|答案|回复|回覆|输出|輸出|响应|響應)[^。！？!?\n]{0,6}(?:背后|背後)[^。！？!?\n]{0,6}(?:推理|思考|思[维維])`,
		},
		{
			ID: "en-decrypt-reasoning", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: false, Negation: ModerationNegationEnglish,
			Hints:   []string{"encrypted", "reasoning", "redacted", "thinking"},
			Pattern: `\b(?:decrypt|decipher|deobfuscate|crack|unscramble)\w*\b.{0,60}?\b(?:encrypted[\s_]*content|encrypted\s+(?:reasoning|thinking|thoughts?|cot|chain[\s-]*of[\s-]*thought)|reasoning\s+(?:item|block|blob)s?|redacted[\s_]*thinking|thinking\s+signatures?)\b|\b(?:transcribe|decode|reveal|recover)\w*\b.{0,40}?\b(?:encrypted[\s_]*content|encrypted\s+(?:reasoning|thinking))\b`,
		},
		{
			ID: "zh-decrypt-reasoning", Group: ModerationPresetGroupLower, DefaultOn: true, CaseSensitive: false, Negation: ModerationNegationChinese,
			Hints:   []string{"加密", "encrypted_content", "推理块", "推理塊"},
			Pattern: `(?:解密|破解)[^。！？!?\n]{0,20}(?:加密的?(?:推理|思考|思[维維])|encrypted_content|推理(?:块|塊))|(?:转录|轉錄|还原|還原)[^。！？!?\n]{0,20}(?:加密的?(?:推理|思考|思[维維])|encrypted_content)`,
		},
		{
			ID: "en-reasoning-trace", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: false, Negation: ModerationNegationNone,
			Hints:   []string{"trace"},
			Pattern: `\b(?:your|its|the\s+model'?s)\s+(?:[\w-]+\s+){0,2}?reasoning[\s-]*traces?\b`,
		},
		{
			ID: "zh-reasoning-trace", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: true, Negation: ModerationNegationNone,
			Hints:   []string{"轨迹", "軌跡", "痕迹", "痕跡"},
			Pattern: `(?:你|您)(?:自己)?的[^。！？!?\n]{0,4}?(?:推理|思[维維考])(?:轨迹|軌跡|痕迹|痕跡)`,
		},
		{
			ID: "en-thinking-language-consistent", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: false, Negation: ModerationNegationNone,
			Hints:   []string{"language"},
			Pattern: `\blanguage\s+(?:used\s+)?in\s+(?:the\s+|your\s+)?(?:thinking|reasoning|thoughts?)(?:\s+content|\s+process)?\b.{0,40}?\b(?:must|should|has\s+to|needs\s+to)\b.{0,40}?\b(?:consistent|same|match)`,
		},
		{
			ID: "zh-thinking-language-consistent", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: true, Negation: ModerationNegationNone,
			Hints:   []string{"语言", "語言"},
			Pattern: `思考(?:内容|內容|过程|過程)?[^。！？!?\n]{0,6}(?:语言|語言)[^。！？!?\n]{0,12}(?:必须|必須)[^。！？!?\n]{0,8}(?:与|與|和)[^。！？!?\n]{0,16}(?:严格|嚴格)?(?:一致|相同)`,
		},
		{
			ID: "en-r1-boxed-template", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: false, Negation: ModerationNegationNone,
			Hints:   []string{"boxed"},
			Pattern: `reason\s+step\s+by\s+step,?\s+and\s+put\s+your\s+final\s+answer\s+within\s+\\+boxed`,
		},
		{
			ID: "zh-r1-boxed-template", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: true, Negation: ModerationNegationNone,
			Hints:   []string{"boxed"},
			Pattern: `请?(?:一步步|逐步|一步一步)地?推理[^。\n]{0,20}(?:最终答案|最終答案)[^。\n]{0,12}\\+boxed`,
		},
		{
			ID: "en-think-tags", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: false, Negation: ModerationNegationEnglish,
			Hints:   []string{"<"},
			Pattern: `\b(?:put|place|wrap|enclose|write|output|include)\b.{0,50}?\b(?:reasoning|thinking|thoughts|thought\s+process|chain[\s-]*of[\s-]*thought)\b.{0,50}?<\s*/?\s*(?:think|thinking|thought|reasoning|scratchpad|analysis)\s*>`,
		},
		{
			ID: "en-training-data-for-model", Group: ModerationPresetGroupHigher, DefaultOn: false, CaseSensitive: false, Negation: ModerationNegationNone,
			Hints:   []string{"data"},
			Pattern: `\b(?:training\s+data|dataset|sft\s+data|fine[\s-]*tun\w*\s+data|distillation\s+data)\b.{0,40}?\b(?:to\s+train|to\s+fine[\s-]*tune|to\s+distill|for\s+training)\b.{0,30}?\b(?:our|my|a|another|the\s+student|student)\s+(?:own\s+)?(?:model|llm)`,
		},
	}
}

// ModerationRulePresetByID returns the built-in rule with that ID.
func ModerationRulePresetByID(id string) (ModerationRulePreset, bool) {
	for _, preset := range ModerationRulePresets() {
		if preset.ID == id {
			return preset, true
		}
	}
	return ModerationRulePreset{}, false
}

// EffectiveModerationMaxChars is ModerationMaxChars, or
// ModerationMaxCharsDefault when it is unset or out of range.
func (s *ChannelOtherSettings) EffectiveModerationMaxChars() int {
	if s == nil || s.ModerationMaxChars < ModerationMaxCharsMin || s.ModerationMaxChars > ModerationMaxCharsMax {
		return ModerationMaxCharsDefault
	}
	return s.ModerationMaxChars
}

// EffectiveModerationTimeout is ModerationTimeoutSeconds, or
// ModerationTimeoutSecondsDefault when it is unset or out of range.
func (s *ChannelOtherSettings) EffectiveModerationTimeout() time.Duration {
	if s == nil || s.ModerationTimeoutSeconds < ModerationTimeoutSecondsMin || s.ModerationTimeoutSeconds > ModerationTimeoutSecondsMax {
		return ModerationTimeoutSecondsDefault * time.Second
	}
	return time.Duration(s.ModerationTimeoutSeconds) * time.Second
}

// EffectiveModerationRulePresets is the channel's preset IDs in list order;
// nil when none was set or the list is empty.
func (s *ChannelOtherSettings) EffectiveModerationRulePresets() []string {
	if s == nil || s.ModerationRulePresets == nil || len(*s.ModerationRulePresets) == 0 {
		return nil
	}
	return append([]string(nil), (*s.ModerationRulePresets)...)
}

// ValidateModeration rejects moderation settings the relay cannot run: a
// budget or timeout out of range, unknown or repeated categories and preset
// IDs, and custom rules that are malformed, repeated, invalid, too complex or
// would match every text. It checks them whether or not moderation is on.
func (s *ChannelOtherSettings) ValidateModeration() error {
	if s == nil {
		return nil
	}
	if v := s.ModerationMaxChars; v != 0 && (v < ModerationMaxCharsMin || v > ModerationMaxCharsMax) {
		return fmt.Errorf("moderation_max_chars must be 0 or between %d and %d", ModerationMaxCharsMin, ModerationMaxCharsMax)
	}
	if v := s.ModerationTimeoutSeconds; v != 0 && (v < ModerationTimeoutSecondsMin || v > ModerationTimeoutSecondsMax) {
		return fmt.Errorf("moderation_timeout_seconds must be 0 or between %d and %d", ModerationTimeoutSecondsMin, ModerationTimeoutSecondsMax)
	}
	for i, category := range s.ModerationCategories {
		if !slices.Contains(ModerationCategoryNames, category) {
			return fmt.Errorf("moderation_categories[%d] %q is not an OpenAI moderation category", i, category)
		}
		if slices.Contains(s.ModerationCategories[:i], category) {
			return fmt.Errorf("moderation_categories[%d] %q is listed more than once", i, category)
		}
	}
	if s.ModerationRulePresets != nil {
		presets := *s.ModerationRulePresets
		for i, id := range presets {
			if _, ok := ModerationRulePresetByID(id); !ok {
				return fmt.Errorf("moderation_rule_presets[%d] %q is not a built-in preset", i, id)
			}
			if slices.Contains(presets[:i], id) {
				return fmt.Errorf("moderation_rule_presets[%d] %q is listed more than once", i, id)
			}
		}
	}
	rules := s.ModerationRules
	if len(rules) > ModerationRulesMax {
		return fmt.Errorf("moderation_rules must have at most %d rules", ModerationRulesMax)
	}
	regexRules := 0
	for _, rule := range rules {
		if rule.Type == ModerationRuleTypeRegex {
			regexRules++
		}
	}
	if regexRules > ModerationRegexRulesMax {
		return fmt.Errorf("moderation_rules must have at most %d regex rules", ModerationRegexRulesMax)
	}
	for i := range rules {
		if err := validateModerationRule(rules, i); err != nil {
			return err
		}
	}
	return nil
}

// validateModerationRule checks rules[i] against itself and the rules before it.
func validateModerationRule(rules []ModerationRule, i int) error {
	rule := rules[i]
	if rule.Type != ModerationRuleTypeKeyword && rule.Type != ModerationRuleTypeRegex {
		return fmt.Errorf("moderation_rules[%d].type must be keyword or regex", i)
	}
	if rule.Name != strings.TrimSpace(rule.Name) {
		return fmt.Errorf("moderation_rules[%d].name must not start or end with spaces", i)
	}
	if utf8.RuneCountInString(rule.Name) > ModerationRuleNameMax {
		return fmt.Errorf("moderation_rules[%d].name must be at most %d characters", i, ModerationRuleNameMax)
	}
	switch {
	case rule.Pattern == "":
		return fmt.Errorf("moderation_rules[%d].pattern must not be empty", i)
	case rule.Pattern != strings.TrimSpace(rule.Pattern):
		return fmt.Errorf("moderation_rules[%d].pattern must not start or end with spaces", i)
	case utf8.RuneCountInString(rule.Pattern) > ModerationRulePatternMax:
		return fmt.Errorf("moderation_rules[%d].pattern must be at most %d characters", i, ModerationRulePatternMax)
	case ModerationRuleView(NormalizeModerationText(rule.Pattern)) == "":
		return fmt.Errorf("moderation_rules[%d].pattern must contain a visible character", i)
	}
	for j, earlier := range rules[:i] {
		if earlier.Type != rule.Type {
			continue
		}
		repeated := earlier.Pattern == rule.Pattern
		if rule.Type == ModerationRuleTypeKeyword {
			repeated = strings.EqualFold(earlier.Pattern, rule.Pattern)
		}
		if repeated {
			return fmt.Errorf("moderation_rules[%d].pattern repeats moderation_rules[%d]", i, j)
		}
	}
	if rule.Type != ModerationRuleTypeRegex {
		return nil
	}
	re, err := regexp.Compile(moderationRegexSource(rule.Pattern, rule.CaseSensitive))
	if err != nil {
		return fmt.Errorf("moderation_rules[%d].pattern is not a valid regular expression: %s", i, cutModerationError(err.Error()))
	}
	if moderationRegexProgramSize(rule.Pattern, rule.CaseSensitive) > ModerationRegexProgramMax {
		return fmt.Errorf("moderation_rules[%d].pattern is too complex (more than %d regular expression instructions)", i, ModerationRegexProgramMax)
	}
	if re.MatchString("") {
		return fmt.Errorf("moderation_rules[%d].pattern matches empty text", i)
	}
	if text := moderationRegexUnmatchableText(rule.Pattern, rule.CaseSensitive); text != "" {
		return fmt.Errorf("moderation_rules[%d].pattern contains %q, which content moderation normalises before matching (write the half-width form, e.g. ! ? , ( ) instead of ！？，（）, and a single space instead of tabs or full-width spaces)", i, text)
	}
	return nil
}

// moderationRegexUnmatchableText is text a regex requires that the rule view
// of normalised text never contains: a literal rune the normalisation
// rewrites or drops (full-width forms, tabs, format characters, combining
// marks), two whitespace runes in a row, or a character class made only of
// such runes. It returns "" when there is none. The pattern is walked as RE2
// parses it, so escapes count and a class with a live alternative such as
// [！!] does not.
func moderationRegexUnmatchableText(pattern string, caseSensitive bool) string {
	parsed, err := syntax.Parse(moderationRegexSource(pattern, caseSensitive), syntax.Perl)
	if err != nil {
		return ""
	}
	stack := []*syntax.Regexp{parsed}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch node.Op {
		case syntax.OpLiteral:
			for j, r := range node.Rune {
				if !moderationRuleViewKeeps(r) {
					return string(r)
				}
				if j > 0 && unicode.IsSpace(r) && unicode.IsSpace(node.Rune[j-1]) {
					return string(node.Rune[j-1 : j+1])
				}
			}
		case syntax.OpCharClass:
			if len(node.Rune) > 0 && moderationClassIsUnmatchable(node.Rune) {
				return string(node.Rune[0])
			}
		}
		for j := len(node.Sub) - 1; j >= 0; j-- {
			stack = append(stack, node.Sub[j])
		}
	}
	return ""
}

// moderationRuleViewKeeps reports whether r survives normalisation and the
// rule view unchanged between two letters.
func moderationRuleViewKeeps(r rune) bool {
	text := "a" + string(r) + "a"
	return ModerationRuleView(NormalizeModerationText(text)) == text
}

// moderationClassIsUnmatchable reports whether every rune of a parsed
// character class (lo-hi pairs) is one the rule view never contains. It
// gives up, treating the class as matchable, after 1<<16 runes.
func moderationClassIsUnmatchable(ranges []rune) bool {
	checked := 0
	for j := 0; j+1 < len(ranges); j += 2 {
		for r := ranges[j]; r <= ranges[j+1]; r++ {
			if moderationRuleViewKeeps(r) {
				return false
			}
			checked++
			if checked >= 1<<16 {
				return false
			}
		}
	}
	return true
}

// CompileModerationRegex compiles a moderation regex as the rules engine runs
// it ("(?i)" + pattern unless caseSensitive). It refuses a program larger
// than ModerationRegexProgramMax.
func CompileModerationRegex(pattern string, caseSensitive bool) (*regexp.Regexp, error) {
	re, err := regexp.Compile(moderationRegexSource(pattern, caseSensitive))
	if err != nil {
		return nil, err
	}
	if moderationRegexProgramSize(pattern, caseSensitive) > ModerationRegexProgramMax {
		return nil, fmt.Errorf("more than %d regular expression instructions", ModerationRegexProgramMax)
	}
	return re, nil
}

func moderationRegexSource(pattern string, caseSensitive bool) string {
	if caseSensitive {
		return pattern
	}
	return "(?i)" + pattern
}

// moderationRegexProgramSize is the number of instructions of the compiled
// RE2 program; -1 when the pattern does not compile.
func moderationRegexProgramSize(pattern string, caseSensitive bool) int {
	parsed, err := syntax.Parse(moderationRegexSource(pattern, caseSensitive), syntax.Perl)
	if err != nil {
		return -1
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return -1
	}
	return len(prog.Inst)
}

// cutModerationError keeps a regexp error, which quotes the whole pattern,
// readable: at most 200 bytes, cut at a rune boundary with "…" appended.
func cutModerationError(msg string) string {
	const maxBytes = 200
	if len(msg) <= maxBytes {
		return msg
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

// NormalizeModerationText is the text normalisation that content moderation
// applies before rules, hashing and the call to OpenAI: NFKC; tag characters
// (U+E0020–U+E007E) become their ASCII twin and every other format,
// variation-selector or default-ignorable rune is dropped; every run of
// whitespace becomes one "\n" when it holds a line break, else one space;
// the ends are trimmed.
func NormalizeModerationText(text string) string {
	text = norm.NFKC.String(text)
	var b strings.Builder
	b.Grow(len(text))
	pendingSpace, pendingNewline := false, false
	for _, r := range text {
		if r >= 0xE0020 && r <= 0xE007E {
			r -= 0xE0000
		} else if unicode.In(r, unicode.Cf, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point) {
			continue
		}
		if unicode.IsSpace(r) {
			if r == '\n' || r == '\r' {
				pendingNewline = true
			} else {
				pendingSpace = true
			}
			continue
		}
		if b.Len() > 0 {
			if pendingNewline {
				b.WriteByte('\n')
			} else if pendingSpace {
				b.WriteByte(' ')
			}
		}
		pendingSpace, pendingNewline = false, false
		b.WriteRune(r)
	}
	return b.String()
}

// ModerationRuleView is the text the rules match: normalised text without
// nonspacing marks, so combining marks added to every letter cannot split a
// keyword. OpenAI still receives the marks.
func ModerationRuleView(normalised string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, normalised)
}
