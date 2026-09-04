package helper

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
