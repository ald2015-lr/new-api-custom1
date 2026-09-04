package geminichat

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiStreamChunk(parts ...dto.GeminiPart) *dto.GeminiChatResponse {
	return &dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Index:   0,
			Content: dto.GeminiChatContent{Role: "model", Parts: parts},
		}},
	}
}

// Gemini 2.5 emits chunks that carry a thought part and an answer part
// together. The thought belongs in reasoning_content and the answer in
// content; labelling the whole chunk by one flag moved answer text into the
// reasoning box and made the client render reasoning -> answer -> reasoning.
func TestStreamResponseGeminiChat2OpenAI_SplitsThoughtAndAnswerInOneChunk(t *testing.T) {
	resp, _ := StreamResponseGeminiChat2OpenAI(geminiStreamChunk(
		dto.GeminiPart{Text: "Let me think.", Thought: true},
		dto.GeminiPart{Text: "The answer is 42."},
	))

	require.Len(t, resp.Choices, 1)
	delta := resp.Choices[0].Delta
	assert.Equal(t, "Let me think.", delta.GetReasoningContent())
	assert.Equal(t, "The answer is 42.", delta.GetContentString())
}

func TestStreamResponseGeminiChat2OpenAI_ThoughtOnlyChunkHasNoContentField(t *testing.T) {
	resp, _ := StreamResponseGeminiChat2OpenAI(geminiStreamChunk(
		dto.GeminiPart{Text: "step one", Thought: true},
		dto.GeminiPart{Text: "step two", Thought: true},
	))

	require.Len(t, resp.Choices, 1)
	delta := resp.Choices[0].Delta
	assert.Equal(t, "step one\nstep two", delta.GetReasoningContent())
	assert.Nil(t, delta.Content)
}

func TestStreamResponseGeminiChat2OpenAI_TextOnlyChunkKeepsContent(t *testing.T) {
	resp, _ := StreamResponseGeminiChat2OpenAI(geminiStreamChunk(
		dto.GeminiPart{Text: "plain answer"},
	))

	require.Len(t, resp.Choices, 1)
	delta := resp.Choices[0].Delta
	assert.Equal(t, "plain answer", delta.GetContentString())
	assert.Empty(t, delta.GetReasoningContent())
}
