package claudemessages

import "github.com/QuantumNous/new-api/relaykit/dto"

// ChatImageURL is the image URL the Claude → Chat converter sends for a
// Claude image source; content moderation checks the same URL.
func ChatImageURL(source *dto.ClaudeMessageSource) string {
	return claudeImageSourceURL(source)
}

// ResponsesSourceURL is the image URL the Claude → Responses converter sends
// for a Claude image source; content moderation checks the same URL.
func ResponsesSourceURL(source *dto.ClaudeMessageSource) string {
	return claudeSourceURL(source)
}
