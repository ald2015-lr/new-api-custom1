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
	"cmp"
	"context"
	"fmt"
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

const (
	// An SSE comment line, skipped by every SSE parser.
	sseKeepAlive = ": PING\n\n"
	// Leading whitespace, skipped by every JSON parser.
	jsonKeepAlive = "\n"
)

var sseEventEnd = [2]byte{'\n', '\n'}

// KeepAliveWriter keeps a client that waits on a slow upstream from timing
// out, the way OpenRouter does: after every interval without output it sends
// an SSE comment to a stream, or whitespace ahead of a JSON body.
//
// Keepalive bytes are not the response. Written stays false until the handler
// writes, so the relay may still retry another channel and the next attempt's
// body simply follows the keepalives. Status reports what the handler asked
// for even when a keepalive already committed 200, so the middleware still
// counts a failed request as failed.
//
// Handlers and the keepalive goroutine share the gin writer and its real
// header map, so both are only touched under mu. Handlers mutate the map
// returned by Header without any lock, so the keepalive goroutine never reads
// it and commits a header set fixed at install time instead.
type KeepAliveWriter struct {
	under           gin.ResponseWriter
	sse             bool
	interval        time.Duration
	shadow          http.Header
	keepAliveHeader http.Header

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu                 sync.Mutex
	status             int
	wireCommitted      bool
	keepAliveCommitted bool
	sseWire            bool
	payload            bool
	size               int
	tail               [2]byte
	lastWrite          time.Time
	writeDeadline      time.Time
}

// InstallKeepAlive puts a KeepAliveWriter in front of c.Writer when keepalive
// is enabled and the client's response format can carry it, and returns nil
// otherwise. The mode is fixed here from what the client asked for: handlers
// later flip info.IsStream when an upstream answers with SSE, but the client
// still parses what it requested.
func InstallKeepAlive(c *gin.Context, relayFormat types.RelayFormat, info *relaycommon.RelayInfo) *KeepAliveWriter {
	if relayFormat == types.RelayFormatGemini {
		// python-genai json-decodes every SSE line that is not "data:", so a
		// comment line breaks it. The Gemini adaptor already turns the old
		// pingers off, but a Gemini-format response served by another channel
		// type, or turned into a stream by the upstream, would still get them.
		info.DisablePing = true
	}
	settings := operation_setting.GetGeneralSetting()
	if !settings.PingIntervalEnabled {
		return nil
	}
	switch info.RelayMode {
	case relayconstant.RelayModeAudioSpeech, relayconstant.RelayModeAudioTranscription, relayconstant.RelayModeAudioTranslation, relayconstant.RelayModeAlphaSearch:
		// Binary audio, srt/vtt transcripts and unparsed passthrough bodies
		// cannot take extra bytes, whatever route reached here.
		return nil
	}
	allowed := false
	if info.IsStream {
		switch relayFormat {
		case types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIImage:
			allowed = true
		}
	} else if settings.NonStreamPingEnabled {
		// Native Gemini is left out here too: after a keepalive an error can
		// only go out as 200 plus an error body, which google-genai returns as
		// an empty response without raising, a silent failure.
		switch relayFormat {
		case types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatOpenAIResponses,
			types.RelayFormatOpenAIResponsesCompaction, types.RelayFormatOpenAIImage, types.RelayFormatEmbedding, types.RelayFormatRerank:
			allowed = true
		}
	}
	if !allowed {
		return nil
	}
	return installKeepAlive(c, info.IsStream, settings.PingIntervalSeconds)
}

// InstallJSONKeepAlive keeps a non-stream request alive with whitespace ahead
// of its JSON body, for routes outside the relay, such as task plugin
// protocols, that wait silently for a task to settle. It returns nil unless
// non-stream keepalive is enabled.
func InstallJSONKeepAlive(c *gin.Context) *KeepAliveWriter {
	settings := operation_setting.GetGeneralSetting()
	if !settings.PingIntervalEnabled || !settings.NonStreamPingEnabled {
		return nil
	}
	return installKeepAlive(c, false, settings.PingIntervalSeconds)
}

func installKeepAlive(c *gin.Context, sse bool, seconds int) *KeepAliveWriter {
	interval := time.Duration(seconds) * time.Second
	if interval <= 0 {
		interval = DefaultPingInterval
	}
	// A keepalive commit makes net/http read its *http.Request (Body,
	// ContentLength) on the keepalive goroutine, while each retry attempt
	// replaces c.Request.Body. Handlers get their own copy so the two never
	// share that struct, as they already do behind the RequestId middleware.
	c.Request = c.Request.WithContext(c.Request.Context())
	w := newKeepAliveWriter(c.Request.Context(), c.Writer, sse, interval)
	c.Writer = w
	return w
}

func newKeepAliveWriter(ctx context.Context, under gin.ResponseWriter, sse bool, interval time.Duration) *KeepAliveWriter {
	// Middleware headers such as the request id and CORS are already set and
	// must survive a keepalive commit; anything describing a body must not.
	keepAliveHeader := under.Header().Clone()
	keepAliveHeader.Del("Content-Length")
	keepAliveHeader.Del("Content-Encoding")
	keepAliveHeader.Set("Content-Type", "application/json")
	if sse {
		keepAliveHeader.Set("Content-Type", "text/event-stream")
	}
	keepAliveHeader.Set("Cache-Control", "no-cache")
	// nginx-based proxies buffer responses unless told not to, which would
	// hold the keepalive bytes back from the client.
	keepAliveHeader.Set("X-Accel-Buffering", "no")
	w := &KeepAliveWriter{
		under:           under,
		sse:             sse,
		interval:        interval,
		shadow:          under.Header().Clone(),
		keepAliveHeader: keepAliveHeader,
		stop:            make(chan struct{}),
		lastWrite:       time.Now(),
	}
	w.wg.Go(func() { w.keepAlive(ctx) })
	return w
}

// KeepAliveActive reports whether a KeepAliveWriter guards c's response. It
// then owns keepalive for the request; any other ping would count as
// response bytes and stop retries.
func KeepAliveActive(c *gin.Context) bool {
	_, ok := c.Writer.(*KeepAliveWriter)
	return ok
}

// KeepAliveStreaming reports whether a KeepAliveWriter keeps an SSE stream
// alive for c. A JSON-mode writer stops at the first byte of the body, so a
// stream the upstream forced on a non-stream request still needs the stream
// scanner's own pings.
func KeepAliveStreaming(c *gin.Context) bool {
	w, ok := c.Writer.(*KeepAliveWriter)
	return ok && w.sse
}

// KeepAliveCommitted reports whether a keepalive put the status line on the
// wire before the handler wrote anything, and whether it committed an SSE
// stream rather than a JSON body.
func KeepAliveCommitted(c *gin.Context) (committed bool, sse bool) {
	w, ok := c.Writer.(*KeepAliveWriter)
	if !ok {
		return false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.keepAliveCommitted, w.sse
}

// Stop ends keepalive and waits for its goroutine, which must not outlive the
// handler: gin recycles the context and net/http the connection buffers.
// The writer keeps working afterwards.
func (w *KeepAliveWriter) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	w.wg.Wait()
}

// Finalize stops keepalive and, when nothing reached the wire, hands the
// handler's pending headers and status to the gin writer: gin finishes the
// request on that writer, not on c.Writer.
func (w *KeepAliveWriter) Finalize() {
	w.Stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.wireCommitted {
		w.handOverLocked()
	}
}

func (w *KeepAliveWriter) keepAlive(ctx context.Context) {
	defer func() {
		// A failing write ends keepalive, never the process.
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf("relay keepalive panic: %v", r))
		}
	}()
	timer := time.NewTimer(w.interval)
	defer timer.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		next, ok := w.tick()
		if !ok {
			return
		}
		timer.Reset(next)
	}
}

// tick sends a keepalive when the client has seen nothing for a whole
// interval. It returns when to check again, or false once keepalive is over.
func (w *KeepAliveWriter) tick() (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Once the handler's response started, only an SSE stream can still take
	// keepalive bytes; a JSON body cannot take them anywhere but in front.
	if w.payload && (!w.sse || !w.sseWire) {
		return 0, false
	}
	idle := time.Since(w.lastWrite)
	if idle < w.interval {
		return w.interval - idle, true
	}
	// Between an "event:" line and its "data:" line, a comment would dispatch
	// an empty event and orphan the data.
	if w.size > 0 && w.tail != sseEventEnd {
		return w.interval, true
	}
	if !w.wireCommitted {
		w.replaceHeaderLocked(w.keepAliveHeader)
		w.under.WriteHeader(http.StatusOK)
		w.wireCommitted, w.keepAliveCommitted, w.sseWire = true, true, w.sse
	}
	frame := jsonKeepAlive
	if w.sse {
		frame = sseKeepAlive
	}
	// Bound the write so a stalled client cannot block Stop, then restore the
	// handler's own deadline: a leftover keepalive deadline would cut off a
	// later large body, a cleared one would unbound the next stream write.
	controller := http.NewResponseController(w.under)
	_ = controller.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	_, err := w.under.WriteString(frame)
	if err == nil {
		w.under.Flush()
	}
	_ = controller.SetWriteDeadline(w.writeDeadline)
	if err != nil {
		return 0, false
	}
	w.lastWrite = time.Now()
	return w.interval, true
}

// commitLocked starts the handler's response. Committing the status line
// starts it even without bytes, as in gin; after a keepalive commit only real
// bytes do, so that retries stay possible.
func (w *KeepAliveWriter) commitLocked(hasBytes bool) {
	if w.wireCommitted {
		w.payload = w.payload || hasBytes
		return
	}
	w.handOverLocked()
	w.under.WriteHeaderNow()
	w.wireCommitted, w.payload = true, true
	w.sseWire = strings.HasPrefix(w.shadow.Get("Content-Type"), "text/event-stream")
}

// handOverLocked gives the gin writer the handler's headers and status. It
// sends them on its first write.
func (w *KeepAliveWriter) handOverLocked() {
	w.replaceHeaderLocked(w.shadow)
	if w.status > 0 {
		w.under.WriteHeader(w.status)
	}
}

func (w *KeepAliveWriter) replaceHeaderLocked(header http.Header) {
	wire := w.under.Header()
	clear(wire)
	maps.Copy(wire, header.Clone())
}

func (w *KeepAliveWriter) trackLocked(n int, tail string) {
	if n == 0 {
		return
	}
	w.size += n
	for i := range len(tail) {
		w.tail = [2]byte{w.tail[1], tail[i]}
	}
	w.lastWrite = time.Now()
}

func (w *KeepAliveWriter) Header() http.Header {
	return w.shadow
}

func (w *KeepAliveWriter) WriteHeader(code int) {
	if code <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.payload {
		w.status = code
	}
}

func (w *KeepAliveWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commitLocked(false)
}

func (w *KeepAliveWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commitLocked(len(data) > 0)
	n, err := w.under.Write(data)
	w.trackLocked(n, string(data[max(0, n-2):n]))
	return n, err
}

func (w *KeepAliveWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commitLocked(len(s) > 0)
	n, err := w.under.WriteString(s)
	w.trackLocked(n, s[max(0, n-2):n])
	return n, err
}

func (w *KeepAliveWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commitLocked(false)
	w.under.Flush()
}

func (w *KeepAliveWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return cmp.Or(w.status, http.StatusOK)
}

func (w *KeepAliveWriter) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.payload {
		return -1
	}
	return w.size
}

func (w *KeepAliveWriter) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.payload
}

// SetWriteDeadline is what http.ResponseController reaches first. The
// deadline is remembered so a keepalive can restore it after bounding its own
// write.
func (w *KeepAliveWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeDeadline = deadline
	return http.NewResponseController(w.under).SetWriteDeadline(deadline)
}

func (w *KeepAliveWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.Stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wireCommitted, w.payload = true, true
	return w.under.Hijack()
}

func (w *KeepAliveWriter) CloseNotify() <-chan bool {
	return w.under.CloseNotify()
}

func (w *KeepAliveWriter) Pusher() http.Pusher {
	return w.under.Pusher()
}

// Unwrap lets http.ResponseController reach the connection for everything
// this writer does not implement itself.
func (w *KeepAliveWriter) Unwrap() http.ResponseWriter {
	return w.under
}
