package novelai

import (
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
// channel. The slot is released once, by the first of: release, the
// response body being closed (generationBody), or the end of the client's
// request.
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

// generationBody releases the generation slot when the response body is
// closed: NovelAI generates while the body arrives.
type generationBody struct {
	io.ReadCloser
	release func()
}

func (b *generationBody) Close() error {
	err := b.ReadCloser.Close()
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
