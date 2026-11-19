package relayconvert

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
	claudemessages "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/claude_messages"
	geminichat "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/gemini_chat"
)

// ClaudeChatImageURL is the image_url the Claude → Chat Completions
// conversion sends for a Claude image source ("" when it sends none), so
// content moderation checks what the upstream receives.
func ClaudeChatImageURL(source *dto.ClaudeMessageSource) string {
	return claudemessages.ChatImageURL(source)
}

// ClaudeResponsesSourceURL is the image URL the Claude → Responses
// conversion sends for a Claude image source ("" when it sends none).
func ClaudeResponsesSourceURL(source *dto.ClaudeMessageSource) string {
	return claudemessages.ResponsesSourceURL(source)
}

// GeminiMediaToChatContent is the Chat content part the Gemini → Chat
// Completions conversion sends for a part's inlineData or fileData; a
// non-empty reason means it sends none.
func GeminiMediaToChatContent(inlineData *dto.GeminiInlineData, fileData *dto.GeminiFileData) (dto.MediaContent, string) {
	if inlineData == nil && fileData == nil {
		return dto.MediaContent{}, "no media"
	}
	return geminichat.MediaToChatContent(inlineData, fileData)
}
