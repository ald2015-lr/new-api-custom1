package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Streams the given deltas through sendStreamData with thinking_to_content on
// and returns the concatenated content the client would render.
func renderThinkToContent(t *testing.T, deltas []dto.ChatCompletionsStreamResponseChoiceDelta) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ThinkingContentInfo: relaycommon.ThinkingContentInfo{IsFirstThinkingContent: true},
	}

	for _, delta := range deltas {
		chunk := dto.ChatCompletionsStreamResponse{
			Id:      "chatcmpl-1",
			Object:  "chat.completion.chunk",
			Choices: []dto.ChatCompletionsStreamResponseChoice{{Delta: delta}},
		}
		raw, err := common.Marshal(chunk)
		require.NoError(t, err)
		require.NoError(t, sendStreamData(c, info, string(raw), false, true))
	}

	var rendered strings.Builder
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk dto.ChatCompletionsStreamResponse
		require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(line, "data: "), &chunk))
		for _, choice := range chunk.Choices {
			assert.Empty(t, choice.Delta.GetReasoningContent(), "reasoning must have been folded into content")
			rendered.WriteString(choice.Delta.GetContentString())
		}
	}
	return rendered.String()
}

func reasoningDelta(text string) dto.ChatCompletionsStreamResponseChoiceDelta {
	delta := dto.ChatCompletionsStreamResponseChoiceDelta{}
	delta.SetReasoningContent(text)
	return delta
}

func contentDelta(text string) dto.ChatCompletionsStreamResponseChoiceDelta {
	delta := dto.ChatCompletionsStreamResponseChoiceDelta{}
	delta.SetContentString(text)
	return delta
}

func TestThinkToContentWrapsReasoningAndReopensForInterleavedThinking(t *testing.T) {
	rendered := renderThinkToContent(t, []dto.ChatCompletionsStreamResponseChoiceDelta{
		reasoningDelta("first "),
		reasoningDelta("thought"),
		contentDelta("answer one"),
		reasoningDelta("second thought"),
		contentDelta("answer two"),
	})

	assert.Equal(t, "<think>\nfirst thought\n</think>\nanswer one\n<think>\nsecond thought\n</think>\nanswer two", rendered)
}

func TestThinkToContentKeepsAnswerTextThatSharesTheFirstReasoningDelta(t *testing.T) {
	both := reasoningDelta("thought")
	both.SetContentString("answer")

	rendered := renderThinkToContent(t, []dto.ChatCompletionsStreamResponseChoiceDelta{both})

	assert.Equal(t, "<think>\nthought\n</think>\nanswer", rendered)
}

func TestThinkToContentLeavesPlainAnswersUntouched(t *testing.T) {
	rendered := renderThinkToContent(t, []dto.ChatCompletionsStreamResponseChoiceDelta{
		contentDelta("no "),
		contentDelta("thinking"),
	})

	assert.Equal(t, "no thinking", rendered)
}
