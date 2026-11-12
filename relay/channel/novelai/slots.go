package novelai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// NovelAI generates one image request at a time per account and answers a
// second one with 429 "Concurrent generation is locked". A NovelAI channel on
// NovelAI's own host therefore sends one generation per key at a time: the
// next waits up to generationWait for the slot, or as long as its client
// stays, and then fails with a 429 another channel may take. A
// NovelAI-compatible forwarder (any other host) queues for itself.
var (
	generationWait    = 120 * time.Second
	generationSlotsMu sync.Mutex
	generationSlots   = map[string]chan struct{}{}
)

// isNovelAIImageHost tells whether a NovelAI channel's base URL is NovelAI's
// image host; an empty base URL means that host.
func isNovelAIImageHost(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(constant.ChannelTypeNovelAI)
	}
	parsed, err := url.Parse(baseURL)
	return err == nil && strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), imageHost)
}

func generationSlot(key string) chan struct{} {
	generationSlotsMu.Lock()
	defer generationSlotsMu.Unlock()
	slot, ok := generationSlots[key]
	if !ok {
		slot = make(chan struct{}, 1)
		generationSlots[key] = slot
	}
	return slot
}

// holdGeneration takes the generation slot of the channel key for a request
// to NovelAI's image host and returns its release, a no-op for any other
// channel. The slot is released once, by the first of: release, the end of
// NovelAI's answer or the close of the response body (generationBody), or
// the end of the client's request.
func holdGeneration(c *gin.Context, info *relaycommon.RelayInfo) (func(), *types.NewAPIError) {
	if info.ChannelType != constant.ChannelTypeNovelAI || !isNovelAIImageHost(info.ChannelBaseUrl) {
		return func() {}, nil
	}
	ctx := c.Request.Context()
	slot := generationSlot(info.ApiKey)
	timer := time.NewTimer(generationWait)
	defer timer.Stop()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, types.NewOpenAIError(ctx.Err(), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	case <-timer.C:
		return nil, types.NewOpenAIError(errors.New(i18n.T(c, i18n.MsgNovelAIAccountBusy)), types.ErrorCodeDoRequestFailed, http.StatusTooManyRequests)
	}
	var once sync.Once
	release := func() { once.Do(func() { <-slot }) }
	context.AfterFunc(ctx, release)
	return release, nil
}

// generationReadAhead bounds how much of NovelAI's answer generationBody
// keeps for a relay that has not taken it yet: as much as the relay keeps of
// a response it converts (responseLimit, and the native response limit).
// Tests lower it.
var generationReadAhead = 64 << 20

// errGenerationBodyClosed fails a read of a closed response body.
var errGenerationBodyClosed = errors.New("http: read on closed response body")

// generationBody reads NovelAI's answer ahead of the relay and releases the
// generation slot when the answer ends (its last byte arrived or the read
// failed) or the body is closed: NovelAI generates while the body arrives,
// and is free once all of it arrived, however long the relay then takes to
// hand the images to its client, which may read slowly or not at all. Up to
// generationReadAhead bytes wait for the relay; past that the read-ahead
// waits for the relay to catch up.
type generationBody struct {
	upstream io.ReadCloser
	release  func()
	// readAheadLimit is generationReadAhead when the body was made.
	readAheadLimit int

	mu      sync.Mutex
	changed *sync.Cond
	ahead   bytes.Buffer
	// ended is how upstream's answer ended: io.EOF, or the read error.
	ended  error
	closed bool
}

// newGenerationBody starts reading upstream ahead of the relay.
func newGenerationBody(upstream io.ReadCloser, release func()) *generationBody {
	body := &generationBody{upstream: upstream, release: release, readAheadLimit: generationReadAhead}
	body.changed = sync.NewCond(&body.mu)
	go body.readAhead()
	return body
}

func (b *generationBody) readAhead() {
	chunk := make([]byte, 32<<10)
	for {
		b.mu.Lock()
		for b.ahead.Len() >= b.readAheadLimit && !b.closed {
			b.changed.Wait()
		}
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return
		}
		n, err := b.upstream.Read(chunk)
		b.mu.Lock()
		if !b.closed {
			b.ahead.Write(chunk[:n])
		}
		if err != nil {
			b.ended = err
		}
		b.changed.Broadcast()
		b.mu.Unlock()
		if err != nil {
			b.release()
			return
		}
	}
}

func (b *generationBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.ahead.Len() == 0 && b.ended == nil && !b.closed {
		b.changed.Wait()
	}
	switch {
	case b.closed:
		return 0, errGenerationBodyClosed
	case b.ahead.Len() > 0:
		n, _ := b.ahead.Read(p)
		b.changed.Broadcast()
		return n, nil
	}
	return 0, b.ended
}

// Close ends the read-ahead, which also ends a read waiting on it.
func (b *generationBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.ahead = bytes.Buffer{}
	b.changed.Broadcast()
	b.mu.Unlock()
	err := b.upstream.Close()
	b.release()
	return err
}

// RequestMayHaveReachedUpstream tells whether a failed send may have reached
// NovelAI, which may then have spent Anlas: anything but a failure raised
// before the round trip (the transport reports its own failures as a
// *url.Error; a URL that does not parse is one too, with Op "parse") or a
// connection that could not be made (a dial failure anywhere in the chain,
// which a SOCKS proxy wraps in its own "socks connect" error, or a failed
// DNS lookup). Such a request is never sent a second time.
func RequestMayHaveReachedUpstream(err error) bool {
	var roundTripErr *url.Error
	var dialErr *net.OpError
	var dnsErr *net.DNSError
	attempted := errors.As(err, &roundTripErr) && roundTripErr.Op != "parse"
	notConnected := errors.As(err, &dnsErr)
	for chain := err; !notConnected && errors.As(chain, &dialErr); chain = dialErr.Err {
		notConnected = dialErr.Op == "dial" || dialErr.Op == "proxyconnect"
	}
	return attempted && !notConnected
}
