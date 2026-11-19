package geminichat

import "github.com/QuantumNous/new-api/relaykit/dto"

// MediaToChatContent is the Chat content part the Gemini → Chat converter
// sends for a part's inlineData or fileData; content moderation checks the
// same part. A non-empty reason means the media has no Chat content part.
func MediaToChatContent(inlineData *dto.GeminiInlineData, fileData *dto.GeminiFileData) (dto.MediaContent, string) {
	return geminiMediaToChatContent(inlineData, fileData)
}
