package claude

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ClaudeResponsesStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	responseID := helper.GetResponseID(c)
	created := common.GetTimestamp()
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatClaude, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:                 responseID,
		Model:              info.UpstreamModelName,
		Created:            created,
		EmitSequenceNumber: true,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	hostedBridge := relayconvert.NewClaudeHostedStreamBridge()

	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   responseID,
		Created:      created,
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	var streamErr *types.NewAPIError
	// streamFailed means a Responses-native terminal error was sent successfully.
	// In that case the scanner stops without a transport error and the partial
	// upstream usage remains billable.
	streamFailed := false

	sendResponsesEvent := func(eventType string, payload dto.ResponsesStreamResponse) bool {
		payload.Type = eventType
		data, err := common.Marshal(payload)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return false
		}
		if err := helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: eventType}, string(data)); err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			return false
		}
		return true
	}
	sendResult := func(result relayconvert.ResponseResult) bool {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			streamErr = types.NewOpenAIError(
				fmt.Errorf("expected OpenAI Responses stream event, got %T", result.Value),
				types.ErrorCodeBadResponse,
				http.StatusInternalServerError,
			)
			return false
		}
		return sendResponsesEvent(event.Type, event.Payload)
	}
	// failResponsesStream ends the stream with the Responses failure events.
	// The request then returns no error, so the failure, under the upstream's
	// code, is recorded here for health, the request policy, the consume log
	// and the model rate limit, as the native Responses path does.
	failResponsesStream := func(code string, err error) bool {
		failureResults, handled := state.FailResponsesStream("server_error", err.Error(), "")
		if !handled {
			return false
		}
		for _, result := range failureResults {
			if !sendResult(result) {
				return true
			}
		}
		streamFailed = true
		info.StreamStatus.MarkFailed(code, string(types.ErrorTypeUpstreamError), http.StatusInternalServerError)
		common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
		return true
	}

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var claudeResponse dto.ClaudeResponse
		if err := common.UnmarshalJsonStr(data, &claudeResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal Claude stream event: %s", err.Error()))
			if failResponsesStream("server_error", err) {
				// A nil streamErr here is intentional: the protocol-level failure
				// event was delivered, so only the scanner needs to stop.
				stopStream(sr, streamErr)
				return
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
			if failResponsesStream(claudeError.Type, fmt.Errorf("%s", claudeError.Message)) {
				stopStream(sr, streamErr)
				return
			}
			streamErr = types.WithClaudeError(*claudeError, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}

		if claudeResponse.StopReason != "" {
			maybeMarkClaudeRefusal(c, info, claudeResponse.StopReason)
		}
		if claudeResponse.Delta != nil && claudeResponse.Delta.StopReason != nil {
			maybeMarkClaudeRefusal(c, info, *claudeResponse.Delta.StopReason)
		}
		if claudeResponse.Type == "message_stop" {
			info.StreamStatus.MarkCompleted()
		}
		if claudeResponse.Type == "message_start" && claudeResponse.Message != nil {
			info.ObserveResponseModel(claudeResponse.Message.Model)
			info.UpstreamModelName = claudeResponse.Message.Model
		}
		FormatClaudeResponseInfo(&claudeResponse, nil, claudeInfo)
		countClaudeStreamBillableTools(info, &claudeResponse)
		hostedEvents, consumed, err := hostedBridge.Convert(&claudeResponse, state)
		if err != nil {
			if failResponsesStream("server_error", err) {
				stopStream(sr, streamErr)
				return
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		for _, event := range hostedEvents {
			if !sendResponsesEvent(event.Type, event.Payload) {
				sr.Stop(streamErr)
				return
			}
		}
		if consumed {
			return
		}

		results, err := service.ConvertStreamResponseChunk(c, info, state, &claudeResponse)
		if err != nil {
			if failResponsesStream("server_error", err) {
				stopStream(sr, streamErr)
				return
			}
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		for _, result := range results {
			if !sendResult(result) {
				sr.Stop(streamErr)
				return
			}
		}
	})
	info.StreamStatus.RequireTerminal()
	if streamErr != nil {
		return nil, streamErr
	}
	if streamFailed {
		return claudeInfo.Usage, nil
	}

	HandleStreamFinalResponse(c, info, claudeInfo)
	// A cut stream gets an error event instead of a response.completed the
	// upstream never sent; its partial usage is still billed.
	if helper.ReportStreamInterruption(c, info) {
		return claudeInfo.Usage, nil
	}
	openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
	state.SetUsage(&openAIUsage)
	finalResults, err := service.FinalizeStreamResponse(c, info, state)
	if err != nil {
		if failResponsesStream("server_error", err) {
			return claudeInfo.Usage, streamErr
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		if !sendResult(result) {
			return nil, streamErr
		}
	}
	return claudeInfo.Usage, nil
}

// stopStream stops the scanner with streamErr, or with no error when there is
// none. A nil *types.NewAPIError passed as an error is a non-nil interface,
// which StreamResult would record and dereference.
func stopStream(sr *helper.StreamResult, streamErr *types.NewAPIError) {
	if streamErr == nil {
		sr.Stop(nil)
		return
	}
	sr.Stop(streamErr)
}
