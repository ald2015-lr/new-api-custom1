/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package helper

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The synctest bubbles below run on a fake clock: time.Sleep returns as soon
// as every goroutine in the bubble is blocked, after advancing the clock
// exactly, so these tests neither wait in real time nor depend on scheduling.
type keepAliveFixture struct {
	c        *gin.Context
	under    gin.ResponseWriter
	w        *KeepAliveWriter
	recorder *httptest.ResponseRecorder
}

func newKeepAliveFixture(t *testing.T, sse bool) *keepAliveFixture {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Header("X-Oneapi-Request-Id", "request-1")
	under := c.Writer
	w := newKeepAliveWriter(c.Request.Context(), under, sse, time.Second)
	c.Writer = w
	t.Cleanup(w.Stop)
	return &keepAliveFixture{c: c, under: under, w: w, recorder: recorder}
}

// wire returns what reached the client so far. Reading under the writer's
// lock orders it with the keepalive goroutine's writes.
func (f *keepAliveFixture) wire() (*http.Response, string) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	return f.recorder.Result(), f.recorder.Body.String()
}

func TestKeepAliveWriterJSONWhitespaceKeepsResponseRetryable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newKeepAliveFixture(t, false)
		// Headers set after install belong to the handler's response; the
		// keepalive commit must not read them.
		f.c.Header("Content-Length", "13")
		f.c.Header("X-Upstream", "attempt-1")

		time.Sleep(time.Second)
		synctest.Wait()
		wire, body := f.wire()
		assert.Equal(t, "\n", body)
		assert.Equal(t, http.StatusOK, wire.StatusCode)
		assert.Equal(t, "application/json", wire.Header.Get("Content-Type"))
		assert.Equal(t, "no-cache", wire.Header.Get("Cache-Control"))
		assert.Equal(t, "no", wire.Header.Get("X-Accel-Buffering"))
		assert.Equal(t, "request-1", wire.Header.Get("X-Oneapi-Request-Id"))
		assert.Empty(t, wire.Header.Get("Content-Length"))
		assert.Empty(t, wire.Header.Get("X-Upstream"))
		assert.False(t, f.w.Written(), "keepalive bytes must leave the request retryable")
		assert.Equal(t, -1, f.w.Size())
		committed, sse := KeepAliveCommitted(f.c)
		assert.True(t, committed)
		assert.False(t, sse)

		time.Sleep(time.Second)
		synctest.Wait()
		_, body = f.wire()
		assert.Equal(t, "\n\n", body)

		f.c.JSON(http.StatusBadGateway, gin.H{"error": "x"})
		wire, body = f.wire()
		assert.Equal(t, "\n\n{\"error\":\"x\"}", body)
		assert.Equal(t, http.StatusOK, wire.StatusCode, "the wire keeps the committed status")
		assert.Equal(t, http.StatusBadGateway, f.w.Status(), "middleware sees the handler's status")
		assert.True(t, f.w.Written())
		assert.Equal(t, len(`{"error":"x"}`), f.w.Size())

		time.Sleep(5 * time.Second)
		synctest.Wait()
		_, body = f.wire()
		assert.Equal(t, "\n\n{\"error\":\"x\"}", body, "nothing may follow a JSON body")
	})
}

func TestKeepAliveWriterSSEPingsOnlyBetweenEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newKeepAliveFixture(t, true)

		time.Sleep(time.Second)
		synctest.Wait()
		wire, body := f.wire()
		assert.Equal(t, ": PING\n\n", body)
		assert.Equal(t, "text/event-stream", wire.Header.Get("Content-Type"))
		assert.False(t, f.w.Written())

		// ResponseChunkData and ClaudeChunkData send the event line and its
		// data line as separate writes.
		f.c.Render(-1, common.CustomEvent{Data: "event: response.created\n"})
		time.Sleep(5 * time.Second)
		synctest.Wait()
		_, body = f.wire()
		assert.Equal(t, ": PING\n\nevent: response.created\n", body)

		f.c.Render(-1, common.CustomEvent{Data: `data: {"type":"response.created"}`})
		time.Sleep(time.Second / 2)
		synctest.Wait()
		events := ": PING\n\nevent: response.created\ndata: {\"type\":\"response.created\"}\n\n"
		_, body = f.wire()
		assert.Equal(t, events, body, "keepalive waits for a whole idle interval")

		time.Sleep(time.Second / 2)
		synctest.Wait()
		_, body = f.wire()
		assert.Equal(t, events+": PING\n\n", body)
		assert.True(t, f.w.Written())
		assert.Equal(t, http.StatusOK, f.w.Status())
	})
}

func TestKeepAliveWriterStopsForGood(t *testing.T) {
	t.Run("stop joins before the next interval", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newKeepAliveFixture(t, false)
			f.c.Header("X-Pending", "kept")
			f.c.Status(http.StatusTooManyRequests)
			f.w.Stop()
			time.Sleep(5 * time.Second)
			synctest.Wait()
			assert.Empty(t, f.recorder.Body.String())

			// gin finishes the request on its own writer, which must carry
			// the status and headers the handler left on the keepalive writer.
			f.w.Finalize()
			f.under.WriteHeaderNow()
			wire, body := f.wire()
			assert.Empty(t, body)
			assert.Equal(t, http.StatusTooManyRequests, wire.StatusCode)
			assert.Equal(t, "kept", wire.Header.Get("X-Pending"))
		})
	})
	t.Run("client gone", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx, cancel := context.WithCancel(context.Background())
			w := newKeepAliveWriter(ctx, c.Writer, true, time.Second)
			cancel()
			time.Sleep(5 * time.Second)
			synctest.Wait()
			w.Stop()
			assert.Empty(t, recorder.Body.String())
		})
	})
}

// Over a real connection, where net/http enforces a declared Content-Length:
// the handler sets one for its body before the keepalive fires, as
// IOCopyBytesGracefully does, and it must not reach the wire because the
// keepalive bytes come on top. The handler also mutates its header map while
// the keepalive commits, which go test -race checks.
func TestKeepAliveWriterOverConnection(t *testing.T) {
	keepAliveRead := make(chan struct{})
	payload := `{"id":"resp_1","status":"completed"}`
	engine := gin.New()
	engine.POST("/", func(c *gin.Context) {
		c.Header("X-Oneapi-Request-Id", "request-1")
		w := newKeepAliveWriter(c.Request.Context(), c.Writer, false, 10*time.Millisecond)
		c.Writer = w
		defer w.Finalize()

		controller := http.NewResponseController(c.Writer)
		assert.NoError(t, controller.SetWriteDeadline(time.Now().Add(time.Minute)))
		assert.NoError(t, controller.SetReadDeadline(time.Now().Add(time.Minute)), "Unwrap must reach the connection")

		c.Header("X-Upstream", "attempt-1")
		c.Header("Content-Length", strconv.Itoa(len(payload)))
		c.Status(http.StatusOK)
		select {
		case <-keepAliveRead:
		case <-c.Request.Context().Done():
			return
		}
		_, err := c.Writer.Write([]byte(payload))
		assert.NoError(t, err)
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	response, err := (&http.Client{Timeout: 10 * time.Second}).Post(server.URL, "application/json", nil)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	assert.Equal(t, "request-1", response.Header.Get("X-Oneapi-Request-Id"))
	assert.Empty(t, response.Header.Get("X-Upstream"))
	assert.EqualValues(t, -1, response.ContentLength)

	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadByte()
	require.NoError(t, err)
	assert.Equal(t, byte('\n'), first)
	close(keepAliveRead)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(append([]byte{first}, rest...), &decoded), "whitespace plus the body must stay one JSON document")
	assert.Equal(t, "completed", decoded["status"])
}

func TestInstallKeepAliveFollowsClientFormat(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.PingIntervalSeconds = 1

	for _, tc := range []struct {
		name      string
		enabled   bool
		nonStream bool
		format    types.RelayFormat
		mode      int
		stream    bool
		want      string
	}{
		{name: "disabled", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions, stream: true},
		{name: "chat stream", enabled: true, format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions, stream: true, want: "sse"},
		{name: "claude stream", enabled: true, format: types.RelayFormatClaude, stream: true, want: "sse"},
		{name: "responses stream", enabled: true, format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses, stream: true, want: "sse"},
		{name: "native gemini stream", enabled: true, nonStream: true, format: types.RelayFormatGemini, mode: relayconstant.RelayModeGemini, stream: true},
		{name: "speech stream", enabled: true, format: types.RelayFormatOpenAIAudio, mode: relayconstant.RelayModeAudioSpeech, stream: true},
		{name: "non-stream needs its own switch", enabled: true, format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions},
		{name: "chat json", enabled: true, nonStream: true, format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions, want: "json"},
		{name: "native gemini json", enabled: true, nonStream: true, format: types.RelayFormatGemini, mode: relayconstant.RelayModeGemini},
		{name: "rerank json", enabled: true, nonStream: true, format: types.RelayFormatRerank, mode: relayconstant.RelayModeRerank, want: "json"},
		{name: "transcription", enabled: true, nonStream: true, format: types.RelayFormatOpenAIAudio, mode: relayconstant.RelayModeAudioTranscription},
		{name: "alpha search", enabled: true, nonStream: true, format: types.RelayFormatOpenAIAlphaSearch, mode: relayconstant.RelayModeAlphaSearch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings.PingIntervalEnabled = tc.enabled
			settings.NonStreamPingEnabled = tc.nonStream
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			original := c.Writer

			info := &relaycommon.RelayInfo{RelayMode: tc.mode, IsStream: tc.stream}
			w := InstallKeepAlive(c, tc.format, info)
			// Native Gemini streams must not get the old pingers either,
			// whichever channel type serves them.
			assert.Equal(t, tc.format == types.RelayFormatGemini, info.DisablePing)
			if tc.want == "" {
				assert.Nil(t, w)
				assert.Same(t, original, c.Writer)
				return
			}
			require.NotNil(t, w)
			w.Stop()
			assert.Same(t, w, c.Writer)
			assert.True(t, KeepAliveActive(c))
			assert.Equal(t, tc.want == "sse", KeepAliveStreaming(c))
		})
	}
}

func TestInstallJSONKeepAliveNeedsBothSwitches(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.PingIntervalSeconds = 1

	for _, tc := range []struct {
		enabled, nonStream, want bool
	}{
		{enabled: false, nonStream: true},
		{enabled: true, nonStream: false},
		{enabled: true, nonStream: true, want: true},
	} {
		settings.PingIntervalEnabled = tc.enabled
		settings.NonStreamPingEnabled = tc.nonStream
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

		w := InstallJSONKeepAlive(c)
		if !tc.want {
			assert.Nil(t, w)
			assert.False(t, KeepAliveActive(c))
			continue
		}
		require.NotNil(t, w)
		w.Stop()
		assert.True(t, KeepAliveActive(c))
		assert.False(t, KeepAliveStreaming(c))
	}
}
