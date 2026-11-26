package model

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetInferredVendorCache empties the memo of inferDefaultVendor, so a test
// sees the rules and not what an earlier test left in the memo.
func resetInferredVendorCache(t *testing.T) {
	t.Helper()
	reset := func() {
		inferredVendorMu.Lock()
		clear(inferredVendorCache)
		inferredVendorMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func assertInferredVendors(t *testing.T, cases []struct{ name, vendor string }) {
	t.Helper()
	for _, tc := range cases {
		assert.Equal(t, tc.vendor, inferDefaultVendor(tc.name), tc.name)
	}
}

func TestDefaultVendorRulesCompile(t *testing.T) {
	require.NotPanics(t, func() { compileVendorRules(defaultVendorRuleSpecs) })
	require.Len(t, defaultVendorRules, len(defaultVendorRuleSpecs))
	orgRows := false
	for i, spec := range defaultVendorRuleSpecs {
		row := fmt.Sprintf("R%02d", i+1)
		require.NotEmpty(t, spec.patterns, row)
		for _, pattern := range spec.patterns {
			_, err := regexp.Compile(pattern)
			require.NoError(t, err, "%s: %s", row, pattern)
		}
		assert.Equal(t, strings.Join(spec.patterns, "|"), defaultVendorRules[i].pattern.String(), row)
		assert.Equal(t, spec.vendor, defaultVendorRules[i].vendor, row)
		assert.Equal(t, spec.org, defaultVendorRules[i].org, row)
		if spec.vendor != "" {
			// No new vendor: every row names a built-in vendor, with its display id and icon.
			assert.Contains(t, defaultVendorDisplayIDs, spec.vendor, row)
			assert.NotEmpty(t, getDefaultVendorIcon(spec.vendor), row)
		}
		// Creator-org and host rows come after every keyword row.
		if spec.org {
			orgRows = true
		} else {
			assert.False(t, orgRows, "%s: a keyword row after an org row", row)
		}
	}
	first, last := defaultVendorRuleSpecs[0], defaultVendorRuleSpecs[len(defaultVendorRuleSpecs)-1]
	assert.Equal(t, "", first.vendor, "the GPT-J/SoVITS/GPT4All stop row is first")
	assert.Equal(t, "Cloudflare", last.vendor, "the Cloudflare host row is last")
	assert.True(t, last.org)
}

// Names whose vendor is easy to get wrong, each checked by hand, and the
// model names of the owner's Model Square screenshots.
func TestInferDefaultVendor(t *testing.T) {
	resetInferredVendorCache(t)
	assertInferredVendors(t, []struct{ name, vendor string }{
		{"tts-1", "OpenAI"},
		{"[按次]tts-1-hd-1106", "OpenAI"},
		{"gpt-4o-mini-tts", "OpenAI"},
		{"whisper-1", "OpenAI"},
		{"whisper-large-v3-turbo", "OpenAI"},
		{"distil-whisper-large-v3-en", ""},
		{"faster-whisper-large-v3", ""},
		{"text-embedding-ada-002", "OpenAI"},
		{"text-embedding-3-small", "OpenAI"},
		{"text-embedding-v4", "阿里巴巴"},
		{"text-embedding-004", "Google"},
		{"gemini-embedding-001", "Google"},
		{"embedding-001", "Google"},
		{"embedding-3", "智谱"},
		{"Embedding-V1", "百度"},
		{"doubao-embedding-large", "字节跳动"},
		{"hunyuan-embedding", "腾讯"},
		{"tongyi-embedding-vision-plus", "阿里巴巴"},
		{"jina-embeddings-v3", "Jina"},
		{"embed-multilingual-v3.0", "Cohere"},
		{"mistral-embed", "Mistral"},
		{"bge-m3", ""},
		{"omni-moderation-latest", "OpenAI"},
		{"text-moderation-stable", "OpenAI"},
		{"mistral-moderation-2411", "Mistral"},
		{"dall-e-3", "OpenAI"},
		{"gpt-image-1", "OpenAI"},
		{"sora-2-pro", "OpenAI"},
		{"open-sora", ""},
		{"codex-mini-latest", "OpenAI"},
		{"gpt-5.3-codex-spark", "OpenAI"},
		{"o4-mini", "OpenAI"},
		{"o3-mini-2025-01-31-high", "OpenAI"},
		{"openai/o4-mini-high", "OpenAI"},
		{"azure/o1-mini", "OpenAI"},
		{"openai-o3", "OpenAI"},
		{"gpt4o-mini", "OpenAI"},
		{"chatgpt-4o-latest", "OpenAI"},
		{"chat-latest", "OpenAI"},
		{"computer-use-preview", "OpenAI"},
		{"gemini-2.5-computer-use-preview-10-2025", "Google"},
		{"davinci-002", "OpenAI"},
		{"ada", "OpenAI"},
		{"ft:gpt-4o-mini-2024-07-18:acme::abc123", "OpenAI"},
		{"gpt-sovits", ""},
		{"gpt-j-6b", ""},
		{"yandexgpt-lite", ""},
		{"360gpt-pro", "360"},
		{"360gpt2-o1", "360"},
		{"Yi-34B-Chat-GPTQ", "零一万物"},
		{"marco-o1", ""},
		{"hidream-o1-image", ""},
		{"veo3.1", "Google"},
		{"veo3-fast", "Google"},
		{"hailuo3", "MiniMax"},
		{"kling-video-o1", "快手"},
		{"fal-ai/kling-image/o1", "快手"},
		{"gemini-claude-opus-4-6-thinking", "Anthropic"},
		{"Qwen3.5-27B-Claude-4.6-Opus-Reasoning-Distilled", "Anthropic"},
		{"gemini-2.5-flash-preview-tts", "Google"},
		{"gemma-3-27b-it", "Google"},
		{"bge-multilingual-gemma2", ""},
		{"chirp_3", "Google"},
		{"chirp-v4", ""},
		{"imagen-4.0-generate-001", "Google"},
		{"nano-banana-pro", "Google"},
		{"aqa", "Google"},
		{"deep-research-pro-preview-12-2025", "Google"},
		{"o3-deep-research", "OpenAI"},
		{"sonar-deep-research", ""},
		{"seed-tts-2.0", "字节跳动"},
		{"doubao-seedream-4-0-250828", "字节跳动"},
		{"seedream-4.0", "字节跳动"},
		{"dreamina-seedance-2-0-260128", "字节跳动"},
		{"dreamina-3.1", "即梦"},
		{"jimeng_t2i_v40", "即梦"},
		{"ui-tars-1.5-7b", "字节跳动"},
		{"muse-spark-1.3", "Meta"},
		{"yi-spark", "零一万物"},
		{"sparkdesk-v3.5", "讯飞"},
		{"generalv3.5", "讯飞"},
		{"4.0Ultra", "讯飞"},
		{"spark-tts-0.5b", ""},
		{"vispark/vision-large", ""},
		{"inkling", ""},
		{"kling-v2-1-master", "快手"},
		{"Kwai-Kolors/Kolors", "快手"},
		{"kat-coder-pro-v1", "快手"},
		{"qwq-32b", "阿里巴巴"},
		{"wan2.6-t2v", "阿里巴巴"},
		{"cosyvoice-v2", "阿里巴巴"},
		{"sensevoice-v1", "阿里巴巴"},
		{"happyhorse-1.0-t2v", "阿里巴巴"},
		{"z-image-turbo", "阿里巴巴"},
		{"360zhinao-turbo-qwen-plus", "360"},
		{"deepseek-r1-distill-qwen-32b", "DeepSeek"},
		{"deepseek-r1-distill-llama-70b", "DeepSeek"},
		{"glm-4.5", "智谱"},
		{"chatglm_turbo", "智谱"},
		{"cogview-4-250304", "智谱"},
		{"zai-glm-5-2", "智谱"},
		{"kimi-k2-0905-preview", "Moonshot"},
		{"MiniMaxAI/MiniMax-M2.5", "MiniMax"},
		{"speech-02-hd", "MiniMax"},
		{"T2V-01-Director", "MiniMax"},
		{"image-01", "MiniMax"},
		{"music-2.0", "MiniMax"},
		{"ernie-4.5-turbo-128k", "百度"},
		{"musesteamer-2.0-turbo-i2v", "百度"},
		{"hy3", "腾讯"},
		{"hunyuan-t1-latest", "腾讯"},
		{"command-r-plus-08-2024", "Cohere"},
		{"c4ai-aya-expanse-32b", "Cohere"},
		{"rerank-v3.5", "Cohere"},
		{"embed-v-4-0", "Cohere"},
		{"amazon.rerank-v1:0", ""},
		{"readerlm-v2", "Jina"},
		{"codestral-2501", "Mistral"},
		{"mixtral-8x7b-32768", "Mistral"},
		{"grok-4-1-fast-reasoning", "xAI"},
		{"llama-3.3-70b-instruct", "Meta"},
		{"meta.llama3-1-70b-instruct-v1:0", "Meta"},
		{"tinyllama-1.1b-chat-v1.0", ""},
		{"ollama/vicuna", ""},
		{"hermes-3-llama-3.1-405b", "Meta"},
		{"llama-3.1-nemotron-70b-instruct", "Meta"},
		{"dolphin-mixtral-8x22b", "Mistral"},
		{"llama-3-erato-v1", "NovelAI"},
		{"@cf/meta/llama-3-8b-instruct", "Meta"},
		{"@cf/qwen/qwq-32b", "阿里巴巴"},
		{"@cf/openai/gpt-oss-120b", "OpenAI"},
		{"@cf/google/gemma-3-12b-it", "Google"},
		{"@cf/meta/m2m100-1.2b", "Meta"},
		{"@cf/deepgram/nova-3", "Cloudflare"},
		{"@cf/microsoft/phi-2", "Cloudflare"},
		{"@cf/thinkingmachines/inkling-2-small", "Cloudflare"},
		{"gpt4", "OpenAI"},
		{"gpt4.1-mini", "OpenAI"},
		{"gpt5", "OpenAI"},
		{"gpt35-turbo", "OpenAI"},
		{"chatgpt", "OpenAI"},
		{"gpt2", "OpenAI"},
		{"gpt4all", ""},
		{"nomic-ai/gpt4all-j", ""},
		{"distilgpt2", ""},
		{"llamagate/nomic-embed-text", ""},
		{"llamagate/gpt-oss-20b", "OpenAI"},
		{"mistral/gpt-oss-120b", "OpenAI"},
		{"mistral/llama-3.3-70b", "Meta"},
		{"mistral/ministral-3b-2512", "Mistral"},
		{"qwencloud/MiniMax-M2.5", "MiniMax"},
		{"qwen_ai_platform/hy3", "腾讯"},
		{"deepseek/search", "DeepSeek"},
		{"bytedance-seed/seed-coder-8b", "字节跳动"},
		{"replicate/bytedance/dreamina-3.1", "即梦"},
		{"fal_ai/fal-ai/bytedance/seedream/v4", "字节跳动"},
		{"vertex_ai/virtual-try-on-001", "Google"},
		{"ft:babbage-002", "OpenAI"},
		{"ft:davinci-002:acme::abc123", "OpenAI"},
		{"nai-diffusion-4-5-full", "NovelAI"},
		{"MAI-DS-R1", ""},
		{"lite", ""},
		{"flux-1.1-pro", ""},
		{"mj_imagine", ""},
		{"suno_music", ""},

		// The owner's screenshots.
		{"tts-1-1106", "OpenAI"},
		{"tts-1-hd", "OpenAI"},
		{"tts-1-hd-1106", "OpenAI"},
		{"gpt-4o-mini", "OpenAI"},
		{"gpt-4o-mini-2024-07-18", "OpenAI"},
		{"deepseek-v4-flash", "DeepSeek"},
		{"[按次]deepseek-v4-flash-0731", "DeepSeek"},
		{"deepseek-v4-pro", "DeepSeek"},
		{"deepseek-v4-pro-0813", "DeepSeek"},
		{"claude-opus-4-5", "Anthropic"},
		{"[官KEY]claude-opus-4-5", "Anthropic"},
		{"[正价kiro]claude-opus-4.5", "Anthropic"},
		{"[正价kiro]claude-opus-4.5-thinking-max", "Anthropic"},
		{"[正价kiro]claude-sonnet-4.5", "Anthropic"},
		{"claude-opus-4-5-20251101", "Anthropic"},
		{"claude-haiku-4-5-20251001", "Anthropic"},
		{"[按次]claude-haiku-4-5", "Anthropic"},
	})
}

// One case per ordering constraint of the rule table: the first row that
// matches decides, so these hold only while the rows keep their order.
func TestInferDefaultVendorOrder(t *testing.T) {
	resetInferredVendorCache(t)
	assertInferredVendors(t, []struct{ name, vendor string }{
		{"gpt-5.3-codex-spark", "OpenAI"},                // spark is not 讯飞 here
		{"muse-spark-1.3", "Meta"},                       // Meta Muse before 讯飞
		{"yi-spark", "零一万物"},                             // 零一万物, not 讯飞
		{"tongyi-embedding-vision-plus", "阿里巴巴"},         // tongyi before 零一万物 yi-
		{"360zhinao-turbo-qwen-plus", "360"},             // 360 before 阿里巴巴
		{"360gpt2-o1", "360"},                            // 360 before OpenAI
		{"gemini-claude-opus-4-6-thinking", "Anthropic"}, // Anthropic before Google
		{"dreamina-seedance-2-0-260128", "字节跳动"},         // Seedance before 即梦
		{"dreamina-3.1", "即梦"},
		{"replicate/bytedance/dreamina-3.1", "即梦"},           // 即梦 before the bare bytedance row
		{"replicate/bytedance/sdxl-lightning-4step", "字节跳动"}, // the bare bytedance row
		{"fal-ai/kling-image/o1", "快手"},                      // every Chinese vendor before OpenAI
		{"deepseek-r1-distill-qwen-32b", "DeepSeek"},         // DeepSeek before 阿里巴巴/Meta
		{"llama-3-erato-v1", "NovelAI"},                      // NovelAI before Meta llama
		{"@cf/qwen/qwq-32b", "阿里巴巴"},                         // a creator keyword before the Cloudflare host
		{"@cf/meta/m2m100-1.2b", "Meta"},                     // a creator org before the Cloudflare host
		{"@cf/deepgram/nova-3", "Cloudflare"},                // the host only when no creator matches
		{"gpt-sovits", ""},                                   // stop row first
		{"gpt4all", ""},
	})
}

// An org/model id is matched on the model id first; only when no row matches
// it do the creator-org rows decide, so a host prefix never does.
func TestInferDefaultVendorOrgModelIds(t *testing.T) {
	resetInferredVendorCache(t)
	assertInferredVendors(t, []struct{ name, vendor string }{
		{"llamagate/nomic-embed-text", ""},
		{"llamagate/gpt-oss-20b", "OpenAI"},
		{"mistral/gpt-oss-120b", "OpenAI"},
		{"mistral/llama-3.3-70b", "Meta"},
		{"mistral/foo-1", "Mistral"},
		{"qwencloud/MiniMax-M2.5", "MiniMax"},
		{"qwencloud/llama-4-maverick", "Meta"},
		{"qwen_ai_platform/hy3", "腾讯"},
		{"deepseek/search", "DeepSeek"},
		{"deepseek-ai/janus-pro-7b", "DeepSeek"},
		{"bytedance-seed/seed-coder-8b", "字节跳动"},
		{"openai/image-2", "OpenAI"},
		{"agentcore/search", ""},
		{"nomic-ai/gpt4all-j", ""}, // the stop row on the model id
	})
}

// Dash-less OpenAI spellings, look-alikes, and fine-tune bases.
func TestInferDefaultVendorAnchors(t *testing.T) {
	resetInferredVendorCache(t)
	assertInferredVendors(t, []struct{ name, vendor string }{
		{"gpt4", "OpenAI"},
		{"gpt4-turbo", "OpenAI"},
		{"gpt4.1-mini", "OpenAI"},
		{"gpt5", "OpenAI"},
		{"gpt3.5-turbo", "OpenAI"},
		{"gpt35-turbo", "OpenAI"},
		{"chatgpt", "OpenAI"},
		{"chatgpt4", "OpenAI"},
		{"gpt2-xl", "OpenAI"},
		{"GPT4All-13B-snoozy", ""},
		{"distilgpt2", ""},
		{"360gpt-pro", "360"},
		{"vertex_ai/virtual-try-on-001", "Google"},
		{"virtual-try-on-preview-08-04", "Google"},
		{"imagen-product-recontext-preview-06-30", "Google"},
		{"ft:babbage-002", "OpenAI"},
		{"ft:davinci-002:acme::abc123", "OpenAI"},
		{"davinci-002", "OpenAI"},
	})
}

func TestInferDefaultVendorStripsLineTagAndCase(t *testing.T) {
	resetInferredVendorCache(t)
	assertInferredVendors(t, []struct{ name, vendor string }{
		{"[按次]TTS-1-HD-1106", "OpenAI"},
		{"[x]tongyi-embedding-vision-plus", "阿里巴巴"},
	})
}

func TestInferDefaultVendorMemo(t *testing.T) {
	resetInferredVendorCache(t)
	assert.Equal(t, "OpenAI", inferDefaultVendor("[按次]TTS-1"))
	assert.Equal(t, "OpenAI", inferDefaultVendor("tts-1"))
	inferredVendorMu.Lock()
	assert.Equal(t, map[string]string{"tts-1": "OpenAI"}, inferredVendorCache, "one entry per lower-cased name without its line tag")
	inferredVendorMu.Unlock()

	for i := 0; i <= inferredVendorCacheLimit; i++ {
		inferDefaultVendor(fmt.Sprintf("probe-%d", i))
	}
	inferredVendorMu.Lock()
	size := len(inferredVendorCache)
	inferredVendorMu.Unlock()
	assert.LessOrEqual(t, size, inferredVendorCacheLimit, "the memo is cleared when full")
	assert.Equal(t, "OpenAI", inferDefaultVendor("tts-1"))
}

// A built-in vendor resolves to a saved vendor named like the same company in
// the official metadata sync; hosting platforms are never aliases.
func TestGetDisplayVendorAliases(t *testing.T) {
	saved := func(names ...string) map[int]*Vendor {
		vendors := make(map[int]*Vendor, len(names))
		for i, name := range names {
			vendors[i+1] = &Vendor{Id: i + 1, Name: name, Status: 1}
		}
		return vendors
	}
	for _, tc := range []struct {
		builtin string
		vendors map[int]*Vendor
		want    int
	}{
		{"阿里巴巴", saved("Tencent TokenHub", "Alibaba"), 2},
		{"阿里巴巴", saved("Alibaba (China)", "Alibaba"), 2}, // alias order
		{"阿里巴巴", saved("Alibaba", "阿里巴巴"), 2},            // the exact name first
		{"智谱", saved("Z.AI Coding Plan"), 1},
		{"Moonshot", saved("moonshot ai"), 1}, // case-insensitive
		{"MiniMax", saved("MiniMax (minimax.cn)"), 1},
		{"腾讯", saved("Tencent TokenHub"), -1020},
		{"字节跳动", saved("Volcengine Ark", "Volcengine Ark Coding Plan"), -1016},
		{"DeepSeek", saved("Tencent TokenHub"), -1005},
		{"OpenAI", saved("openai"), 1},
		// A display vendor is not a saved vendor.
		{"阿里巴巴", map[int]*Vendor{-1: {Id: -1, Name: "Alibaba", Status: 1}}, -1022},
	} {
		t.Run(tc.builtin, func(t *testing.T) {
			assert.Equal(t, tc.want, getDisplayVendor(tc.builtin, tc.vendors))
			if tc.want < 0 {
				// No saved vendor stands for the company: the built-in display vendor is added.
				require.Contains(t, tc.vendors, tc.want)
				assert.Equal(t, Vendor{Id: tc.want, Name: tc.builtin, Status: 1, Icon: getDefaultVendorIcon(tc.builtin)}, *tc.vendors[tc.want])
			}
		})
	}
}
