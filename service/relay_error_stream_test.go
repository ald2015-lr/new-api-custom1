package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRelayRetryContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

// A retry after the client already received part of a streamed answer
// appends a second complete answer to the same response body. Once anything
// has been written the request must not be retried, whatever the error is.
func TestDecideRelayRetryStopsOnceResponseStarted(t *testing.T) {
	retryable := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	fresh := newRelayRetryContext(t)
	assert.Equal(t, "retry", DecideRelayRetry(fresh, retryable, 1).Action, "nothing written yet: retry is still allowed")

	started := newRelayRetryContext(t)
	started.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err := started.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	require.NoError(t, err)
	require.True(t, started.Writer.Written())

	decision := DecideRelayRetry(started, retryable, 1)
	assert.Equal(t, "stop", decision.Action)
	assert.Equal(t, "response_started", decision.Reason)

	channelErr := types.NewError(errors.New("key revoked"), types.ErrorCodeChannelInvalidKey)
	assert.Equal(t, "stop", DecideRelayRetry(started, channelErr, 1).Action, "channel errors are no exception once the stream started")
}

func TestDecideRelayRetryStillRetriesWebSocketRelays(t *testing.T) {
	retryable := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	c := newRelayRetryContext(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	c.Request.Header.Set("Connection", "Upgrade")
	c.Request.Header.Set("Upgrade", "websocket")
	// The upgrade marks the writer as written before any relay attempt.
	c.Writer.WriteHeaderNow()
	require.True(t, c.Writer.Written())

	assert.Equal(t, "retry", DecideRelayRetry(c, retryable, 1).Action)
}
