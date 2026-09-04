package controller

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

// A retry after the client already received part of a streamed answer
// appends a second complete answer to the same response body. Once anything
// has been written the request must not be retried, whatever the error is.
func TestShouldRetryStopsOnceResponseStarted(t *testing.T) {
	retryable := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	fresh := newPinRetryContext()
	assert.True(t, shouldRetry(fresh, retryable, 1), "nothing written yet: retry is still allowed")

	started := newPinRetryContext()
	started.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err := started.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	require.NoError(t, err)
	require.True(t, started.Writer.Written())
	assert.False(t, shouldRetry(started, retryable, 1), "bytes already streamed: never retry")

	channelErr := types.NewError(errors.New("key revoked"), types.ErrorCodeChannelInvalidKey)
	assert.False(t, shouldRetry(started, channelErr, 1), "channel errors are no exception once the stream started")
}

func TestShouldRetryStillRetriesWebSocketRelays(t *testing.T) {
	retryable := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	c.Request.Header.Set("Connection", "Upgrade")
	c.Request.Header.Set("Upgrade", "websocket")
	// The upgrade marks the writer as written before any relay attempt.
	c.Writer.WriteHeaderNow()
	require.True(t, c.Writer.Written())

	assert.True(t, shouldRetry(c, retryable, 1))
}
