package novelai

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// NovelAI generates one image request at a time per account and answers a
// second one with 429 "Concurrent generation is locked". A NovelAI channel on
// NovelAI's own host therefore sends one generation per key at a time, and
// the requests that find their key busy wait in line for it, first come
// first served, up to the channel's queue limit per key
// (ChannelOtherSettings.EffectiveNovelAIQueueLimit). A multi-key channel
// sends each request to its least busy enabled key. Once every key has a
// full line, a request fails at once with a 429 another channel may take
// (ErrorCodeNovelAIKeyBusy, never a reason to disable the channel). A
// NovelAI-compatible forwarder (any other host) queues for itself.

// generationKey is a key a generation can use: its index among the channel's
// keys (0 on a single-key channel) and the key, which goes upstream and is
// never logged.
type generationKey struct {
	index int
	key   string
}

// generationLine is a held NovelAI key's line: the requests waiting for the
// key, in arrival order. A line exists exactly while its key is held.
type generationLine struct {
	waiting list.List // of *generationWaiter
}

type waiterOutcome int

const (
	waiterWaiting waiterOutcome = iota
	waiterGranted               // the key was handed to it
	waiterStopped               // the queue stopped for shutdown
)

// generationWaiter is a request in a line. outcome is set under
// generationLinesMu, and turn closes right after; a read of outcome after
// turn closed needs no lock.
type generationWaiter struct {
	turn    chan struct{}
	outcome waiterOutcome
}

var (
	generationLinesMu sync.Mutex
	// generationLines holds the line of every held key by the SHA-256 of the
	// key: NovelAI locks generation per account, so channels sharing a key
	// share its line.
	generationLines = map[[sha256.Size]byte]*generationLine{}
	// generationRefused holds, by key hash, until when NovelAI's refusal of a
	// key (401, 402, 403 or 429) puts it behind the keys it did not refuse.
	generationRefused = map[[sha256.Size]byte]time.Time{}
	// generationStopped refuses new arrivals once the server shuts down.
	generationStopped bool
	// generationMaxWait bounds a wait in line, generationCooldown a refusal
	// mark. Tests change both.
	generationMaxWait  = 30 * time.Minute
	generationCooldown = 60 * time.Second
)

var (
	errGenerationQueueFull    = errors.New("every NovelAI key of the channel has a full queue")
	errGenerationWaitExpired  = errors.New("the NovelAI key stayed busy past the longest wait")
	errGenerationQueueStopped = errors.New("the NovelAI queues stopped for shutdown")
)

// generationTicket is a request's place for one key: the key itself when it
// was free, or a place in its line.
type generationTicket struct {
	key     generationKey
	id      [sha256.Size]byte
	waiter  *generationWaiter // nil: the key was free and is now held
	element *list.Element
	// position is the 1-based place in line, 0 when the key was free.
	position int
}

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

// enterGenerationLine chooses one of keys for a request and takes it, or a
// place at the back of its line, without waiting. Only keys with room count:
// a free key, or one with fewer than limit requests waiting; none with room
// is errGenerationQueueFull. Among them the keys NovelAI has not just refused
// come first, then the least loaded (holding and waiting), then the
// preferred position (the distributor's polling or random pick, -1 for
// none), then the next tie after it under polling, a random tie under
// random, and the first tie otherwise. Choosing and taking happen under one
// lock, so concurrent arrivals see each other's choice.
func enterGenerationLine(keys []generationKey, preferred int, mode constant.MultiKeyMode, limit int) (*generationTicket, error) {
	ids := make([][sha256.Size]byte, len(keys))
	for i, key := range keys {
		ids[i] = sha256.Sum256([]byte(key.key))
	}
	now := time.Now()
	generationLinesMu.Lock()
	defer generationLinesMu.Unlock()
	if generationStopped {
		return nil, errGenerationQueueStopped
	}
	load := func(i int) int {
		line, held := generationLines[ids[i]]
		if !held {
			return 0
		}
		return 1 + line.waiting.Len()
	}
	var open, pool []int
	for i := range keys {
		if line, held := generationLines[ids[i]]; !held || line.waiting.Len() < limit {
			open = append(open, i)
		}
	}
	if len(open) == 0 {
		return nil, errGenerationQueueFull
	}
	for _, i := range open {
		if until, refused := generationRefused[ids[i]]; !refused || !until.After(now) {
			pool = append(pool, i)
		}
	}
	if len(pool) == 0 {
		pool = open
	}
	least := load(pool[0])
	for _, i := range pool[1:] {
		least = min(least, load(i))
	}
	var ties []int
	for _, i := range pool {
		if load(i) == least {
			ties = append(ties, i)
		}
	}
	chosen := ties[0]
	switch {
	case preferred >= 0 && slices.Contains(ties, preferred):
		chosen = preferred
	case mode == constant.MultiKeyModePolling:
		// The first tie after the preferred position, or from the first key.
		start := max(preferred+1, 0)
		for step := range len(keys) {
			if i := (start + step) % len(keys); slices.Contains(ties, i) {
				chosen = i
				break
			}
		}
	case mode == constant.MultiKeyModeRandom:
		chosen = ties[rand.IntN(len(ties))]
	}
	ticket := &generationTicket{key: keys[chosen], id: ids[chosen]}
	line, held := generationLines[ticket.id]
	if !held {
		generationLines[ticket.id] = &generationLine{}
		return ticket, nil
	}
	ticket.waiter = &generationWaiter{turn: make(chan struct{})}
	ticket.element = line.waiting.PushBack(ticket.waiter)
	ticket.position = line.waiting.Len()
	return ticket, nil
}

// wait waits for the ticket's turn, at most generationMaxWait and as long as
// ctx lasts, and returns the release of the key it then holds. A request
// that stops waiting leaves the line; one whose turn came together with its
// cancellation passes the key on.
func (t *generationTicket) wait(ctx context.Context) (func(), error) {
	release := sync.OnceFunc(func() { releaseGenerationKey(t.id) })
	if t.waiter == nil {
		return release, nil
	}
	timer := time.NewTimer(generationMaxWait)
	defer timer.Stop()
	var err error
	select {
	case <-t.waiter.turn:
		if t.waiter.outcome == waiterStopped {
			return nil, errGenerationQueueStopped
		}
		return release, nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errGenerationWaitExpired
	}
	generationLinesMu.Lock()
	outcome := t.waiter.outcome
	if outcome == waiterWaiting {
		// A line with waiters is held, so it exists.
		generationLines[t.id].waiting.Remove(t.element)
	}
	generationLinesMu.Unlock()
	if outcome == waiterGranted {
		release()
	}
	return nil, err
}

// releaseGenerationKey hands a held key to the head of its line, which keeps
// the key held, or frees it when nobody waits.
func releaseGenerationKey(id [sha256.Size]byte) {
	generationLinesMu.Lock()
	defer generationLinesMu.Unlock()
	line := generationLines[id]
	if line == nil {
		return
	}
	head := line.waiting.Front()
	if head == nil {
		delete(generationLines, id)
		return
	}
	waiter := line.waiting.Remove(head).(*generationWaiter)
	waiter.outcome = waiterGranted
	close(waiter.turn)
}

// holdGeneration takes a key of the channel for a request to NovelAI's image
// host, waiting for its turn when the key is busy, and returns its release;
// it is a no-op for any other channel. On a multi-key channel it chooses the
// key (enterGenerationLine) and makes it the attempt's key: the upstream
// call, the automatic disable of a failing key and the logs' key index use
// it. The key is released once, by the first of: release, the end of
// NovelAI's answer or the close of the response body (generationBody), or
// the end of the client's request. A request with HD holds the key past the
// base answer, through its enhance calls, as one job (Adaptor.DoRequest).
func holdGeneration(c *gin.Context, info *relaycommon.RelayInfo) (func(), *types.NewAPIError) {
	if info.ChannelType != constant.ChannelTypeNovelAI || !isNovelAIImageHost(info.ChannelBaseUrl) {
		return func() {}, nil
	}
	limit := info.ChannelOtherSettings.EffectiveNovelAIQueueLimit()
	// A single-key channel's key is index 0: the context's index may be left
	// over from a multi-key channel an earlier attempt used.
	keys := []generationKey{{index: 0, key: info.ApiKey}}
	preferred, mode := 0, constant.MultiKeyMode("")
	if info.ChannelIsMultiKey {
		keys = []generationKey{{index: info.ChannelMultiKeyIndex, key: info.ApiKey}}
		if channel, err := model.CacheGetChannel(info.ChannelId); err == nil && channel.ChannelInfo.IsMultiKey {
			all, enabled := channel.EnabledMultiKeys()
			if len(enabled) > 0 {
				candidates, pick := make([]generationKey, 0, len(enabled)), -1
				for _, index := range enabled {
					if index == info.ChannelMultiKeyIndex && all[index] == info.ApiKey {
						pick = len(candidates)
					}
					candidates = append(candidates, generationKey{index: index, key: all[index]})
				}
				keys, preferred, mode = candidates, pick, channel.ChannelInfo.MultiKeyMode
			}
		}
	}
	ticket, err := enterGenerationLine(keys, preferred, mode, limit)
	switch {
	case errors.Is(err, errGenerationQueueStopped):
		return nil, novelAIQueueStoppedError(c)
	case err != nil:
		logger.LogWarn(c, common.LogText("NovelAI channel #%d: every key is generating with %d requests waiting (the channel's limit), rejected with 429", info.ChannelId, limit))
		return nil, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgNovelAIQueueFull)), types.ErrorCodeNovelAIKeyBusy, http.StatusTooManyRequests)
	}
	// From here until wait returns nothing may return: the ticket holds the
	// key or a place in its line.
	info.ApiKey, info.ChannelMultiKeyIndex = ticket.key.key, ticket.key.index
	if info.ChannelIsMultiKey {
		common.SetContextKey(c, constant.ContextKeyChannelKey, ticket.key.key)
		common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, ticket.key.index)
	}
	ctx, queuedAt := c.Request.Context(), time.Now()
	release, err := ticket.wait(ctx)
	switch {
	case errors.Is(err, errGenerationQueueStopped):
		return nil, novelAIQueueStoppedError(c)
	case errors.Is(err, errGenerationWaitExpired):
		logger.LogWarn(c, common.LogText("NovelAI channel #%d key #%d: no turn after waiting %s, rejected with 429", info.ChannelId, ticket.key.index, generationMaxWait))
		return nil, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgNovelAIQueueTimeout, map[string]any{"Minutes": int(generationMaxWait.Minutes())})), types.ErrorCodeNovelAIKeyBusy, http.StatusTooManyRequests)
	case err != nil: // the client left
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	if ticket.position > 0 {
		logger.LogInfo(c, common.LogText("NovelAI channel #%d key #%d: waited %s at position %d in its queue", info.ChannelId, ticket.key.index, time.Since(queuedAt).Round(time.Millisecond), ticket.position))
	}
	context.AfterFunc(ctx, release)
	return release, nil
}

// novelAIQueueStoppedError answers a request the NovelAI queues stopped for
// shutdown: it was never sent, and the server that would retry it is
// stopping.
func novelAIQueueStoppedError(c *gin.Context) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgNovelAIQueueStopped)), types.ErrorCodeNovelAIKeyBusy, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

// isGenerationRefusal tells NovelAI's answers that refuse the account rather
// than the request: a rejected token, no Anlas, a forbidden account, and its
// own concurrency lock.
func isGenerationRefusal(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}

// noteGenerationAnswer records NovelAI's answer for the attempt's key: a
// refusal puts the key behind the keys it did not refuse for
// generationCooldown, a success clears that.
func noteGenerationAnswer(c *gin.Context, info *relaycommon.RelayInfo, status int) {
	refused, succeeded := isGenerationRefusal(status), status >= 200 && status < 300
	if !refused && !succeeded {
		return
	}
	id, now := sha256.Sum256([]byte(info.ApiKey)), time.Now()
	generationLinesMu.Lock()
	if succeeded {
		delete(generationRefused, id)
	} else {
		for other, until := range generationRefused {
			if !now.Before(until) {
				delete(generationRefused, other)
			}
		}
		generationRefused[id] = now.Add(generationCooldown)
	}
	generationLinesMu.Unlock()
	if refused && info.ChannelIsMultiKey {
		logger.LogWarn(c, common.LogText("NovelAI channel #%d key #%d answered %d, so new requests go to its other keys with room for %s", info.ChannelId, info.ChannelMultiKeyIndex, status, generationCooldown))
	}
}

// StopGenerationQueues answers every request waiting for a NovelAI key, and
// every later one, with a 503 that is never sent upstream, so nothing new
// starts generating while the server shuts down. Requests already
// generating keep their keys and finish.
func StopGenerationQueues() {
	generationLinesMu.Lock()
	generationStopped = true
	stopped := 0
	for _, line := range generationLines {
		for e := line.waiting.Front(); e != nil; e = e.Next() {
			waiter := e.Value.(*generationWaiter)
			waiter.outcome = waiterStopped
			close(waiter.turn)
			stopped++
		}
		line.waiting.Init()
	}
	generationLinesMu.Unlock()
	if stopped > 0 {
		common.SysLog(common.LogText("NovelAI queues stopped for shutdown: %d waiting requests were not sent", stopped))
	}
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
//
// A deliberate trade-off: a client that stops reading keeps the rest of its
// answer in memory (at most generationReadAhead) until the native relay's
// write budget (helper.StreamWriteBudget) gives up on it, about 35 minutes
// for a 16 MB answer, while the key it freed generates again, so one user
// may hold several such answers, each one paid for. The converted routes
// hold a whole answer the same way, with no write deadline at all.
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
