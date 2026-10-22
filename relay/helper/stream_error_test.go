package helper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStartedStream(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	SetEventStreamHeaders(c)
	require.NoError(t, StringData(c, `{"id":"chatcmpl-1","choices":[{"delta":{"content":"partial"}}]}`))
	require.True(t, c.Writer.Written())
	return c, recorder
}

func TestWriteStreamErrorAppendsOpenAIErrorChunkAndDone(t *testing.T) {
	c, recorder := newStartedStream(t)
	apiErr := types.NewOpenAIError(errors.New("upstream overloaded"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	WriteStreamError(c, types.RelayFormatOpenAI, apiErr)

	body := recorder.Body.String()
	assert.Equal(t, http.StatusOK, recorder.Code, "the committed status line must not change")
	assert.Contains(t, body, `data: {"id":"chatcmpl-1"`)
	assert.Contains(t, body, `data: {"error":{`)
	assert.Contains(t, body, `"message":"upstream overloaded"`)
	assert.Contains(t, body, "data: [DONE]")
	assert.NotContains(t, body, "\n{\"error\"", "a bare JSON object must never be glued onto the SSE stream")
}

func TestWriteStreamErrorUsesClaudeErrorEvent(t *testing.T) {
	c, recorder := newStartedStream(t)
	apiErr := types.NewOpenAIError(errors.New("upstream overloaded"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	WriteStreamError(c, types.RelayFormatClaude, apiErr)

	body := recorder.Body.String()
	assert.Contains(t, body, "event: error\n")
	assert.Contains(t, body, `data: {"error":{`)
	assert.Contains(t, body, `"type":"error"`)
	assert.NotContains(t, body, "[DONE]", "Anthropic streams have no [DONE] sentinel")
}

func TestWriteStreamErrorIgnoresNilInputs(t *testing.T) {
	c, recorder := newStartedStream(t)
	before := recorder.Body.Len()

	WriteStreamError(nil, types.RelayFormatOpenAI, nil)
	WriteStreamError(c, types.RelayFormatOpenAI, nil)

	assert.Equal(t, before, recorder.Body.Len())
}

func newEndedStreamInfo(format types.RelayFormat, reason relaycommon.StreamEndReason) *relaycommon.RelayInfo {
	status := relaycommon.NewStreamStatus()
	status.RequireTerminal()
	status.SetEndReason(reason, nil)
	return &relaycommon.RelayInfo{RelayFormat: format, StreamStatus: status}
}

func TestReportStreamInterruptionWritesOneErrorInTheClientDialect(t *testing.T) {
	const message = "upstream stream ended before the response was complete (request id: req-cut)"
	for _, tc := range []struct {
		format types.RelayFormat
		tail   string
	}{
		{format: types.RelayFormatOpenAI, tail: `data: {"error":{"message":"` + message + `","type":"upstream_error","param":"","code":"upstream_stream_interrupted"}}` + "\n\ndata: [DONE]\n\n"},
		{format: types.RelayFormatClaude, tail: "event: error\n" + `data: {"error":{"type":"api_error","message":"` + message + `"},"type":"error"}` + "\n\n\n"},
		{format: types.RelayFormatOpenAIResponses, tail: "event: error\n" + `data: {"code":"upstream_stream_interrupted","message":"` + message + `","param":"","type":"error"}` + "\n\n"},
		{format: types.RelayFormatGemini, tail: `data: {"error":{"code":502,"message":"` + message + `","status":"UNAVAILABLE"}}` + "\n\n"},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			c, recorder := newStartedStream(t)
			c.Set(common.RequestIdKey, "req-cut")
			before := recorder.Body.String()
			info := newEndedStreamInfo(tc.format, relaycommon.StreamEndReasonEOF)

			require.True(t, ReportStreamInterruption(c, info))

			assert.Equal(t, tc.tail, strings.TrimPrefix(recorder.Body.String(), before))
			outcome := info.StreamStatus.OutcomeSnapshot()
			assert.Equal(t, relaycommon.ResponseOutcomeFailed, outcome.Response)
			assert.Equal(t, "upstream_stream_interrupted", outcome.ErrorCode)
			assert.Equal(t, "upstream_error", outcome.ErrorType)
			assert.Equal(t, http.StatusBadGateway, outcome.ErrorStatus)
			status, _ := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
			assert.True(t, status.ResponseFailed(), "a cut stream must not consume the successful-request rate limit")
			assert.False(t, ReportStreamInterruption(c, info), "a failed stream is reported once")
		})
	}
}

func TestReportStreamInterruptionLeavesOtherStreamsAlone(t *testing.T) {
	completed := newEndedStreamInfo(types.RelayFormatOpenAI, relaycommon.StreamEndReasonEOF)
	completed.StreamStatus.MarkCompleted()
	for _, tc := range []struct {
		name       string
		info       *relaycommon.RelayInfo
		clientLeft bool
	}{
		{name: "completed", info: completed},
		{name: "done sentinel", info: newEndedStreamInfo(types.RelayFormatOpenAI, relaycommon.StreamEndReasonDone)},
		{name: "client gone", info: newEndedStreamInfo(types.RelayFormatClaude, relaycommon.StreamEndReasonClientGone)},
		{name: "cut stream whose client left", info: newEndedStreamInfo(types.RelayFormatOpenAI, relaycommon.StreamEndReasonEOF), clientLeft: true},
		{name: "no stream state", info: &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := newStartedStream(t)
			if tc.clientLeft {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			before := recorder.Body.Len()

			assert.False(t, ReportStreamInterruption(c, tc.info))
			assert.Equal(t, before, recorder.Body.Len())
			assert.False(t, tc.info.StreamStatus.ResponseFailed())
		})
	}
}
