package model

import (
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/constant"
)

// Pattern fragments. Names are matched lower-cased, without a leading [line] tag.
const (
	segStart  = `(?:^|[^a-z0-9])` // start of a name segment
	idStart   = `(?:^|[/:])`      // start of the id, or after org/ or ft:
	pathStart = `(?:^|[/.:~])`    // also after Bedrock/OCI "org." and OpenRouter "~"
)

type vendorRuleSpec struct {
	vendor   string // "" stops matching with no vendor
	org      bool   // creator-org or host row: also tried on the whole name of an org/model id
	patterns []string
}

// defaultVendorRuleSpecs is ordered: the first row that matches wins.
var defaultVendorRuleSpecs = []vendorRuleSpec{
	// R01: stop: GPT-J/Neo/NeoX (EleutherAI), GPT-SoVITS, GPT-SW3, GPT4All are not OpenAI
	{"", false, []string{
		segStart + `gpt-(?:j|neo|neox|sovits|sw3)(?:-|$)`,
		segStart + `gpt4all`,
	}},
	// R02: image models; text models before Meta llama
	{"NovelAI", false, []string{
		`nai-diffusion`,
		`novelai`,
		segStart + `erato-v\d`,
		pathStart + `(?:kayra|clio)-v\d`,
	}},
	// R03: before Alibaba (360zhinao-turbo-qwen-plus) and OpenAI (360gpt2-o1)
	{"360", false, []string{segStart + `360(?:gpt|zhinao)`}},
	// R04: before Alibaba/Meta: deepseek-r1-distill-qwen/llama
	{"DeepSeek", false, []string{`deepseek`}},
	// R05: before Google: gemini-claude-* (Antigravity) is Claude
	{"Anthropic", false, []string{`claude`}},
	// R06: Gemini, Gemma, Imagen, Veo, Lyria, Nano Banana, PaLM, Google embeddings, agents
	{"Google", false, []string{
		`gemini`,
		pathStart + `(?:code|recurrent|pali|shield|med|tx|data|embedding|t5|vault|diffusion|function|translate|sign|dolphin)?gemma(?:\d|[-:_]|$)`,
		`^(?:.*/)?databricks-gemma-`,
		pathStart + `imagen(?:-?\d|-product)`,
		pathStart + `image(?:generation|text)@`,
		pathStart + `veo[-_]?\d`,
		pathStart + `lyria-`,
		pathStart + `nano-banana`,
		pathStart + `(?:learn|med)lm-`,
		pathStart + `chirp(?:$|_\d|-\d+-hd|-hd)`,
		pathStart + `(?:palm-?2|(?:text|chat|code|codechat)-bison|(?:code|embedding)-gecko|textembedding-gecko|text-unicorn|multimodalembedding)`,
		pathStart + `(?:text-(?:multilingual-)?embedding-(?:00\d|preview-0409|large-exp)|embedding-001$)`,
		`(?:^|/)(?:deep-research-(?:pro-|max-)?preview|antigravity-preview)`,
		`^(?:aqa|genie-\d+)$`,
		`(?:^|/)virtual-try-on-`,
	}},
	// R07: before 即梦: dreamina-seedance-* is a Seedance model
	{"字节跳动", false, []string{
		segStart + `doubao`,
		segStart + `seed(?:ream|edit|ance)`,
		segStart + `seed-(?:tts|icl|oss|asr|audio|\d)`,
		segStart + `skylark`,
		segStart + `ui-tars`,
		segStart + `omni-?human`,
	}},
	// R08: Muse before 讯飞 spark
	{"Meta", false, []string{segStart + `muse[-_](?:spark|wonder|image|video|ember|glimmer|voice)`}},
	// R09
	{"Moonshot", false, []string{segStart + `(?:moonshot|kimi)`}},
	// R10
	{"智谱", false, []string{
		segStart + `(?:chat|char|auto)?glm`,
		segStart + `codegeex`,
		segStart + `cog(?:view|video)`,
		idStart + `embedding-[23]$`,
		segStart + `emohaa`,
	}},
	// R11: tongyi before 零一万物 yi-
	{"阿里巴巴", false, []string{
		segStart + `(?:code)?(?:qwen|qwq|qvq)`,
		segStart + `(?:tongyi|wanx)`,
		segStart + `wan-?v?\d`,
		segStart + `text-embedding-(?:async-)?v\d`,
		idStart + `multimodal-embedding-v\d`,
		segStart + `gte-rerank`,
		segStart + `(?:cosyvoice|paraformer|sambert|farui|sensevoice|happyhorse)`,
		idStart + `z-image`,
	}},
	// R12
	{"MiniMax", false, []string{
		segStart + `(?:minimax|abab)`,
		segStart + `hailuo`,
		idStart + `speech-\d`,
		idStart + `(?:t2v|i2v|s2v|video)-0\d`,
		idStart + `image-01`,
		idStart + `m2-her`,
		idStart + `embo-0\d`,
		idStart + `music-(?:0\d|\d\.\d)`,
	}},
	// R13
	{"百度", false, []string{
		segStart + `ernie`,
		idStart + `embedding-v1$`,
		segStart + `tao-8k`,
		segStart + `qianfan`,
		segStart + `musesteamer`,
	}},
	// R14: never a bare spark
	{"讯飞", false, []string{
		segStart + `sparkdesk`,
		segStart + `spark-?(?:lite|pro|max|ultra|x\d|v?\d)`,
		idStart + `generalv\d`,
		idStart + `(?:4\.0ultra|pro-128k|max-32k)$`,
	}},
	// R15
	{"腾讯", false, []string{
		segStart + `hunyuan`,
		idStart + `hy\d`,
		idStart + `hy-(?:image|video|t1|turbo|mt\d?|3d|world)`,
		segStart + `youtu-`,
		idStart + `yt-video-`,
	}},
	// R16: idStart keeps amazon.rerank-v1:0 out
	{"Cohere", false, []string{
		`cohere`,
		segStart + `command(?:-|$)`,
		segStart + `(?:c4ai-)?aya-`,
		idStart + `(?:embed|rerank)-(?:english|multilingual|v-?\d)`,
	}},
	// R17: never a bare yi
	{"零一万物", false, []string{segStart + `yi-`}},
	// R18
	{"Jina", false, []string{
		segStart + `jina`,
		pathStart + `readerlm-`,
	}},
	// R19
	{"Mistral", false, []string{segStart + `(?:mistral|mixtral|codestral|pixtral|ministral|magistral|devstral|voxtral|mathstral|leanstral)`}},
	// R20: never a bare xai (MiniMaxAI/)
	{"xAI", false, []string{segStart + `grok`}},
	// R21: segStart keeps tinyllama/ollama out
	{"Meta", false, []string{segStart + `(?:code-?)?llama`}},
	// R22: segStart keeps inkling out
	{"快手", false, []string{
		segStart + `kling`,
		segStart + `kolors`,
		segStart + `kwai`,
		segStart + `kat-(?:coder|dev)`,
	}},
	// R23
	{"即梦", false, []string{segStart + `(?:jimeng|dreamina)`}},
	// R24: after 即梦: bytedance/dreamina-3.1 is the Dreamina brand
	{"字节跳动", false, []string{`bytedance`}},
	// R25
	{"Vidu", false, []string{segStart + `vidu`}},
	// R26: after every Chinese vendor (kling-image/o1, 360gpt2-o1); o-series only at the start of the id, after org/ or ft:
	{"OpenAI", false, []string{
		segStart + `(?:chat)?gpt-`,
		`(?:^|/)(?:chat)?gpt-?\d`,
		`(?:^|/)chatgpt$`,
		idStart + `o[1-9](?:-|$)`,
		`(?:^|/)tts-1(?:-|$)`,
		`(?:^|/)tts(?:-hd)?$`,
		`(?:^|/)whisper(?:-|$)`,
		`(?:^|/)dall-?e`,
		`(?:^|/)sora(?:[-_]|\d|$)`,
		`(?:^|/)codex-`,
		`(?:^|/)computer-use-preview`,
		`(?:^|/)text-embedding-(?:3-(?:small|large)|ada-002)`,
		`(?:^|/)(?:omni|text)-moderation-`,
		idStart + `(?:babbage|davinci)-00[12](?:$|:)`,
		`(?:^|/)(?:text|code)-(?:davinci|curie|babbage|ada|cushman|search|similarity)-`,
		`(?:^|/)(?:chat-latest|daybreak-(?:blue|red)-latest)$`,
		`(?:^|/)(?:davinci|curie|babbage|ada)(?:$|:)`,
		`(?:^|/)openai-`,
	}},
	// R27: creator org
	{"Meta", true, []string{`(?:^|/)(?:meta-llama|meta|facebook)/`}},
	// R28: creator org
	{"Mistral", true, []string{`(?:^|/)mistral(?:ai)?/`}},
	// R29: creator org
	{"xAI", true, []string{`(?:^|/)x-?ai/`}},
	// R30: creator org
	{"Cohere", true, []string{`(?:^|/)(?:cohere|cohereforai|coherelabs)/`}},
	// R31: creator org
	{"Jina", true, []string{`(?:^|/)jina-?ai/`}},
	// R32: creator org
	{"DeepSeek", true, []string{`(?:^|/)deepseek(?:-ai)?/`}},
	// R33: creator org
	{"阿里巴巴", true, []string{`(?:^|/)qwen/`}},
	// R34: creator org
	{"智谱", true, []string{`(?:^|/)(?:zai-org|thudm)/`}},
	// R35: creator org
	{"Moonshot", true, []string{`(?:^|/)moonshotai/`}},
	// R36: creator org
	{"MiniMax", true, []string{`(?:^|/)minimax(?:ai)?/`}},
	// R37: creator org
	{"零一万物", true, []string{`(?:^|/)01-ai/`}},
	// R38: creator org (google/, google., @cf/google/)
	{"Google", true, []string{`(?:^|[/@~])google[/.]`}},
	// R39: creator org
	{"字节跳动", true, []string{`(?:^|/)bytedance(?:-seed)?/`}},
	// R40: creator org (openai/, Bedrock openai., @cf/openai/)
	{"OpenAI", true, []string{`(?:^|[/.@])openai[/.]`}},
	// R41: host: only when no creator matched
	{"Cloudflare", true, []string{`(?:^|/)@(?:cf|hf)/`}},
}

type vendorRule struct {
	vendor  string
	org     bool
	pattern *regexp.Regexp
}

// inferredVendorCacheLimit bounds the memo of inferDefaultVendor. The table
// is fixed at build time, so a name's vendor never changes in a process.
const inferredVendorCacheLimit = 20000

var (
	modelLineTagPattern = regexp.MustCompile(`^\[[^\]]+\]`)
	defaultVendorRules  = compileVendorRules(defaultVendorRuleSpecs) // panics at init on a bad pattern; a test compiles it too

	inferredVendorMu    sync.Mutex
	inferredVendorCache = make(map[string]string) // lower-cased tag-free name -> vendor ("" = none)
)

func compileVendorRules(specs []vendorRuleSpec) []vendorRule {
	rules := make([]vendorRule, len(specs))
	for i, spec := range specs {
		rules[i] = vendorRule{vendor: spec.vendor, org: spec.org, pattern: regexp.MustCompile(strings.Join(spec.patterns, "|"))}
	}
	return rules
}

// matchVendorRules returns the vendor of the first rule that matches name
// (only org rules when orgOnly) and whether a rule matched; a stop rule
// matches with vendor "".
func matchVendorRules(name string, orgOnly bool) (string, bool) {
	for _, rule := range defaultVendorRules {
		if orgOnly && !rule.org {
			continue
		}
		if rule.pattern.MatchString(name) {
			return rule.vendor, true
		}
	}
	return "", false
}

// inferDefaultVendor names the built-in vendor of a model without metadata
// (or whose record has no vendor), or "" when no rule names one. The name is matched lower-cased, without a
// leading [line] tag. An org/model id (exactly one "/", not first, no leading
// "@") is matched on the model id first; only when no rule matches it are the
// org rules tried on the whole name, so a host prefix (llamagate/, qwencloud/,
// mistral/) never decides. Results are memoised per name.
func inferDefaultVendor(modelName string) string {
	name := strings.ToLower(modelLineTagPattern.ReplaceAllString(modelName, ""))
	inferredVendorMu.Lock()
	vendor, ok := inferredVendorCache[name]
	inferredVendorMu.Unlock()
	if ok {
		return vendor
	}
	if slash := strings.IndexByte(name, '/'); slash > 0 && slash == strings.LastIndexByte(name, '/') && name[0] != '@' {
		var matched bool
		if vendor, matched = matchVendorRules(name[slash+1:], false); !matched {
			vendor, _ = matchVendorRules(name, true)
		}
	} else {
		vendor, _ = matchVendorRules(name, false)
	}
	inferredVendorMu.Lock()
	if len(inferredVendorCache) >= inferredVendorCacheLimit {
		clear(inferredVendorCache)
	}
	inferredVendorCache[name] = vendor
	inferredVendorMu.Unlock()
	return vendor
}

// 供应商默认图标映射
var defaultVendorIcons = map[string]string{
	"OpenAI":     "OpenAI",
	"Anthropic":  "Claude.Color",
	"Google":     "Gemini.Color",
	"Moonshot":   "Moonshot",
	"智谱":         "Zhipu.Color",
	"阿里巴巴":       "Qwen.Color",
	"DeepSeek":   "DeepSeek.Color",
	"MiniMax":    "Minimax.Color",
	"百度":         "Wenxin.Color",
	"讯飞":         "Spark.Color",
	"腾讯":         "Hunyuan.Color",
	"Cohere":     "Cohere.Color",
	"Cloudflare": "Cloudflare.Color",
	"360":        "Ai360.Color",
	"零一万物":       "Yi.Color",
	"Jina":       "Jina",
	"Mistral":    "Mistral.Color",
	"xAI":        "XAI",
	"Meta":       "Ollama",
	"字节跳动":       "Doubao.Color",
	"快手":         "Kling.Color",
	"即梦":         "Jimeng.Color",
	"Vidu":       "Vidu",
	"NovelAI":    "NovelAI",
	"微软":         "AzureAI",
	"Microsoft":  "AzureAI",
	"Azure":      "AzureAI",
}

// defaultChannelTypeVendors names the vendor of a channel type that serves
// only that vendor's models. It applies when no name rule matches, so a model
// renamed on a NovelAI channel still lists under NovelAI.
var defaultChannelTypeVendors = map[int]string{
	constant.ChannelTypeNovelAI: "NovelAI",
}

// initDefaultVendorMapping gives every enabled model without a metadata
// record an in-memory record whose vendor is the one its name names
// (inferDefaultVendor), else the vendor of a single-vendor channel type that
// serves it. A saved record keeps its saved vendor; a record saved without a
// vendor takes the vendor its name names, never a channel type's.
func initDefaultVendorMapping(metaMap map[string]*Model, vendorMap map[int]*Vendor, enableAbilities []AbilityWithChannel) {
	inferred := make(map[string]bool)
	for _, ability := range enableAbilities {
		modelName := ability.Model
		channelVendor := defaultChannelTypeVendors[ability.ChannelType]
		if meta, exists := metaMap[modelName]; exists {
			switch {
			case inferred[modelName]:
				// A model inferred without a vendor from an earlier ability takes
				// the vendor of a single-vendor channel that also serves it.
				if meta.VendorID == 0 && channelVendor != "" {
					meta.VendorID = getDisplayVendor(channelVendor, vendorMap)
				}
			case meta.VendorID == 0:
				// A record saved without a vendor (made only to add a
				// description, say) must not hide the vendor its name names.
				// resolveModelMetadata shares one record among all the names a
				// prefix, suffix or contains record matches, so the vendor goes
				// on a copy for this name only.
				if vendorName := inferDefaultVendor(modelName); vendorName != "" {
					record := *meta
					record.VendorID = getDisplayVendor(vendorName, vendorMap)
					metaMap[modelName] = &record
				}
			}
			continue
		}

		// 匹配供应商
		vendorID := 0
		if vendorName := inferDefaultVendor(modelName); vendorName != "" {
			vendorID = getDisplayVendor(vendorName, vendorMap)
		}
		if vendorID == 0 && channelVendor != "" {
			vendorID = getDisplayVendor(channelVendor, vendorMap)
		}

		// 创建模型元数据
		metaMap[modelName] = &Model{
			ModelName: modelName,
			VendorID:  vendorID,
			Status:    1,
			NameRule:  NameRuleExact,
		}
		inferred[modelName] = true
	}
}

// Default vendor entries are presentation data. Reading pricing must never
// recreate a deleted/merged database record.
var defaultVendorDisplayIDs = map[string]int{
	"360":        -1001,
	"Anthropic":  -1002,
	"Cloudflare": -1003,
	"Cohere":     -1004,
	"DeepSeek":   -1005,
	"Google":     -1006,
	"Jina":       -1007,
	"Meta":       -1008,
	"MiniMax":    -1009,
	"Mistral":    -1010,
	"Moonshot":   -1011,
	"OpenAI":     -1012,
	"Vidu":       -1013,
	"xAI":        -1014,
	"即梦":         -1015,
	"字节跳动":       -1016,
	"快手":         -1017,
	"智谱":         -1018,
	"百度":         -1019,
	"腾讯":         -1020,
	"讯飞":         -1021,
	"阿里巴巴":       -1022,
	"零一万物":       -1023,
	"NovelAI":    -1024,
}

// BuiltinVendor is a vendor the catalog infers from model names or channel
// types without a saved record. Choosing one saves an ordinary vendor record
// with this icon; its display ID is never stored on a model.
type BuiltinVendor struct {
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// BuiltinVendors lists the inferred vendors in display ID order.
func BuiltinVendors() []BuiltinVendor {
	names := make([]string, 0, len(defaultVendorDisplayIDs))
	for name := range defaultVendorDisplayIDs {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		return defaultVendorDisplayIDs[b] - defaultVendorDisplayIDs[a]
	})
	vendors := make([]BuiltinVendor, 0, len(names))
	for _, name := range names {
		vendors = append(vendors, BuiltinVendor{Name: name, Icon: getDefaultVendorIcon(name)})
	}
	return vendors
}

// builtinVendorAliases lists the names the official metadata sync (basellm
// llm-metadata vendors.json) gives a built-in vendor's company. A saved vendor
// of one of these names stands for the built-in vendor, so models inferred by
// name list under the synced company instead of a second vendor entry.
// Platforms that also host other companies' models (Tencent TokenHub,
// Volcengine Ark, Thinking Machines) are never aliases.
var builtinVendorAliases = map[string][]string{
	"阿里巴巴":     {"Alibaba", "Alibaba (China)", "Alibaba Token Plan"},
	"智谱":       {"Z.AI", "Z.AI Coding Plan"},
	"Moonshot": {"Moonshot AI"},
	"MiniMax":  {"MiniMax (minimax.io)", "MiniMax (minimax.cn)", "MiniMax Token Plan (minimax.io)"},
}

func getDisplayVendor(vendorName string, vendorMap map[int]*Vendor) int {
	for id, vendor := range vendorMap {
		if strings.EqualFold(vendor.Name, vendorName) {
			return id
		}
	}
	// A saved vendor (positive id) named like the company, in alias order.
	for _, alias := range builtinVendorAliases[vendorName] {
		for id, vendor := range vendorMap {
			if id > 0 && strings.EqualFold(vendor.Name, alias) {
				return id
			}
		}
	}
	id := defaultVendorDisplayIDs[vendorName]
	if id == 0 {
		return 0
	}
	vendorMap[id] = &Vendor{Id: id, Name: vendorName, Status: 1, Icon: getDefaultVendorIcon(vendorName)}
	return id
}

// 获取供应商默认图标
func getDefaultVendorIcon(vendorName string) string {
	if icon, exists := defaultVendorIcons[vendorName]; exists {
		return icon
	}
	return ""
}

// dedupePricingVendors lists each vendor name once. Releases that created
// vendor rows while reading pricing could insert the same name more than once
// when refreshes overlapped, so a database may hold several active rows per
// name. The oldest row stands for the name (its empty icon or description is
// filled from a duplicate); aliases maps every other id to it. The list is
// ordered by id, saved vendors before built-in display vendors, so it no
// longer reshuffles on every refresh.
func dedupePricingVendors(vendorMap map[int]*Vendor) ([]PricingVendor, map[int]int) {
	ids := make([]int, 0, len(vendorMap))
	for id := range vendorMap {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int) int {
		if (a > 0) != (b > 0) {
			if a > 0 {
				return -1
			}
			return 1
		}
		if a > 0 {
			return a - b
		}
		return b - a
	})

	list := make([]PricingVendor, 0, len(ids))
	aliases := make(map[int]int)
	byName := make(map[string]int, len(ids))
	for _, id := range ids {
		vendor := vendorMap[id]
		key := strings.ToLower(strings.TrimSpace(vendor.Name))
		if index, seen := byName[key]; seen {
			kept := &list[index]
			aliases[id] = kept.ID
			if kept.Icon == "" {
				kept.Icon = vendor.Icon
			}
			if kept.Description == "" {
				kept.Description = vendor.Description
			}
			continue
		}
		byName[key] = len(list)
		list = append(list, PricingVendor{
			ID:          vendor.Id,
			Name:        vendor.Name,
			Description: vendor.Description,
			Icon:        vendor.Icon,
		})
	}
	return list, aliases
}
