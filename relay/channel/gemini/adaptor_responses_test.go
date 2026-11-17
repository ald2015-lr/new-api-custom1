package gemini

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestConvertOpenAIResponsesRequestToGeminiInstructionsAndInput(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model:        "gemini-test",
		Instructions: mustGeminiRawMessage(t, "system rules"),
		Input:        mustGeminiRawMessage(t, "hello"),
	})

	require.NotNil(t, got.SystemInstructions)
	require.Len(t, got.SystemInstructions.Parts, 1)
	assert.Equal(t, "system rules", got.SystemInstructions.Parts[0].Text)
	require.Len(t, got.Contents, 1)
	assert.Equal(t, "user", got.Contents[0].Role)
	require.Len(t, got.Contents[0].Parts, 1)
	assert.Equal(t, "hello", got.Contents[0].Parts[0].Text)
}

func TestConvertOpenAIResponsesRequestToGeminiFunctionToolAndChoice(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustGeminiRawMessage(t, "lookup weather"),
		Tools: mustGeminiRawMessage(t, []map[string]any{
			{
				"type":        "function",
				"name":        "lookup",
				"description": "Lookup data",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"q": map[string]any{"type": "string"},
					},
				},
			},
			{"type": "custom", "name": "freeform"},
		}),
		ToolChoice: mustGeminiRawMessage(t, map[string]any{
			"type": "function",
			"name": "lookup",
		}),
	})

	tools := got.GetTools()
	require.Len(t, tools, 1)
	assert.Equal(t, "lookup", ojson.GetBytes(got.Tools, "0.functionDeclarations.0.name").String())
	assert.Equal(t, "Lookup data", ojson.GetBytes(got.Tools, "0.functionDeclarations.0.description").String())
	require.NotNil(t, got.ToolConfig)
	require.NotNil(t, got.ToolConfig.FunctionCallingConfig)
	assert.Equal(t, dto.FunctionCallingConfigMode("ANY"), got.ToolConfig.FunctionCallingConfig.Mode)
	assert.Equal(t, []string{"lookup"}, got.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
}

func TestConvertOpenAIResponsesRequestToGeminiFunctionCallConversation(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustGeminiRawMessage(t, []map[string]any{
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "I will call."},
				},
			},
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"q": "x"},
			},
			{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  map[string]any{"ok": true},
			},
		}),
		Tools: mustGeminiRawMessage(t, []map[string]any{
			{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
		}),
	})

	require.Len(t, got.Contents, 2)
	assert.Equal(t, "model", got.Contents[0].Role)
	require.Len(t, got.Contents[0].Parts, 2)
	require.NotNil(t, got.Contents[0].Parts[0].FunctionCall)
	assert.Equal(t, "lookup", got.Contents[0].Parts[0].FunctionCall.FunctionName)
	assert.Equal(t, map[string]any{"q": "x"}, got.Contents[0].Parts[0].FunctionCall.Arguments)
	assert.Equal(t, "I will call.", got.Contents[0].Parts[1].Text)

	assert.Equal(t, "user", got.Contents[1].Role)
	require.Len(t, got.Contents[1].Parts, 1)
	require.NotNil(t, got.Contents[1].Parts[0].FunctionResponse)
	assert.Equal(t, "lookup", got.Contents[1].Parts[0].FunctionResponse.Name)
	assert.Equal(t, map[string]any{"ok": true}, got.Contents[1].Parts[0].FunctionResponse.Response)
}

func TestConvertOpenAIResponsesRequestToGeminiKeepsCustomToolCalls(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustGeminiRawMessage(t, []map[string]any{
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "before custom"},
				},
			},
			{
				"type":    "custom_tool_call",
				"call_id": "call_custom",
				"name":    "apply_patch",
				"input":   "patch body",
			},
			{
				"type":    "custom_tool_call_output",
				"call_id": "call_custom",
				"output":  "ok",
			},
			{
				"type":    "function_call_output",
				"call_id": "call_custom",
				"output":  "legacy custom output",
			},
			{
				"role":    "user",
				"content": "next turn",
			},
		}),
		Tools: mustGeminiRawMessage(t, []map[string]any{
			{"type": "custom", "name": "apply_patch"},
			{"type": "unknown", "name": "unknown"},
		}),
	})

	// The cleaned schema has no additionalProperties, and the unknown tool is dropped.
	assert.JSONEq(t, `[{"functionDeclarations":[{
		"name":"apply_patch",
		"description":"This tool takes freeform text. Put the complete raw text in the \"input\" argument.",
		"parameters":{"type":"OBJECT","properties":{"input":{"type":"STRING","description":"Raw input for the tool."}},"required":["input"]}
	}]}]`, string(got.Tools))

	require.Len(t, got.Contents, 3)
	assert.Equal(t, "model", got.Contents[0].Role)
	require.Len(t, got.Contents[0].Parts, 2)
	require.NotNil(t, got.Contents[0].Parts[0].FunctionCall)
	assert.Equal(t, "apply_patch", got.Contents[0].Parts[0].FunctionCall.FunctionName)
	assert.Equal(t, map[string]any{"input": "patch body"}, got.Contents[0].Parts[0].FunctionCall.Arguments)
	assert.Equal(t, "before custom", got.Contents[0].Parts[1].Text)

	assert.Equal(t, "user", got.Contents[1].Role)
	require.Len(t, got.Contents[1].Parts, 2)
	for i, output := range []string{"ok", "legacy custom output"} {
		response := got.Contents[1].Parts[i].FunctionResponse
		require.NotNil(t, response)
		assert.Equal(t, "apply_patch", response.Name)
		assert.Equal(t, map[string]any{"content": output}, response.Response)
		assert.JSONEq(t, `"call_custom"`, string(response.ID))
	}

	assert.Equal(t, "user", got.Contents[2].Role)
	require.Len(t, got.Contents[2].Parts, 1)
	assert.Equal(t, "next turn", got.Contents[2].Parts[0].Text)
}

func mustConvertResponsesToGemini(t *testing.T, req dto.OpenAIResponsesRequest) *dto.GeminiChatRequest {
	t.Helper()
	info := &relaycommon.RelayInfo{
		OriginModelName: req.Model,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: req.Model,
		},
	}
	got, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, req)
	require.NoError(t, err)
	geminiReq, ok := got.(*dto.GeminiChatRequest)
	require.True(t, ok)
	return geminiReq
}

func mustGeminiRawMessage(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := common.Marshal(value)
	require.NoError(t, err)
	return raw
}

// Gemini embeds plain text only. A content part fails the request with a 400
// that skips retries, because the request estimate billed for this channel
// counts parts Gemini would never receive.
func TestConvertEmbeddingRequestRejectsContentParts(t *testing.T) {
	imagePart := map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/cat.png"}}
	textPart := map[string]any{"type": "text", "text": "a cat"}
	for _, input := range []any{[]any{"a cat", imagePart}, []any{textPart}} {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-embedding-001"}}
		_, err := (&Adaptor{}).ConvertEmbeddingRequest(nil, info, dto.EmbeddingRequest{Model: "gemini-embedding-001", Input: input})
		var apiErr *types.NewAPIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
		assert.True(t, types.IsSkipRetryError(apiErr))
	}

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-embedding-001"}}
	got, err := (&Adaptor{}).ConvertEmbeddingRequest(nil, info, dto.EmbeddingRequest{Model: "gemini-embedding-001", Input: []any{"a cat", "a dog"}})
	require.NoError(t, err)
	wire, err := common.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `["a cat","a dog"]`, ojson.GetBytes(wire, "requests.#.content.parts.0.text").Raw)
}

// freeKeyGoogle plays Google for a channel in free key mode: it records the
// countTokens and generate calls and answers them as the test says.
type freeKeyGoogle struct {
	mu        sync.Mutex
	counts    []freeKeyGoogleCall
	generates []freeKeyGoogleCall
	// countAnswer and generateAnswer answer a call on key; nil answers 200
	// with 100 tokens and a generation.
	countAnswer    func(key string) (int, string)
	generateAnswer func(key string) (int, string)
}

type freeKeyGoogleCall struct {
	key, path string
	body      string
}

const freeKeyGeneration = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`

func (g *freeKeyGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	call := freeKeyGoogleCall{key: r.Header.Get("x-goog-api-key"), path: r.URL.Path, body: string(body)}
	g.mu.Lock()
	status, answer := http.StatusOK, freeKeyGeneration
	if strings.HasSuffix(r.URL.Path, ":countTokens") {
		g.counts = append(g.counts, call)
		status, answer = http.StatusOK, `{"totalTokens":100,"promptTokensDetails":[{"modality":"TEXT","tokenCount":100}]}`
		if g.countAnswer != nil {
			status, answer = g.countAnswer(call.key)
		}
	} else {
		g.generates = append(g.generates, call)
		if g.generateAnswer != nil {
			status, answer = g.generateAnswer(call.key)
		}
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, answer)
}

func (g *freeKeyGoogle) calls() (counts, generates []freeKeyGoogleCall) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.counts), slices.Clone(g.generates)
}

// freeKeyFixture is a Gemini channel in free key mode with keys, in a
// database of its own, against fake Google, with a limiter of its own.
type freeKeyFixture struct {
	google    *freeKeyGoogle
	server    *httptest.Server
	channelID int
	keys      []string
	limiter   *geminifreekey.Limiter
}

func newFreeKeyFixture(t *testing.T, keys []string, keyStatus map[int]int) *freeKeyFixture {
	t.Helper()
	require.NoError(t, i18n.Init())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.User{}, &model.Ability{}))
	previousDB, previousCache := model.DB, common.MemoryCacheEnabled
	model.DB, common.MemoryCacheEnabled = db, false
	limiter := geminifreekey.New(nil)
	previousLimiter := geminifreekey.SetDefault(limiter)
	google := &freeKeyGoogle{}
	server := httptest.NewServer(google)
	t.Cleanup(func() {
		server.Close()
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		geminifreekey.SetDefault(previousLimiter)
		model.DB, common.MemoryCacheEnabled = previousDB, previousCache
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	channel := model.Channel{Type: constant.ChannelTypeGemini, Key: strings.Join(keys, "\n"), Status: common.ChannelStatusEnabled, Name: "gemini-free", Models: "gemini-2.5-flash", Group: "default",
		ChannelInfo: model.ChannelInfo{IsMultiKey: len(keys) > 1, MultiKeySize: len(keys), MultiKeyMode: constant.MultiKeyModePolling, MultiKeyStatusList: keyStatus}}
	require.NoError(t, db.Create(&channel).Error)
	return &freeKeyFixture{google: google, server: server, channelID: channel.Id, keys: keys, limiter: limiter}
}

// request is the relay state of a request the distributor sent to the
// channel's first key, and its context.
func (f *freeKeyFixture) request(modelName string, settings dto.ChannelOtherSettings) (*gin.Context, *relaycommon.RelayInfo) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelKey, f.keys[0])
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, 0)
	common.SetContextKey(c, constant.ContextKeyChannelAutoBan, true)
	common.SetContextKey(c, constant.ContextKeyChannelName, "gemini-free")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeGemini, ChannelId: f.channelID, ChannelIsMultiKey: len(f.keys) > 1,
		ChannelBaseUrl: f.server.URL, ApiKey: f.keys[0], UpstreamModelName: modelName, ChannelOtherSettings: settings,
	}}
	info.SetEstimatePromptTokens(42)
	return c, info
}

const freeKeyChatBody = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{}}`

func doFreeKeyRequest(t *testing.T, c *gin.Context, info *relaycommon.RelayInfo, body string) (*http.Response, error) {
	t.Helper()
	requestBody, closer, err := relaycommon.NewOutboundJSONBody([]byte(body))
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })
	resp, err := (&Adaptor{}).DoRequest(c, info, requestBody)
	if err != nil {
		return nil, err
	}
	return resp.(*http.Response), nil
}

func readFreeKeyResponse(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(data)
}

func freeKeyAuditOf(t *testing.T, c *gin.Context) *geminifreekey.Audit {
	t.Helper()
	audit, ok := common.GetContextKeyType[*geminifreekey.Audit](c, constant.ContextKeyGeminiFreeKeyAudit)
	require.True(t, ok)
	return audit
}

// freeKeyServerLog collects the server's log lines while a test runs.
type freeKeyServerLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *freeKeyServerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

func (l *freeKeyServerLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// captureFreeKeyServerLog collects the warnings and errors the server logs
// until the test ends.
func captureFreeKeyServerLog(t *testing.T) *freeKeyServerLog {
	t.Helper()
	log := &freeKeyServerLog{}
	common.LogWriterMu.Lock()
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = log
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previous
		common.LogWriterMu.Unlock()
	})
	return log
}

func requireFreeKeyBusy(t *testing.T, err error) *types.NewAPIError {
	t.Helper()
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, types.ErrorCodeGeminiFreeKeyBusy, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.False(t, types.IsSkipRetryError(apiErr), "another channel may take it")
	return apiErr
}

const freeKeyTPM429 = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 250000, model: gemini-2.5-flash, description: Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 49.477199395s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_input_token_count","quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaDimensions":{"location":"global","model":"gemini-2.5-flash"},"quotaValue":"250000"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"49s"}]}}`

// freeKeyDay429 is Google's per-day 429 for gemini-2.5-flash without a retry
// delay.
const freeKeyDay429 = `{"error":{"code":429,"message":"x","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"20"}]}]}}`

// freeKeyPlain429 is Google's rate limit without quota details.
const freeKeyPlain429 = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`

// freeKeyProjectUnavailable is Google's answer for a key whose project has no
// free tier.
const freeKeyProjectUnavailable = `{"error":{"code":403,"message":"The Gemini API free tier is not available for this project. To use the Gemini API, please set up billing to upgrade to the paid tier at https://aistudio.google.com/billing. For more information, see https://ai.google.dev/gemini-api/docs/troubleshooting#project-unavailable.","status":"PERMISSION_DENIED"}}`

// On a Gemini channel in free key mode a request counts its input tokens
// once with countTokens, goes to a key with room under the model's free tier
// limits and moves to another key when Google answers 429 or rejects the
// key, sending the same bytes; one no key can take is a 429 another channel
// may take, sent nowhere.
func TestFreeKeyModeSendsEachRequestToAKeyWithRoom(t *testing.T) {
	on := dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true}
	keys := []string{"AIzaSyFreeKey0-abcdefghijklmnopqrstuvw", "AIzaSyFreeKey1-abcdefghijklmnopqrstuvw", "AIzaSyFreeKey2-abcdefghijklmnopqrstuvw"}

	t.Run("countTokens counts the whole generate request", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, freeKeyGeneration, readFreeKeyResponse(t, resp))
		counts, generates := f.google.calls()
		require.Len(t, counts, 1)
		assert.Equal(t, "/v1beta/models/gemini-2.5-flash:countTokens", counts[0].path)
		assert.Equal(t, keys[0], counts[0].key)
		assert.JSONEq(t, `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{},"model":"models/gemini-2.5-flash"}}`, counts[0].body)
		require.Len(t, generates, 1)
		assert.Equal(t, "/v1beta/models/gemini-2.5-flash:generateContent", generates[0].path)
		assert.Equal(t, freeKeyChatBody, generates[0].body)
		assert.Equal(t, &geminifreekey.Audit{ChannelID: f.channelID, Bucket: "gemini-2.5-flash", Tokens: 100, TokenSource: "count_tokens", KeyIndex: 0,
			Attempts: []geminifreekey.AuditAttempt{{KeyIndex: 0, Status: 200, Reason: "accepted"}}}, freeKeyAuditOf(t, c))
		usage := f.limiter.Usage([]geminifreekey.Key{{Index: 0, Secret: keys[0]}}, dto.DefaultGeminiFreeKeyModelLimits(), []string{"gemini-2.5-flash"})
		assert.Equal(t, []geminifreekey.ModelUsage{{Model: "gemini-2.5-flash", Limits: geminifreekey.Limits{RPM: 5, TPM: 250000, RPD: 20}, RPMUsed: 1, TPMUsed: 100, RPDUsed: 1}}, usage[0].Models)
	})

	t.Run("countTokens counts each content of a batch embedding, which counts per item", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		c, info := f.request("gemini-embedding-001", on)
		info.IsGeminiBatchEmbedding = true
		const batch = `{"requests":[{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"a"}]}},{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"b"}]}}]}`
		f.google.generateAnswer = func(string) (int, string) { return http.StatusOK, `{"embeddings":[{"values":[1]},{"values":[2]}]}` }
		_, err := doFreeKeyRequest(t, c, info, batch)
		require.NoError(t, err)
		counts, generates := f.google.calls()
		require.Len(t, counts, 1)
		assert.Equal(t, "/v1beta/models/gemini-embedding-001:countTokens", counts[0].path)
		assert.JSONEq(t, `{"contents":[{"parts":[{"text":"a"}]},{"parts":[{"text":"b"}]}]}`, counts[0].body)
		require.Len(t, generates, 1)
		assert.Equal(t, "/v1beta/models/gemini-embedding-001:batchEmbedContents", generates[0].path)
		usage := f.limiter.Usage([]geminifreekey.Key{{Index: 0, Secret: keys[0]}}, dto.DefaultGeminiFreeKeyModelLimits(), nil)
		assert.Equal(t, 2, usage[0].Models[0].RPDUsed)
	})

	t.Run("a 429 moves to the next key with the same bytes", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[0] {
				return http.StatusTooManyRequests, freeKeyTPM429
			}
			return http.StatusOK, freeKeyGeneration
		}
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, freeKeyGeneration, readFreeKeyResponse(t, resp))
		_, generates := f.google.calls()
		require.Len(t, generates, 2)
		assert.Equal(t, []string{keys[0], keys[1]}, []string{generates[0].key, generates[1].key})
		assert.Equal(t, generates[0].body, generates[1].body)
		assert.Equal(t, keys[1], info.ApiKey)
		assert.Equal(t, 1, info.ChannelMultiKeyIndex)
		assert.Equal(t, keys[1], common.GetContextKeyString(c, constant.ContextKeyChannelKey), "a failure now disables the key that answered")
		assert.Equal(t, 1, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex))
		assert.Equal(t, []geminifreekey.AuditAttempt{{KeyIndex: 0, Status: 429, Reason: "minute"}, {KeyIndex: 1, Status: 200, Reason: "accepted"}}, freeKeyAuditOf(t, c).Attempts)
		bucket := geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")
		pool := geminifreekey.Pool{Keys: []geminifreekey.Key{{Index: 0, Secret: keys[0]}}, Bucket: bucket}
		assert.Equal(t, geminifreekey.Report{Keys: 1, PausedMinute: 1}, f.limiter.Inspect(pool, 0, 1, nil))
	})

	t.Run("Google's 429 on every key the request may try ends it as Google's error", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(string) (int, string) { return http.StatusTooManyRequests, freeKeyTPM429 }
		c, info := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyMaxAttempts: 2})
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		apiErr := requireFreeKeyBusy(t, err)
		_, generates := f.google.calls()
		assert.Len(t, generates, 2)
		assert.Equal(t, types.ErrorTypeOpenAIError, apiErr.GetErrorType())
		var googleError struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, common.UnmarshalJsonStr(freeKeyTPM429, &googleError))
		assert.Equal(t, types.OpenAIError{Message: googleError.Error.Message, Type: "upstream_error", Code: float64(429)}, apiErr.ToOpenAIError(), "the client gets Google's error as the relay passes it on")
		assert.Equal(t, "rate_limited", freeKeyAuditOf(t, c).Rejected)
	})

	t.Run("a request over the model's tokens per minute reaches no key", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.countAnswer = func(string) (int, string) { return http.StatusOK, `{"totalTokens":250001}` }
		c, info := f.request("gemini-2.5-flash", on)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		apiErr := requireFreeKeyBusy(t, err)
		assert.Contains(t, apiErr.Error(), "250001")
		_, generates := f.google.calls()
		assert.Empty(t, generates)
		audit := freeKeyAuditOf(t, c)
		assert.Equal(t, "too_large", audit.Rejected)
		assert.Equal(t, 250001, audit.Tokens)
	})

	t.Run("a model the free tier does not serve reaches no key", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		c, info := f.request("gemini-2.5-pro", on)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		requireFreeKeyBusy(t, err)
		counts, generates := f.google.calls()
		assert.Empty(t, counts)
		assert.Empty(t, generates)
		assert.Equal(t, "not_free", freeKeyAuditOf(t, c).Rejected)
		assert.True(t, f.limiter.Active(), "selection from the database skips the channel from now on")
	})

	t.Run("no key left for the model today is answered before counting", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:2], nil)
		bucket := geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")
		pool := geminifreekey.Pool{Bucket: bucket}
		for _, key := range keys[:2] {
			f.limiter.Observe(pool, geminifreekey.Key{Secret: key}, http.StatusTooManyRequests, []byte(`{"error":{"code":429,"message":"x","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"20"}]}]}}`))
		}
		c, info := f.request("gemini-2.5-flash", on)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		apiErr := requireFreeKeyBusy(t, err)
		assert.Contains(t, apiErr.Error(), "2 keys, 2 paused for the rest of the day")
		counts, generates := f.google.calls()
		assert.Empty(t, counts)
		assert.Empty(t, generates)
		assert.Equal(t, "no_key", freeKeyAuditOf(t, c).Rejected)
	})

	t.Run("countTokens failing falls back to the local estimate", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.countAnswer = func(string) (int, string) {
			return http.StatusNotFound, `{"error":{"code":404,"message":"models/gemini-2.5-flash is not found for API version v1beta, or is not supported for countTokens.","status":"NOT_FOUND"}}`
		}
		serverLog := captureFreeKeyServerLog(t)
		c, info := f.request("gemini-2.5-flash", on)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		audit := freeKeyAuditOf(t, c)
		assert.Equal(t, 42, audit.Tokens)
		assert.Equal(t, "local", audit.TokenSource)
		assert.Contains(t, serverLog.String(), fmt.Sprintf("Gemini free key channel #%d: countTokens answered 404, using the local estimate", f.channelID))
	})

	t.Run("the local counter never calls countTokens", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		c, info := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyTokenCounter: "local"})
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		counts, generates := f.google.calls()
		assert.Empty(t, counts)
		assert.Len(t, generates, 1)
		assert.Equal(t, "local", freeKeyAuditOf(t, c).TokenSource)
	})

	t.Run("a rejected key sits out, and is disabled once the request moves on", func(t *testing.T) {
		previousAutoDisable := common.AutomaticDisableChannelEnabled
		common.AutomaticDisableChannelEnabled = true
		t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
		f := newFreeKeyFixture(t, keys, nil)
		const invalid = `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}]}}`
		const suspended = `{"error":{"code":403,"message":"Permission denied: Consumer 'api_key:AIzaSyFreeKey1' has been suspended.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CONSUMER_SUSPENDED"}]}}`
		// Key 0 is rejected by countTokens, key 1 by the generation.
		f.google.countAnswer = func(key string) (int, string) {
			if key == keys[0] {
				return http.StatusBadRequest, invalid
			}
			return http.StatusOK, `{"totalTokens":7}`
		}
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[1] {
				return http.StatusForbidden, suspended
			}
			return http.StatusOK, freeKeyGeneration
		}
		serverLog := captureFreeKeyServerLog(t)
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, freeKeyGeneration, readFreeKeyResponse(t, resp))
		// The log numbers keys from 1, as the channel's key list does.
		assert.Contains(t, serverLog.String(), fmt.Sprintf("Gemini free key channel #%d: Google rejected key #1 on countTokens (status 400), trying key #2", f.channelID))
		assert.Contains(t, serverLog.String(), fmt.Sprintf("Gemini free key channel #%d key #2 was rejected by Google, disabling it and trying another key", f.channelID))
		assert.NotContains(t, serverLog.String(), "local estimate", "countTokens counted on the next key")
		counts, generates := f.google.calls()
		assert.Equal(t, []string{keys[0], keys[1]}, []string{counts[0].key, counts[1].key})
		assert.Equal(t, []string{keys[1], keys[2]}, []string{generates[0].key, generates[1].key})
		assert.Equal(t, []geminifreekey.AuditAttempt{{KeyIndex: 0, Status: 400, Reason: "count_tokens_key_error"}, {KeyIndex: 1, Status: 403, Reason: "key_error"}, {KeyIndex: 2, Status: 200, Reason: "accepted"}}, freeKeyAuditOf(t, c).Attempts)
		bucket := geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")
		pool := geminifreekey.Pool{Keys: []geminifreekey.Key{{Index: 0, Secret: keys[0]}, {Index: 1, Secret: keys[1]}}, Bucket: bucket}
		assert.Equal(t, geminifreekey.Report{Keys: 2, KeyError: 2}, f.limiter.Inspect(pool, 0, 1, nil))
		// The suspension matches the "Permission denied" keyword; an invalid
		// key's 400 matches no rule.
		assert.Eventually(t, func() bool {
			channel, err := model.GetChannelById(f.channelID, true)
			return err == nil && channel.ChannelInfo.MultiKeyStatusList[1] == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond)
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 0)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	})

	t.Run("the last key's rejection is returned as Google sent it", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:1], nil)
		const unauthenticated = `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`
		f.google.generateAnswer = func(string) (int, string) { return http.StatusUnauthorized, unauthenticated }
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, unauthenticated, readFreeKeyResponse(t, resp))
	})

	t.Run("the key countTokens rejected last is the key the request ends on", func(t *testing.T) {
		previousAutoDisable := common.AutomaticDisableChannelEnabled
		common.AutomaticDisableChannelEnabled = true
		t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
		f := newFreeKeyFixture(t, keys[:2], nil)
		const suspended = `{"error":{"code":403,"message":"Permission denied: Consumer 'api_key:AIzaSyFreeKey1' has been suspended.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CONSUMER_SUSPENDED"}]}}`
		// The distributor's key 0 is done for the day; key 1 was suspended.
		pool := geminifreekey.Pool{Bucket: geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")}
		f.limiter.Observe(pool, geminifreekey.Key{Secret: keys[0]}, http.StatusTooManyRequests, []byte(freeKeyDay429))
		f.google.countAnswer = func(key string) (int, string) {
			if key == keys[1] {
				return http.StatusForbidden, suspended
			}
			return http.StatusOK, `{"totalTokens":7}`
		}
		serverLog := captureFreeKeyServerLog(t)
		common.SetLogLanguage("zh-CN")
		t.Cleanup(func() {
			// The automatic disable below logs from its own goroutine.
			assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
			common.SetLogLanguage("")
		})
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		_, generates := f.google.calls()
		assert.Empty(t, generates)
		assert.Contains(t, serverLog.String(), fmt.Sprintf("Gemini 免费KEY渠道 #%d：KEY #2 调用 countTokens 时被 Google 拒绝（状态码 403），且没有其他可尝试的 KEY，按该拒绝返回", f.channelID))
		assert.NotContains(t, serverLog.String(), "本地估算")
		assert.Equal(t, keys[1], info.ApiKey)
		assert.Equal(t, 1, info.ChannelMultiKeyIndex)
		assert.Equal(t, keys[1], common.GetContextKeyString(c, constant.ContextKeyChannelKey))
		assert.Equal(t, 1, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex))
		assert.Equal(t, 1, freeKeyAuditOf(t, c).KeyIndex)

		// The relay acts on the answer as it does on any request's last one.
		apiErr := service.RelayErrorHandler(c.Request.Context(), resp, false)
		service.ProcessChannelError(c, *types.NewChannelError(f.channelID, constant.ChannelTypeGemini, "gemini-free", true, common.GetContextKeyString(c, constant.ContextKeyChannelKey), true), apiErr, nil, info)
		assert.Eventually(t, func() bool {
			channel, err := model.GetChannelById(f.channelID, true)
			return err == nil && channel.ChannelInfo.MultiKeyStatusList[1] == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond, "the suspended key is disabled")
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 0, "the healthy key stays enabled")
	})

	t.Run("a key whose project has no free tier is disabled whatever the global rules say", func(t *testing.T) {
		previousAutoDisable := common.AutomaticDisableChannelEnabled
		common.AutomaticDisableChannelEnabled = false
		t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[0] {
				return http.StatusForbidden, freeKeyProjectUnavailable
			}
			return http.StatusOK, freeKeyGeneration
		}
		serverLog := captureFreeKeyServerLog(t)
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, freeKeyGeneration, readFreeKeyResponse(t, resp))
		_, generates := f.google.calls()
		assert.Equal(t, []string{keys[0], keys[1]}, []string{generates[0].key, generates[1].key})
		assert.Equal(t, []geminifreekey.AuditAttempt{{KeyIndex: 0, Status: 403, Reason: "key_error"}, {KeyIndex: 1, Status: 200, Reason: "accepted"}}, freeKeyAuditOf(t, c).Attempts)
		assert.Empty(t, freeKeyAuditOf(t, c).Rejected)
		assert.Contains(t, serverLog.String(), fmt.Sprintf("Gemini free key channel #%d key #1 has no free tier on its Google project, disabling it", f.channelID))
		assert.Eventually(t, func() bool {
			channel, err := model.GetChannelById(f.channelID, true)
			return err == nil && channel.ChannelInfo.MultiKeyStatusList[0] == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond)
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 1, "the key that served stays enabled")
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	})

	t.Run("a channel without automatic disable keeps a key whose project has no free tier", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[0] {
				return http.StatusForbidden, freeKeyProjectUnavailable
			}
			return http.StatusOK, freeKeyGeneration
		}
		serverLog := captureFreeKeyServerLog(t)
		c, info := f.request("gemini-2.5-flash", on)
		common.SetContextKey(c, constant.ContextKeyChannelAutoBan, false)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, freeKeyGeneration, readFreeKeyResponse(t, resp))
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		assert.NotContains(t, serverLog.String(), "disabling it")
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 0)
		pool := geminifreekey.Pool{Keys: []geminifreekey.Key{{Index: 0, Secret: keys[0]}}, Bucket: geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")}
		assert.Equal(t, geminifreekey.Report{Keys: 1, KeyError: 1}, f.limiter.Inspect(pool, 0, 1, nil), "the key sits out all the same")
	})

	t.Run("the last key whose project has no free tier is disabled and its answer returned", func(t *testing.T) {
		previousAutoDisable := common.AutomaticDisableChannelEnabled
		common.AutomaticDisableChannelEnabled = false
		t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
		f := newFreeKeyFixture(t, keys[:2], nil)
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[1] {
				return http.StatusForbidden, freeKeyProjectUnavailable
			}
			return http.StatusOK, freeKeyGeneration
		}
		// The distributor's key 0 is done for the day.
		pool := geminifreekey.Pool{Bucket: geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")}
		f.limiter.Observe(pool, geminifreekey.Key{Secret: keys[0]}, http.StatusTooManyRequests, []byte(freeKeyDay429))
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, freeKeyProjectUnavailable, readFreeKeyResponse(t, resp))
		assert.Equal(t, 1, info.ChannelMultiKeyIndex)
		assert.Eventually(t, func() bool {
			channel, err := model.GetChannelById(f.channelID, true)
			return err == nil && channel.ChannelInfo.MultiKeyStatusList[1] == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond)
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 0, "the key done for the day stays enabled")
		// The user never learns that the key is a free one.
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeGemini)
		common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, on)
		answer := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(strings.NewReader(freeKeyProjectUnavailable))}
		shown := service.DisguiseGeminiFreeKeyError(c, service.RelayErrorHandler(c.Request.Context(), answer, false))
		require.NotNil(t, shown)
		assert.Equal(t, http.StatusTooManyRequests, shown.StatusCode)
		assert.NotContains(t, shown.Error(), "free tier")
	})

	t.Run("the last such key the relay's own rules disable is left to them", func(t *testing.T) {
		previousAutoDisable, previousCodes := common.AutomaticDisableChannelEnabled, operation_setting.AutomaticDisableStatusCodeRanges
		common.AutomaticDisableChannelEnabled = true
		operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 403, End: 403}}
		t.Cleanup(func() {
			common.AutomaticDisableChannelEnabled, operation_setting.AutomaticDisableStatusCodeRanges = previousAutoDisable, previousCodes
		})
		f := newFreeKeyFixture(t, keys[:1], nil)
		f.google.generateAnswer = func(string) (int, string) { return http.StatusForbidden, freeKeyProjectUnavailable }
		serverLog := captureFreeKeyServerLog(t)
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		assert.NotContains(t, serverLog.String(), "disabling it", "the relay disables the key the request ends on once")
	})

	t.Run("the channel test leaves keys whose project has no free tier enabled", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:2], nil)
		f.google.generateAnswer = func(string) (int, string) { return http.StatusForbidden, freeKeyProjectUnavailable }
		serverLog := captureFreeKeyServerLog(t)
		c, info := f.request("gemini-2.5-flash", on)
		info.IsChannelTest = true
		resp, err := (&Adaptor{}).DoRequest(c, info, bytes.NewBufferString(freeKeyChatBody))
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.(*http.Response).StatusCode)
		_, generates := f.google.calls()
		assert.Equal(t, []string{keys[0], keys[1]}, []string{generates[0].key, generates[1].key})
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		assert.NotContains(t, serverLog.String(), "disabling it")
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.Empty(t, channel.ChannelInfo.MultiKeyStatusList, "neither the key moved past nor the key the test ends on")
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	})

	t.Run("a request Google received stays counted when its answer never comes", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:1], nil)
		c, info := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyTokenCounter: "local"})
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			// Google has the request; the client leaves before its answer.
			cancel()
			<-r.Context().Done()
		}))
		t.Cleanup(silent.Close)
		info.ChannelBaseUrl = silent.URL
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.Error(t, err)
		usage := f.limiter.Usage([]geminifreekey.Key{{Index: 0, Secret: keys[0]}}, dto.DefaultGeminiFreeKeyModelLimits(), []string{"gemini-2.5-flash"})
		assert.Equal(t, 1, usage[0].Models[0].RPMUsed)
		assert.Equal(t, 42, usage[0].Models[0].TPMUsed)
		assert.Equal(t, 1, usage[0].Models[0].RPDUsed)
		assert.Equal(t, []geminifreekey.AuditAttempt{{KeyIndex: 0, Reason: "no_answer"}}, freeKeyAuditOf(t, c).Attempts)
	})

	t.Run("a request that never reached Google is taken back", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:1], nil)
		c, info := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyTokenCounter: "local"})
		info.ChannelBaseUrl = "http://127.0.0.1:1"
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.Error(t, err)
		usage := f.limiter.Usage([]geminifreekey.Key{{Index: 0, Secret: keys[0]}}, dto.DefaultGeminiFreeKeyModelLimits(), []string{"gemini-2.5-flash"})
		assert.Zero(t, usage[0].Models[0].RPDUsed)
		assert.Zero(t, usage[0].Models[0].RPMUsed)
		assert.Equal(t, []geminifreekey.AuditAttempt{{KeyIndex: 0, Reason: "transport_error"}}, freeKeyAuditOf(t, c).Attempts)
	})

	t.Run("the channel test leaves a key Google rejected enabled", func(t *testing.T) {
		previousAutoDisable := common.AutomaticDisableChannelEnabled
		common.AutomaticDisableChannelEnabled = true
		t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
		f := newFreeKeyFixture(t, keys, nil)
		const suspended = `{"error":{"code":403,"message":"Permission denied: Consumer 'api_key:AIzaSyFreeKey0' has been suspended.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CONSUMER_SUSPENDED"}]}}`
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[0] {
				return http.StatusForbidden, suspended
			}
			return http.StatusOK, freeKeyGeneration
		}
		c, info := f.request("gemini-2.5-flash", on)
		info.IsChannelTest = true
		resp, err := (&Adaptor{}).DoRequest(c, info, bytes.NewBufferString(freeKeyChatBody))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.(*http.Response).StatusCode)
		_, generates := f.google.calls()
		assert.Equal(t, []string{keys[0], keys[1]}, []string{generates[0].key, generates[1].key})
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		channel, err := model.GetChannelById(f.channelID, true)
		require.NoError(t, err)
		assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 0, "a channel test disables no key it moved past")
		pool := geminifreekey.Pool{Keys: []geminifreekey.Key{{Index: 0, Secret: keys[0]}}, Bucket: geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")}
		assert.Equal(t, geminifreekey.Report{Keys: 1, KeyError: 1}, f.limiter.Inspect(pool, 0, 1, nil), "the key still sits out")
	})

	t.Run("a stream answered without Google has the headers of one sent to it", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.countAnswer = func(string) (int, string) { return http.StatusOK, `{"totalTokens":250001}` }
		f.google.generateAnswer = func(string) (int, string) { return http.StatusTooManyRequests, freeKeyPlain429 }
		refused, refusedInfo := f.request("gemini-2.5-flash", on)
		refusedInfo.IsStream = true
		_, err := doFreeKeyRequest(t, refused, refusedInfo, freeKeyChatBody)
		requireFreeKeyBusy(t, err)
		sent, sentInfo := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{})
		sentInfo.IsStream = true
		resp, err := doFreeKeyRequest(t, sent, sentInfo, freeKeyChatBody)
		require.NoError(t, err)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		assert.Equal(t, "no-cache", refused.Writer.Header().Get("Cache-Control"))
		assert.Equal(t, sent.Writer.Header(), refused.Writer.Header())
	})

	t.Run("a request of exactly the model's tokens per minute is sent", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.countAnswer = func(string) (int, string) { return http.StatusOK, `{"totalTokens":250000}` }
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, freeKeyGeneration, readFreeKeyResponse(t, resp))
	})

	t.Run("Google's last 429 follows the channel's status code mapping when errors are revealed", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(string) (int, string) { return http.StatusTooManyRequests, freeKeyTPM429 }
		c, info := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyRevealTierErrors: true})
		c.Set("status_code_mapping", `{"429":"503"}`)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		var apiErr *types.NewAPIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, types.ErrorCodeGeminiFreeKeyBusy, apiErr.GetErrorCode())
		assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	})

	t.Run("the key summary for administrators is in their language", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:2], nil)
		pool := geminifreekey.Pool{Bucket: geminifreekey.ResolveBucket(dto.DefaultGeminiFreeKeyModelLimits(), "gemini-2.5-flash")}
		for _, key := range keys[:2] {
			f.limiter.Observe(pool, geminifreekey.Key{Secret: key}, http.StatusTooManyRequests, []byte(freeKeyDay429))
		}
		c, info := f.request("gemini-2.5-flash", on)
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{Language: i18n.LangZhCN})
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		apiErr := requireFreeKeyBusy(t, err)
		assert.Equal(t, "免费KEY模式：该渠道今天没有可用于 gemini-2.5-flash 的 KEY（共 2 个 KEY，2 个今天剩余时间暂停），请求未发送到 Google", apiErr.Error())
	})

	t.Run("any other answer is returned as sent", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		const overloaded = `{"error":{"code":503,"message":"This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.","status":"UNAVAILABLE"}}`
		f.google.generateAnswer = func(string) (int, string) { return http.StatusServiceUnavailable, overloaded }
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, "application/json; charset=UTF-8", resp.Header.Get("Content-Type"))
		assert.Equal(t, overloaded, readFreeKeyResponse(t, resp))
		_, generates := f.google.calls()
		assert.Len(t, generates, 1)
	})

	t.Run("a free tier error is a rate limit unless the channel reveals them", func(t *testing.T) {
		const country = `{"error":{"code":400,"message":"Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio.","status":"FAILED_PRECONDITION"}}`
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(string) (int, string) { return http.StatusBadRequest, country }
		c, info := f.request("gemini-2.5-flash", on)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		apiErr := requireFreeKeyBusy(t, err)
		assert.Contains(t, apiErr.Error(), "free tier is not available in your country", "for administrators")
		assert.Equal(t, "tier_error", freeKeyAuditOf(t, c).Rejected)

		c, info = f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyRevealTierErrors: true})
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, country, readFreeKeyResponse(t, resp))
	})

	t.Run("the channel test's buffer is sent again as it was", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(key string) (int, string) {
			if key == keys[0] {
				return http.StatusTooManyRequests, freeKeyTPM429
			}
			return http.StatusOK, freeKeyGeneration
		}
		c, info := f.request("gemini-2.5-flash", on)
		resp, err := (&Adaptor{}).DoRequest(c, info, bytes.NewBufferString(freeKeyChatBody))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.(*http.Response).StatusCode)
		_, generates := f.google.calls()
		require.Len(t, generates, 2)
		assert.Equal(t, freeKeyChatBody, generates[0].body)
		assert.Equal(t, freeKeyChatBody, generates[1].body)
	})

	t.Run("a single-key channel keeps its key", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys[:1], nil)
		c, info := f.request("gemini-2.5-flash", on)
		common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, 3)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, 3, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex), "the context is left as the distributor set it")
		assert.Equal(t, 0, freeKeyAuditOf(t, c).KeyIndex)
	})

	t.Run("a disabled key is never used", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, map[int]int{0: common.ChannelStatusManuallyDisabled})
		c, info := f.request("gemini-2.5-flash", on)
		_, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		counts, generates := f.google.calls()
		assert.Equal(t, keys[1], counts[0].key)
		assert.Equal(t, keys[1], generates[0].key)
	})

	t.Run("with the mode off the request goes out as before", func(t *testing.T) {
		f := newFreeKeyFixture(t, keys, nil)
		f.google.generateAnswer = func(string) (int, string) { return http.StatusTooManyRequests, freeKeyTPM429 }
		c, info := f.request("gemini-2.5-flash", dto.ChannelOtherSettings{GeminiFreeKeyRevealTierErrors: true, GeminiFreeKeyMaxAttempts: 3})
		info.ApiKey = keys[2]
		resp, err := doFreeKeyRequest(t, c, info, freeKeyChatBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		assert.Equal(t, freeKeyTPM429, readFreeKeyResponse(t, resp))
		counts, generates := f.google.calls()
		assert.Empty(t, counts)
		require.Len(t, generates, 1)
		assert.Equal(t, keys[2], generates[0].key)
		_, audited := common.GetContextKeyType[*geminifreekey.Audit](c, constant.ContextKeyGeminiFreeKeyAudit)
		assert.False(t, audited)
		assert.False(t, f.limiter.Active())
	})
}
