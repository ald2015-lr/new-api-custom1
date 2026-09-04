package helper

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// WriteStreamError delivers a relay error to a client whose SSE response has
// already started. The status line is gone by then, so the error travels as
// an event in the dialect the client speaks and the stream is terminated.
// Writing a JSON body at that point would glue a non-SSE object onto the
// stream, which every strict client reports as a parse error mid-answer.
func WriteStreamError(c *gin.Context, relayFormat types.RelayFormat, apiErr *types.NewAPIError) {
	if c == nil || c.Writer == nil || apiErr == nil {
		return
	}
	switch relayFormat {
	case types.RelayFormatClaude:
		payload, err := common.Marshal(gin.H{"type": "error", "error": apiErr.ToClaudeError()})
		if err != nil {
			return
		}
		ClaudeChunkData(c, dto.ClaudeResponse{Type: "error"}, string(payload))
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		openaiErr := apiErr.ToOpenAIError()
		payload, err := common.Marshal(gin.H{
			"type":    "error",
			"code":    openaiErr.Code,
			"message": openaiErr.Message,
			"param":   openaiErr.Param,
		})
		if err != nil {
			return
		}
		_ = ResponseChunkData(c, dto.ResponsesStreamResponse{Type: "error"}, string(payload))
	case types.RelayFormatGemini:
		_ = ObjectData(c, gin.H{"error": gin.H{
			"code":    apiErr.StatusCode,
			"message": apiErr.Error(),
		}})
	default:
		// Chat completions and every other data:-only stream. openai
		// compatible SDKs raise a typed error for a {"error": ...} chunk.
		_ = ObjectData(c, gin.H{"error": apiErr.ToOpenAIError()})
		Done(c)
	}
}
