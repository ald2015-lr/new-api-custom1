package openai

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cutChatChunkOne  = `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"hel"}}]}`
	cutChatChunkTwo  = `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"lo"}}]}`
	chatFinishChunk  = `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	chatUsageChunk   = `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	interruptedError = `data: {"error":{"message":"upstream stream ended before the response was complete (request id: responses-test)","type":"upstream_error","param":"","code":"upstream_stream_interrupted"}}` + "\n\n"
	interruptedEvent = "event: error\n"
	// What a Gemini-native client receives for the same cut.
	interruptedGeminiError = `data: {"error":{"code":502,"message":"upstream stream ended before the response was complete (request id: responses-test)","status":"UNAVAILABLE"}}` + "\n\n"
)

func sseBody(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: " + event + "\n\n")
	}
	return body.String()
}

func setupStreamInterruptionTest(t *testing.T) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		gin.SetMode(oldMode)
		constant.StreamingTimeout = oldTimeout
	})
}

func requireInterruptedStatus(t *testing.T, info *relaycommon.RelayInfo) {
	t.Helper()
	outcome := info.StreamStatus.OutcomeSnapshot()
	require.Equal(t, relaycommon.ResponseOutcomeFailed, outcome.Response)
	assert.Equal(t, "upstream_stream_interrupted", outcome.ErrorCode)
}

// A chat completions upstream that closes without finish_reason or [DONE]
// used to end like a finished answer; now the client sees one error event
// before its terminator, and a finished stream is relayed byte for byte.
func TestOaiStreamHandlerReportsCutUpstream(t *testing.T) {
	setupStreamInterruptionTest(t)
	completeBody := sseBody(cutChatChunkOne, chatFinishChunk, chatUsageChunk, "[DONE]")

	t.Run("openai cut", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, sseBody(cutChatChunkOne, cutChatChunkTwo), true)
		info.RelayMode = relayconstant.RelayModeChatCompletions

		usage, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)
		require.NotNil(t, usage)
		assert.Positive(t, usage.CompletionTokens, "the delivered output is still billed")

		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, `"error"`))
		assert.True(t, strings.HasSuffix(got, interruptedError+"data: [DONE]\n\n"), got)
		requireOrderedSubstrings(t, got, "data: "+cutChatChunkOne+"\n\n", "data: "+cutChatChunkTwo+"\n\n", `"usage":{`, interruptedError)
		assert.NotContains(t, got, `"finish_reason":"stop"`)
		requireInterruptedStatus(t, info)
	})

	t.Run("openai complete", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, completeBody, true)

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)
		assert.Equal(t, completeBody, recorder.Body.String())
		assert.Equal(t, string(relaycommon.ResponseOutcomeCompleted), info.StreamStatus.ResponseOutcome())
	})

	t.Run("claude cut", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, sseBody(cutChatChunkOne, cutChatChunkTwo), true)
		info.RelayFormat = types.RelayFormatClaude
		info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)

		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, interruptedEvent))
		requireOrderedSubstrings(t, got, "event: message_start\n", `"text":"lo"`, interruptedEvent+`data: {"error":{"type":"api_error",`)
		assert.NotContains(t, got, "event: message_delta\n", "no stop the upstream never sent")
		assert.NotContains(t, got, "event: message_stop\n")
		requireInterruptedStatus(t, info)
	})

	t.Run("claude cut after usage", func(t *testing.T) {
		// The converter closes the message on a usage chunk even without a
		// finish_reason; for a cut stream that close is never sent.
		c, recorder, resp, info := newResponsesChatTestContext(t, sseBody(cutChatChunkOne, chatUsageChunk), true)
		info.RelayFormat = types.RelayFormatClaude
		info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)

		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, interruptedEvent))
		requireOrderedSubstrings(t, got, `"text":"hel"`, interruptedEvent+`data: {"error":{"type":"api_error",`)
		assert.NotContains(t, got, "event: message_delta\n")
		assert.NotContains(t, got, "event: message_stop\n")
		requireInterruptedStatus(t, info)
	})

	t.Run("claude complete", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, completeBody, true)
		info.RelayFormat = types.RelayFormatClaude
		info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)

		got := recorder.Body.String()
		assert.True(t, strings.HasSuffix(got, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"), got)
		assert.NotContains(t, got, interruptedEvent)
	})

	t.Run("gemini cut", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, sseBody(cutChatChunkOne, cutChatChunkTwo), true)
		info.RelayFormat = types.RelayFormatGemini

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)

		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, `"error"`))
		requireOrderedSubstrings(t, got, `"text":"hel"`, `"text":"lo"`, interruptedGeminiError)
		assert.True(t, strings.HasSuffix(got, interruptedGeminiError), got)
		assert.NotContains(t, got, `"finishReason":"STOP"`, "no finish the upstream never sent")
		requireInterruptedStatus(t, info)
	})

	t.Run("gemini complete", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, completeBody, true)
		info.RelayFormat = types.RelayFormatGemini

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)

		got := recorder.Body.String()
		assert.Contains(t, got, `"finishReason":"STOP"`)
		assert.NotContains(t, got, `"error"`)
		assert.Equal(t, string(relaycommon.ResponseOutcomeCompleted), info.StreamStatus.ResponseOutcome())
	})

	t.Run("client gone", func(t *testing.T) {
		// An upstream that has not sent anything yet when the client leaves.
		body, upstream := io.Pipe()
		t.Cleanup(func() { _ = upstream.Close() })
		c, recorder, resp, info := newResponsesChatTestContext(t, "", true)
		resp.Body = body
		ctx, cancel := context.WithCancel(c.Request.Context())
		cancel()
		c.Request = c.Request.WithContext(ctx)

		_, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
		assert.NotContains(t, recorder.Body.String(), `"error"`)
		assert.False(t, info.StreamStatus.ResponseFailed())
	})
}

func TestOaiResponsesStreamHandlerReportsCutUpstream(t *testing.T) {
	setupStreamInterruptionTest(t)
	created := `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`
	delta := `{"type":"response.output_text.delta","delta":"hello"}`
	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`
	// The gateway relays each Responses event with its type in an event: line.
	relayedCreated := "event: response.created\ndata: " + created + "\n\n"
	relayedDelta := "event: response.output_text.delta\ndata: " + delta + "\n\n"
	relayedCompleted := "event: response.completed\ndata: " + completed + "\n\n"
	interrupted := interruptedEvent + `data: {"code":"upstream_stream_interrupted","message":"upstream stream ended before the response was complete (request id: responses-test)","param":"","type":"error"}` + "\n\n"

	for _, tc := range []struct {
		name, upstream, client string
	}{
		{name: "cut after output", upstream: sseBody(created, delta), client: relayedCreated + relayedDelta + interrupted},
		{name: "cut before output", upstream: sseBody(created), client: relayedCreated + interrupted},
		{name: "complete", upstream: sseBody(created, delta, completed, "[DONE]"), client: relayedCreated + relayedDelta + relayedCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder, resp, info := newResponsesChatTestContext(t, tc.upstream, true)
			info.RelayFormat = types.RelayFormatOpenAIResponses
			info.SetEstimatePromptTokens(7)

			usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
			require.Nil(t, apiErr)
			assert.Equal(t, tc.client, recorder.Body.String())
			if tc.name == "complete" {
				assert.Equal(t, 3, usage.PromptTokens)
				return
			}
			assert.Equal(t, 7, usage.PromptTokens, "a started stream bills its prompt as before, cut or not")
			requireInterruptedStatus(t, info)
		})
	}
}

// A Responses upstream that closes before response.completed is not finalized
// into a chat or Claude stream that looks complete.
func TestOaiResponsesToChatStreamHandlerReportsCutUpstream(t *testing.T) {
	setupStreamInterruptionTest(t)
	body := sseBody(
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-test","created_at":1710000000}}`,
		`{"type":"response.output_text.delta","delta":"hello"}`,
	)

	t.Run("openai", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, body, true)

		usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
		require.Nil(t, apiErr)
		require.NotNil(t, usage)

		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, `"error"`))
		assert.True(t, strings.HasSuffix(got, interruptedError+"data: [DONE]\n\n"), got)
		requireOrderedSubstrings(t, got, `"content":"hello"`, `"usage":{`, interruptedError)
		assert.NotContains(t, got, `"finish_reason":"`)
		requireInterruptedStatus(t, info)
	})

	t.Run("claude", func(t *testing.T) {
		c, recorder, resp, info := newResponsesChatTestContext(t, body, true)
		info.RelayFormat = types.RelayFormatClaude

		_, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
		require.Nil(t, apiErr)

		got := recorder.Body.String()
		assert.Equal(t, 1, strings.Count(got, interruptedEvent))
		requireOrderedSubstrings(t, got, "event: message_start\n", `"text":"hello"`, interruptedEvent)
		assert.NotContains(t, got, "event: message_stop\n")
		requireInterruptedStatus(t, info)
	})

	// An upstream that ends the response with an error event or a
	// cancellation and then closes: the client gets that as an error event
	// in its own dialect, never a finish, and the delivered output is billed.
	for _, tc := range []struct {
		name, last, message string
		outcome             relaycommon.ResponseOutcome
	}{
		{name: "error event", last: `{"type":"error","code":"server_error","message":"upstream boom"}`, message: "upstream boom", outcome: relaycommon.ResponseOutcomeFailed},
		{name: "cancelled", last: `{"type":"response.cancelled","response":{"id":"resp_1","status":"cancelled"}}`, message: "responses stream error: response.cancelled", outcome: relaycommon.ResponseOutcomeCancelled},
		{name: "canceled", last: `{"type":"response.canceled","response":{"id":"resp_1","status":"cancelled"}}`, message: "responses stream error: response.canceled", outcome: relaycommon.ResponseOutcomeCancelled},
	} {
		for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
			t.Run(tc.name+" "+string(format), func(t *testing.T) {
				c, recorder, resp, info := newResponsesChatTestContext(t, body+sseBody(tc.last), true)
				info.RelayFormat = format

				usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
				require.Nil(t, apiErr)
				require.NotNil(t, usage)
				assert.Positive(t, usage.CompletionTokens, "the delivered output is still billed")
				assert.Equal(t, string(tc.outcome), info.StreamStatus.ResponseOutcome())

				got := recorder.Body.String()
				if format == types.RelayFormatOpenAI {
					assert.Equal(t, 1, strings.Count(got, `"error"`))
					requireOrderedSubstrings(t, got, `"content":"hello"`, `data: {"error":{"message":"`+tc.message+`"`)
					assert.True(t, strings.HasSuffix(got, "}}\n\ndata: [DONE]\n\n"), got)
					assert.NotContains(t, got, `"finish_reason":"`)
					return
				}
				assert.Equal(t, 1, strings.Count(got, interruptedEvent))
				requireOrderedSubstrings(t, got, `"text":"hello"`, interruptedEvent+`data: {"error":{`, tc.message)
				assert.NotContains(t, got, "event: message_delta\n")
				assert.NotContains(t, got, "event: message_stop\n")
			})
		}
	}
}
