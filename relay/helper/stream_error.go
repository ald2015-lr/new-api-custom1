package helper

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	streamInterruptedCode    = "upstream_stream_interrupted"
	streamInterruptedMessage = "upstream stream ended before the response was complete"
)

// WriteStreamError delivers a relay error to a client whose SSE response has
// already started. The status line is gone by then, so the error travels as
// an event in the dialect the client speaks and the stream is terminated.
// Writing a JSON body at that point would glue a non-SSE object onto the
// stream, which every strict client reports as a parse error mid-answer.
func WriteStreamError(c *gin.Context, relayFormat types.RelayFormat, apiErr *types.NewAPIError, upstreamURLs ...string) {
	if c == nil || c.Writer == nil || apiErr == nil {
		return
	}
	switch relayFormat {
	case types.RelayFormatClaude:
		payload, err := common.Marshal(gin.H{"type": "error", "error": apiErr.ToClaudeError(upstreamURLs...)})
		if err != nil {
			return
		}
		ClaudeChunkData(c, dto.ClaudeResponse{Type: "error"}, string(payload))
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		openaiErr := apiErr.ToOpenAIError(upstreamURLs...)
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
		// The envelope Google APIs use for errors, status included.
		_ = ObjectData(c, gin.H{"error": gin.H{
			"code":    apiErr.StatusCode,
			"message": apiErr.MaskSensitiveError(upstreamURLs...),
			"status":  googleRPCStatus(apiErr.StatusCode),
		}})
	default:
		// Chat completions and every other data:-only stream. openai
		// compatible SDKs raise a typed error for a {"error": ...} chunk.
		_ = ObjectData(c, gin.H{"error": apiErr.ToOpenAIError(upstreamURLs...)})
		Done(c)
	}
}

// ReportStreamInterruption ends a stream that the upstream cut before its
// terminal event with an error event in the client's dialect, so the client
// does not take the partial answer for a complete one, and records the stream
// as failed. Billing is untouched: callers settle the partial output as they
// did before. For every other stream, including one whose client is gone, it
// writes nothing and reports false. Callers call it where their terminator
// goes and write the terminator only on false; the chat completions error
// event already ends with [DONE].
func ReportStreamInterruption(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if c == nil || info == nil || !info.StreamStatus.Interrupted() || requestContextDone(c) {
		return false
	}
	info.StreamStatus.MarkFailed(streamInterruptedCode, string(types.ErrorTypeUpstreamError), http.StatusBadGateway)
	// As for any stream error, the request must not count as a success
	// against the model request rate limit.
	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	logger.LogError(c, common.LogText("stream interrupted: upstream ended before completion (%s)", info.StreamStatus.Summary()))
	message := common.MessageWithRequestId(streamInterruptedMessage, c.GetString(common.RequestIdKey))
	apiErr := types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    string(types.ErrorTypeUpstreamError),
		Code:    streamInterruptedCode,
	}, http.StatusBadGateway)
	if info.RelayFormat == types.RelayFormatClaude {
		// Anthropic's type for an unexpected server-side failure.
		apiErr = types.WithClaudeError(types.ClaudeError{Type: "api_error", Message: message}, http.StatusBadGateway)
	}
	WriteStreamError(c, info.RelayFormat, apiErr)
	return true
}

// googleRPCStatus is the google.rpc.Code name Google APIs pair with an HTTP
// status in their error envelope.
func googleRPCStatus(httpStatus int) string {
	switch httpStatus {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	}
	switch {
	case httpStatus >= http.StatusInternalServerError:
		return "INTERNAL"
	case httpStatus >= http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	}
	return "UNKNOWN"
}
