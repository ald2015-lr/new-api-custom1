package gemini

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamResponseGeminiChat2OpenAIAttachesUsageMetadata(t *testing.T) {
	t.Parallel()

	withUsage, isStop := streamResponseGeminiChat2OpenAI(&dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     3868,
			CandidatesTokenCount: 0,
			TotalTokenCount:      3868,
		},
	})
	require.False(t, isStop)
	require.NotNil(t, withUsage)
	require.NotNil(t, withUsage.Usage)
	require.Equal(t, 3868, withUsage.Usage.PromptTokens)
	require.Equal(t, 3868, withUsage.Usage.TotalTokens)
	require.NotNil(t, withUsage.Usage.BillingUsage)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, withUsage.Usage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, withUsage.Usage.BillingUsage.Semantic)
	require.NotNil(t, withUsage.Usage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, 3868, withUsage.Usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.False(t, withUsage.Usage.BillingUsage.Estimated)

	withoutUsage, _ := streamResponseGeminiChat2OpenAI(&dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
	})
	require.NotNil(t, withoutUsage)
	require.Nil(t, withoutUsage.Usage)
}

func TestGeminiChatStreamHandlerClaudeFirstFrameUsesUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatClaude,
		OriginModelName: "gemini-2.5-flash",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-2.5-flash",
		},
		ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{
			LastMessagesType: relaycommon.LastMessageTypeNone,
		},
	}
	info.SetEstimatePromptTokens(4994)

	chunkData, err := common.Marshal(dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: 3868,
			TotalTokenCount:  3868,
		},
	})
	require.NoError(t, err)
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n"))),
	}

	usage, newAPIError := GeminiChatStreamHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 3868, usage.PromptTokens)

	var startUsage, deltaUsage *dto.ClaudeUsage
	for line := range strings.SplitSeq(recorder.Body.String(), "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		var event dto.ClaudeResponse
		if err := common.UnmarshalJsonStr(payload, &event); err != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				startUsage = event.Message.Usage
			}
		case "message_delta":
			deltaUsage = event.Usage
		}
	}

	require.NotNil(t, startUsage)
	require.Equal(t, 3868, startUsage.InputTokens)
	require.NotNil(t, startUsage.BillingUsage)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, startUsage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, startUsage.BillingUsage.Semantic)
	require.NotNil(t, startUsage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, 3868, startUsage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.False(t, startUsage.BillingUsage.Estimated)

	require.NotNil(t, deltaUsage)
	require.Equal(t, 3868, deltaUsage.InputTokens)
	require.NotNil(t, deltaUsage.BillingUsage)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, deltaUsage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, deltaUsage.BillingUsage.Semantic)
	require.NotNil(t, deltaUsage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, 3868, deltaUsage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
}

func TestGeminiChatHandlerCompletionTokensExcludeToolUsePromptTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        151,
			ToolUsePromptTokenCount: 18329,
			CandidatesTokenCount:    1089,
			ThoughtsTokenCount:      1120,
			TotalTokenCount:         20689,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 18480, usage.PromptTokens)
	require.Equal(t, 2209, usage.CompletionTokens)
	require.Equal(t, 20689, usage.TotalTokens)
	require.Equal(t, 1120, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestGeminiStreamHandlerCompletionTokensExcludeToolUsePromptTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	chunk := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "partial"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        151,
			ToolUsePromptTokenCount: 18329,
			CandidatesTokenCount:    1089,
			ThoughtsTokenCount:      1120,
			TotalTokenCount:         20689,
		},
	}

	chunkData, err := common.Marshal(chunk)
	require.NoError(t, err)

	streamBody := []byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n")
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(streamBody)),
	}

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 18480, usage.PromptTokens)
	require.Equal(t, 2209, usage.CompletionTokens)
	require.Equal(t, 20689, usage.TotalTokens)
	require.Equal(t, 1120, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestGeminiTextGenerationHandlerPromptTokensIncludeToolUsePromptTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-flash-preview:generateContent", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        151,
			ToolUsePromptTokenCount: 18329,
			CandidatesTokenCount:    1089,
			ThoughtsTokenCount:      1120,
			TotalTokenCount:         20689,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiTextGenerationHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 18480, usage.PromptTokens)
	require.Equal(t, 2209, usage.CompletionTokens)
	require.Equal(t, 20689, usage.TotalTokens)
	require.Equal(t, 1120, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestGeminiChatHandlerUsesEstimatedPromptTokensWhenUsagePromptMissing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        0,
			ToolUsePromptTokenCount: 0,
			CandidatesTokenCount:    90,
			ThoughtsTokenCount:      10,
			TotalTokenCount:         110,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 110, usage.TotalTokens)
}

func TestGeminiStreamHandlerUsesEstimatedPromptTokensWhenUsagePromptMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	chunk := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "partial"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        0,
			ToolUsePromptTokenCount: 0,
			CandidatesTokenCount:    90,
			ThoughtsTokenCount:      10,
			TotalTokenCount:         110,
		},
	}

	chunkData, err := common.Marshal(chunk)
	require.NoError(t, err)

	streamBody := []byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n")
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(streamBody)),
	}

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 110, usage.TotalTokens)
}

func TestGeminiTextGenerationHandlerUsesEstimatedPromptTokensWhenUsagePromptMissing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-flash-preview:generateContent", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        0,
			ToolUsePromptTokenCount: 0,
			CandidatesTokenCount:    90,
			ThoughtsTokenCount:      10,
			TotalTokenCount:         110,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiTextGenerationHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 110, usage.TotalTokens)
}

func TestGeminiChatHandlerMissingUsageMetadataBuildsEstimatedBillingUsage(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.NotNil(t, usage.BillingUsage)
	require.True(t, usage.BillingUsage.Estimated)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, usage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, usage.BillingUsage.Semantic)
	require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, usage.PromptTokens, usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestGeminiStreamHandlerPromptOnlyUsageMetadataEstimatesCompletionTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	for _, tc := range []struct {
		name      string
		part      dto.GeminiPart
		usageText string
	}{
		{"text", dto.GeminiPart{Text: "partial streamed answer before disconnect"}, "partial streamed answer before disconnect"},
		// An agent turn that only calls a tool still produced output tokens.
		{"function call", dto.GeminiPart{FunctionCall: &dto.FunctionCall{FunctionName: "read_file", Arguments: map[string]any{"path": "main.go"}}}, `read_file{"path":"main.go"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3-flash-preview",
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "gemini-3-flash-preview",
				},
			}
			info.SetEstimatePromptTokens(20)

			// Simulates a client aborting the stream before the final chunk: output was
			// streamed but the last observed usageMetadata only carries prompt tokens.
			chunk := dto.GeminiChatResponse{
				Candidates: []dto.GeminiChatCandidate{
					{
						Content: dto.GeminiChatContent{
							Role:  "model",
							Parts: []dto.GeminiPart{tc.part},
						},
					},
				},
				UsageMetadata: dto.GeminiUsageMetadata{
					PromptTokenCount: 151,
					TotalTokenCount:  151,
				},
			}

			chunkData, err := common.Marshal(chunk)
			require.NoError(t, err)

			streamBody := []byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n")
			resp := &http.Response{
				Body: io.NopCloser(bytes.NewReader(streamBody)),
			}

			usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
				return true
			})
			require.Nil(t, newAPIError)
			require.NotNil(t, usage)
			require.Equal(t, 151, usage.PromptTokens)
			require.Equal(t, tokenkit.Estimate("gemini-3-flash-preview", tc.usageText), usage.CompletionTokens)
			require.Greater(t, usage.CompletionTokens, 0)
			require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
			require.NotNil(t, usage.BillingUsage)
			require.True(t, usage.BillingUsage.Estimated)
			require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
			require.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
		})
	}
}

func TestGeminiChatHandlerPromptOnlyUsageMetadataEstimatesCompletionTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "answer text without candidate token count"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: 151,
			TotalTokenCount:  151,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 151, usage.PromptTokens)
	require.Greater(t, usage.CompletionTokens, 0)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, usage.BillingUsage)
	require.True(t, usage.BillingUsage.Estimated)
}

func TestGeminiStreamHandlerEmptyUsageMetadataBuildsEstimatedBillingUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	streamBody := []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{}}\n" + "data: [DONE]\n")
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(streamBody)),
	}

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.NotNil(t, usage.BillingUsage)
	require.True(t, usage.BillingUsage.Estimated)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, usage.BillingUsage.Source)
	require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, usage.PromptTokens, usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

// geminiInlineFrame is one stream frame carrying a single inlineData part.
func geminiInlineFrame(mimeType, data string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"` + mimeType + `","data":"` + data + `"}}]},"index":0}],"modelVersion":"gemini-3.8-flash-lite-tts"}`
}

// pcm100ms is 4800 bytes of base64 PCM: 100 ms of 16-bit mono audio at 24 kHz.
var pcm100ms = strings.Repeat("A", 6400)

// Without billable usageMetadata, inline output is estimated from the payload:
// 1400 tokens per image, and 25 tokens per second of PCM audio (the Gemini TTS
// rate) with the length taken from the PCM byte count. Audio whose length
// cannot be measured, and non-image media, add nothing. A relay that answered
// a text request with audio/l16 chunks must not bill each chunk as an image.
func TestGeminiStreamHandlerWithoutUsageMetadataEstimatesInlineOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	l16Chunk := geminiInlineFrame("audio/l16; rate=24000; channels=1", pcm100ms)
	for _, tc := range []struct {
		name                 string
		frames               []string
		wantCompletionTokens int
		wantAudioTokens      int
		wantErrStatus        int // the fork reports an in-stream error envelope (DEPLOY §七)
	}{
		{
			// 0.3 s of audio: ceil(0.3 * 25) = 8.
			name: "pcm audio chunks then in-stream error",
			frames: []string{
				l16Chunk,
				l16Chunk,
				l16Chunk,
				`{"error":{"code":504,"message":"context deadline exceeded","status":"DEADLINE_EXCEEDED"}}`,
			},
			wantCompletionTokens: 8,
			wantAudioTokens:      8,
			wantErrStatus:        http.StatusGatewayTimeout,
		},
		{
			// 1 s of audio in the Gemini API's own mimeType form.
			name:                 "pcm audio with codec parameter",
			frames:               []string{geminiInlineFrame("audio/L16;codec=pcm;rate=24000", strings.Repeat(pcm100ms, 10))},
			wantCompletionTokens: 25,
			wantAudioTokens:      25,
		},
		{
			name:                 "audio without a measurable length",
			frames:               []string{geminiInlineFrame("audio/mpeg", pcm100ms), geminiInlineFrame("audio/l16; rate=1", pcm100ms)},
			wantCompletionTokens: 0,
		},
		{
			// One byte is not a whole 16-bit sample.
			name:                 "pcm audio with a partial sample",
			frames:               []string{geminiInlineFrame("audio/l16; rate=24000; channels=1", "AA==")},
			wantCompletionTokens: 0,
		},
		{
			// Invalid base64 is not audio, so its length bills nothing.
			name:                 "pcm audio with invalid base64",
			frames:               []string{geminiInlineFrame("audio/l16; rate=24000; channels=1", strings.Repeat("A!", 3200))},
			wantCompletionTokens: 0,
		},
		{
			name:                 "image chunks",
			frames:               []string{geminiInlineFrame("image/png", "iVBORw0KGgo="), geminiInlineFrame("image/png", "iVBORw0KGgo=")},
			wantCompletionTokens: 2 * 1400,
		},
		{
			// MIME types are case-insensitive (RFC 2045).
			name:                 "uppercase image mimeType",
			frames:               []string{geminiInlineFrame("IMAGE/PNG", "iVBORw0KGgo=")},
			wantCompletionTokens: 1400,
		},
		{
			// Payload-less image parts bill nothing.
			name:                 "image chunk without payload",
			frames:               []string{geminiInlineFrame("image/png", "")},
			wantCompletionTokens: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3.8-flash",
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "gemini-3.8-flash",
				},
			}
			info.SetEstimatePromptTokens(20)

			var streamBody strings.Builder
			for _, frame := range tc.frames {
				streamBody.WriteString("data: " + frame + "\n")
			}
			resp := &http.Response{
				Body: io.NopCloser(strings.NewReader(streamBody.String())),
			}

			usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
				return true
			})
			if tc.wantErrStatus != 0 {
				// The envelope is not swallowed; the usage of what came before it is still measured.
				require.NotNil(t, newAPIError)
				assert.Equal(t, tc.wantErrStatus, newAPIError.StatusCode)
			} else {
				require.Nil(t, newAPIError)
			}
			require.NotNil(t, usage)
			assert.Equal(t, tc.wantCompletionTokens, usage.CompletionTokens)
			assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
			require.NotNil(t, usage.BillingUsage)
			require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
			assert.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
			// Tiered expressions price ao from the settled audio output detail.
			settled, ok := usage.BillingUsage.CanonicalUsage()
			require.True(t, ok)
			assert.Equal(t, tc.wantAudioTokens, settled.CompletionTokenDetails.AudioTokens)
		})
	}
}

// When upstream reports no completion tokens, with prompt-only usageMetadata or
// none at all, the stream and non-stream paths apply the same inline output
// estimate.
func TestGeminiHandlersWithoutCompletionUsageEstimateInlineOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	audioPart := dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "audio/l16; rate=24000; channels=1", Data: pcm100ms}}
	imagePart := dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}}

	for _, tc := range []struct {
		name                 string
		parts                []dto.GeminiPart
		wantCompletionTokens int
		wantAudioTokens      int
	}{
		{"audio", []dto.GeminiPart{audioPart, audioPart, audioPart}, 8, 8},
		{"image", []dto.GeminiPart{imagePart, imagePart}, 2 * 1400, 0},
	} {
		for _, metadata := range []struct {
			name                 string
			usageMetadata        dto.GeminiUsageMetadata
			wantPromptTokens     int
			overrideCompletion   int
			hasUpstreamUsageComp bool
		}{
			{"prompt-only usage", dto.GeminiUsageMetadata{PromptTokenCount: 151, TotalTokenCount: 151}, 151, 0, false},
			{"no usage", dto.GeminiUsageMetadata{}, 20, 0, false},
			{"upstream completion usage", dto.GeminiUsageMetadata{PromptTokenCount: 151, CandidatesTokenCount: 42, TotalTokenCount: 193}, 151, 42, true},
		} {
			payload := dto.GeminiChatResponse{
				Candidates: []dto.GeminiChatCandidate{
					{Content: dto.GeminiChatContent{Role: "model", Parts: tc.parts}},
				},
				UsageMetadata: metadata.usageMetadata,
			}
			body, err := common.Marshal(payload)
			require.NoError(t, err)

			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", tc.name, metadata.name, stream), func(t *testing.T) {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

					info := &relaycommon.RelayInfo{
						RelayFormat:     types.RelayFormatGemini,
						OriginModelName: "gemini-3.8-flash",
						ChannelMeta: &relaycommon.ChannelMeta{
							UpstreamModelName: "gemini-3.8-flash",
						},
					}
					info.SetEstimatePromptTokens(20)

					var usage *dto.Usage
					var newAPIError *types.NewAPIError
					if stream {
						resp := &http.Response{
							Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n" + "data: [DONE]\n")),
						}
						usage, newAPIError = geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
							return true
						})
					} else {
						resp := &http.Response{
							Body: io.NopCloser(bytes.NewReader(body)),
						}
						usage, newAPIError = GeminiChatHandler(c, info, resp)
					}
					require.Nil(t, newAPIError)
					require.NotNil(t, usage)
					wantCompletion := tc.wantCompletionTokens
					wantAudio := tc.wantAudioTokens
					if metadata.hasUpstreamUsageComp {
						wantCompletion = metadata.overrideCompletion
						wantAudio = 0
					}
					assert.Equal(t, metadata.wantPromptTokens, usage.PromptTokens)
					assert.Equal(t, wantCompletion, usage.CompletionTokens)
					assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
					require.NotNil(t, usage.BillingUsage)
					require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
					assert.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
					settled, ok := usage.BillingUsage.CanonicalUsage()
					require.True(t, ok)
					assert.Equal(t, wantAudio, settled.CompletionTokenDetails.AudioTokens)
				})
			}
		}
	}
}

// Prompt-only usageMetadata that already lists an AUDIO candidate detail
// without a candidate token count settles with the inline estimate once
// (replacing it for audio, or clearing it when only images were estimated).
func TestGeminiChatHandlerPromptOnlyUsageReplacesAudioCandidateDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	audioPart := dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "audio/l16; rate=24000; channels=1", Data: pcm100ms}}
	imagePart := dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}}

	for _, tc := range []struct {
		name                 string
		parts                []dto.GeminiPart
		wantCompletionTokens int
		wantAudioTokens      int
	}{
		{"audio replaces stale detail", []dto.GeminiPart{audioPart, audioPart, audioPart}, 8, 8},
		{"image clears stale audio detail", []dto.GeminiPart{imagePart}, 1400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			info := &relaycommon.RelayInfo{
				RelayFormat:     types.RelayFormatGemini,
				OriginModelName: "gemini-3.8-flash",
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "gemini-3.8-flash",
				},
			}

			payload := dto.GeminiChatResponse{
				Candidates: []dto.GeminiChatCandidate{
					{Content: dto.GeminiChatContent{Role: "model", Parts: tc.parts}},
				},
				UsageMetadata: dto.GeminiUsageMetadata{
					PromptTokenCount:        151,
					TotalTokenCount:         151,
					CandidatesTokensDetails: []dto.GeminiPromptTokensDetails{{Modality: "audio", TokenCount: 5}},
				},
			}
			body, err := common.Marshal(payload)
			require.NoError(t, err)

			usage, newAPIError := GeminiChatHandler(c, info, &http.Response{Body: io.NopCloser(bytes.NewReader(body))})
			require.Nil(t, newAPIError)
			require.NotNil(t, usage)
			assert.Equal(t, tc.wantCompletionTokens, usage.CompletionTokens)
			assert.Equal(t, tc.wantAudioTokens, usage.CompletionTokenDetails.AudioTokens)
			require.NotNil(t, usage.BillingUsage)
			settled, ok := usage.BillingUsage.CanonicalUsage()
			require.True(t, ok)
			assert.Equal(t, tc.wantCompletionTokens, settled.CompletionTokens)
			assert.Equal(t, tc.wantAudioTokens, settled.CompletionTokenDetails.AudioTokens)
		})
	}
}

// Google Search grounding records the unique trimmed non-empty queries
// (webSearchQueries and imageSearchQueries of every candidate, across every
// stream frame) under google_search and one grounded prompt when any query
// exists; the tool price table decides which of the two a model pays for.
func TestGeminiHandlersCountGroundingQueries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})
	const text = `"content":{"role":"model","parts":[{"text":"ok"}]}`
	grounded := func(queries int) map[string]int {
		return map[string]int{dto.BuildInToolGoogleSearch: queries, relaycommon.GoogleSearchGroundedPromptTool: 1}
	}

	tests := []struct {
		name   string
		stream bool
		frames []string
		want   map[string]int
	}{
		{
			name:   "native non-stream",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["Shanghai weather today","current weather in Shanghai"],"groundingChunks":[{"web":{"uri":"https://a.example"}}]}}]}`},
			want:   grounded(2),
		},
		{
			name:   "duplicates, whitespace and empty queries",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["go release",""," go release ","   ","tokyo"]}}]}`},
			want:   grounded(2),
		},
		{
			name:   "union across candidates",
			frames: []string{`{"candidates":[{"index":0,` + text + `,"groundingMetadata":{"webSearchQueries":["q1","q2"]}},{"index":1,` + text + `,"groundingMetadata":{"webSearchQueries":["q2","q3"]}}]}`},
			want:   grounded(3),
		},
		{
			name:   "image queries count like web queries",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["cat breeds","orange cat"],"imageSearchQueries":["orange tabby cat photo","orange cat"]}}]}`},
			want:   grounded(3),
		},
		{
			name:   "only empty queries",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["","  "],"imageSearchQueries":[""]}}]}`},
			want:   map[string]int{},
		},
		{
			name:   "no grounding metadata",
			frames: []string{`{"candidates":[{` + text + `,"finishReason":"STOP"}]}`},
			want:   map[string]int{},
		},
		{
			name:   "stream union across frames",
			stream: true,
			frames: []string{
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q1"]}}]}`,
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q2","q1 "]}}]}`,
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q3"]}}]}`,
			},
			want: grounded(3),
		},
		{
			name:   "stream repeats are not added up",
			stream: true,
			frames: []string{
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q1","q2"]}}]}`,
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q1","q2"]}}]}`,
			},
			want: grounded(2),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:generateContent", nil)
			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3.7-flash",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.7-flash"},
			}
			var apiErr *types.NewAPIError
			if tt.stream {
				body := "data: " + strings.Join(tt.frames, "\ndata: ") + "\ndata: [DONE]\n"
				_, apiErr = geminiStreamHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, func(string, *dto.GeminiChatResponse) bool {
					return true
				})
			} else {
				_, apiErr = GeminiTextGenerationHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(tt.frames[0]))})
			}
			require.Nil(t, apiErr)
			counts := map[string]int{}
			if info.ResponsesUsageInfo != nil {
				for name, tool := range info.ResponsesUsageInfo.BuiltInTools {
					counts[name] = tool.CallCount
				}
			}
			assert.Equal(t, tt.want, counts)
		})
	}
}

// A Gemini upstream that closes before any candidate finishes used to be
// finished as a normal answer; each client dialect now gets one error event
// instead, a finished stream is unchanged, and an in-band error envelope
// stops the stream with the upstream's own status and message.
func TestGeminiStreamHandlersReportCutUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	const (
		partial  = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]},"index":0}],"modelVersion":"gemini-2.5-flash"}`
		finished = `{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6},"modelVersion":"gemini-2.5-flash"}`
		inBand   = `{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`
		blocked  = `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":4,"totalTokenCount":4},"modelVersion":"gemini-2.5-flash"}`
		message  = "upstream stream ended before the response was complete (request id: gemini-cut)"
	)
	runBody := func(t *testing.T, format types.RelayFormat, body io.Reader) (*relaycommon.RelayInfo, *dto.Usage, *types.NewAPIError, *gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Set(common.RequestIdKey, "gemini-cut")
		info := &relaycommon.RelayInfo{
			RelayFormat:        format,
			IsStream:           true,
			ShouldIncludeUsage: true,
			DisablePing:        true,
			OriginModelName:    "gemini-2.5-flash",
			ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gemini-2.5-flash"},
			ClaudeConvertInfo:  &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
		}
		info.SetEstimatePromptTokens(4)
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
		var usage *dto.Usage
		var apiErr *types.NewAPIError
		switch format {
		case types.RelayFormatGemini:
			usage, apiErr = GeminiTextGenerationStreamHandler(c, info, resp)
		case types.RelayFormatOpenAIResponses:
			usage, apiErr = GeminiResponsesStreamHandler(c, info, resp)
		default:
			usage, apiErr = GeminiChatStreamHandler(c, info, resp)
		}
		return info, usage, apiErr, c, recorder
	}
	run := func(t *testing.T, format types.RelayFormat, events ...string) (*relaycommon.RelayInfo, *dto.Usage, *types.NewAPIError, *gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		var body strings.Builder
		for _, event := range events {
			body.WriteString("data: " + event + "\n\n")
		}
		return runBody(t, format, strings.NewReader(body.String()))
	}

	t.Run("openai cut", func(t *testing.T) {
		info, usage, apiErr, _, recorder := run(t, types.RelayFormatOpenAI, partial)
		require.Nil(t, apiErr)
		assert.Positive(t, usage.CompletionTokens, "the delivered output is still billed")
		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, `"error"`))
		assert.True(t, strings.HasSuffix(got, `data: {"error":{"message":"`+message+`","type":"upstream_error","param":"","code":"upstream_stream_interrupted"}}`+"\n\ndata: [DONE]\n\n"), got)
		usageChunk := strings.Index(got, `"choices":[],"usage":{"prompt_tokens":4`)
		assert.Less(t, strings.Index(got, `"content":"hel"`), usageChunk)
		assert.Less(t, usageChunk, strings.Index(got, `{"error"`), "the usage chunk still precedes the error")
		assert.NotContains(t, got, `"finish_reason":"`)
		assert.Equal(t, "upstream_stream_interrupted", info.StreamStatus.OutcomeSnapshot().ErrorCode)
	})

	t.Run("openai complete", func(t *testing.T) {
		info, _, apiErr, _, recorder := run(t, types.RelayFormatOpenAI, partial, finished)
		require.Nil(t, apiErr)
		got := recorder.Body.String()
		assert.NotContains(t, got, `"error"`)
		assert.True(t, strings.HasSuffix(got, "}\n\ndata: [DONE]\n\n"), got)
		assert.Contains(t, got, `"finish_reason":"stop"`)
		assert.Equal(t, string(relaycommon.ResponseOutcomeCompleted), info.StreamStatus.ResponseOutcome())
	})

	t.Run("claude cut", func(t *testing.T) {
		info, _, apiErr, _, recorder := run(t, types.RelayFormatClaude, partial)
		require.Nil(t, apiErr)
		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, "event: error\n"))
		assert.True(t, strings.HasSuffix(got, "event: error\n"+`data: {"error":{"type":"api_error","message":"`+message+`"},"type":"error"}`+"\n\n"), got)
		assert.NotContains(t, got, "event: message_delta\n", "no stop the upstream never sent")
		assert.NotContains(t, got, "event: message_stop\n")
		assert.True(t, info.StreamStatus.ResponseFailed())
	})

	t.Run("native cut", func(t *testing.T) {
		info, _, apiErr, _, recorder := run(t, types.RelayFormatGemini, partial)
		require.Nil(t, apiErr)
		assert.Equal(t, "data: "+partial+"\n\n"+`data: {"error":{"code":502,"message":"`+message+`","status":"UNAVAILABLE"}}`+"\n\n", recorder.Body.String())
		assert.True(t, info.StreamStatus.ResponseFailed())
	})

	t.Run("native complete", func(t *testing.T) {
		_, _, apiErr, _, recorder := run(t, types.RelayFormatGemini, partial, finished)
		require.Nil(t, apiErr)
		assert.Equal(t, "data: "+partial+"\n\ndata: "+finished+"\n\n", recorder.Body.String())
	})

	t.Run("in-band error", func(t *testing.T) {
		info, _, apiErr, c, recorder := run(t, types.RelayFormatGemini, partial, inBand)
		require.NotNil(t, apiErr, "the error envelope must not be swallowed")
		assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
		assert.Equal(t, "The model is overloaded.", apiErr.Error())
		assert.Equal(t, http.StatusServiceUnavailable, info.StreamStatus.OutcomeSnapshot().ErrorStatus)
		assert.Equal(t, "data: "+partial+"\n\n", recorder.Body.String(), "the stream stops at the error without an ending of its own")

		// What the relay error writer then sends to the client.
		helper.WriteStreamError(c, info.RelayFormat, apiErr)
		assert.True(t, strings.HasSuffix(recorder.Body.String(), `data: {"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`+"\n\n"), recorder.Body.String())
	})

	// A blocked prompt is one chunk with promptFeedback and no candidate:
	// Gemini's complete answer, which every dialect still ends normally.
	for _, tc := range []struct {
		format types.RelayFormat
		ending string
	}{
		{format: types.RelayFormatGemini, ending: "data: " + blocked + "\n\n"},
		{format: types.RelayFormatOpenAI, ending: "data: [DONE]\n\n"},
		{format: types.RelayFormatClaude, ending: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{format: types.RelayFormatOpenAIResponses, ending: "event: response.completed\n"},
	} {
		t.Run("prompt blocked "+string(tc.format), func(t *testing.T) {
			info, _, apiErr, _, recorder := run(t, tc.format, blocked)
			require.Nil(t, apiErr)
			got := recorder.Body.String()
			if tc.format == types.RelayFormatOpenAIResponses {
				assert.Contains(t, got, tc.ending)
			} else {
				assert.True(t, strings.HasSuffix(got, tc.ending), got)
			}
			assert.NotContains(t, got, "upstream_stream_interrupted")
			assert.NotContains(t, got, "event: error\n")
			assert.False(t, info.StreamStatus.ResponseFailed())
			assert.True(t, info.PerformanceBusinessRejection)
		})
	}

	// A Responses client gets the same error event for an early EOF and for a
	// read error, and both are recorded as an interrupted stream. The read
	// error itself stays in the server log.
	for _, tc := range []struct {
		name      string
		body      io.Reader
		endReason relaycommon.StreamEndReason
	}{
		{name: "responses cut", body: strings.NewReader("data: " + partial + "\n\n"), endReason: relaycommon.StreamEndReasonEOF},
		{name: "responses read error", body: io.MultiReader(strings.NewReader("data: "+partial+"\n\n"), iotest.ErrReader(errors.New("read tcp 10.0.0.7:443: connection reset by peer"))), endReason: relaycommon.StreamEndReasonScannerErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, usage, apiErr, _, recorder := runBody(t, types.RelayFormatOpenAIResponses, tc.body)
			require.Nil(t, apiErr)
			assert.Positive(t, usage.CompletionTokens, "the delivered output is still billed")
			got := recorder.Body.String()
			assert.Equal(t, 1, strings.Count(got, "event: error\n"))
			assert.True(t, strings.HasSuffix(got, "event: error\n"+`data: {"code":"upstream_stream_interrupted","message":"`+message+`","param":"","type":"error"}`+"\n\n"), got)
			assert.NotContains(t, got, "response.completed")
			assert.NotContains(t, got, "10.0.0.7")
			outcome := info.StreamStatus.OutcomeSnapshot()
			assert.Equal(t, tc.endReason, outcome.EndReason)
			assert.Equal(t, relaycommon.ResponseOutcomeFailed, outcome.Response)
			assert.Equal(t, "upstream_stream_interrupted", outcome.ErrorCode)
		})
	}
}
