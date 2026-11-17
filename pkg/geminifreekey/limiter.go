package geminifreekey

import (
	"crypto/sha256"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	// The free tier's day is a day in Los Angeles; the binary carries the
	// zone so that it does not depend on the host's zoneinfo.
	_ "time/tzdata"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// Timezone is the zone of the free tier's day: requests per day restart at
// midnight there.
const Timezone = "America/Los_Angeles"

var pacific = loadPacific()

func loadPacific() *time.Location {
	location, err := time.LoadLocation(Timezone)
	if err != nil {
		// Unreachable with time/tzdata linked in.
		return time.FixedZone("PST", -8*60*60)
	}
	return location
}

// PacificDay is the free tier's day of t.
func PacificDay(t time.Time) string {
	return t.In(pacific).Format(time.DateOnly)
}

// NextReset is the next midnight in Los Angeles after t, when requests per
// day restart.
func NextReset(t time.Time) time.Time {
	local := t.In(pacific)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, pacific)
}

const (
	// window is the span of requests and input tokens per minute.
	window = time.Minute
	// keyErrorCooldown is how long a key Google rejected sits out.
	keyErrorCooldown = time.Hour
	// pruneInterval is how often idle state is dropped.
	pruneInterval = time.Minute
	// cursorIdle is how long an unused rotation cursor is kept.
	cursorIdle = 24 * time.Hour
)

// Reason is why a key, or a key for one model, pauses.
type Reason string

const (
	// ReasonMinute: a per-minute limit of the model, until Google's retry delay.
	ReasonMinute Reason = "minute"
	// ReasonDay: the model's daily limit, until the next Pacific midnight.
	ReasonDay Reason = "day"
	// ReasonNotFree: Google does not serve the model on the key's free tier
	// (limit 0), until the next Pacific midnight.
	ReasonNotFree Reason = "not_free"
	// ReasonUnknown: a 429 without quota details, for a minute.
	ReasonUnknown Reason = "unknown"
	// ReasonKeyError: Google rejected the key itself, for every model.
	ReasonKeyError Reason = "key_error"
)

// Key is a key of a channel: its index among the channel's keys (0 on a
// single-key channel) and the key, which is never logged or stored.
type Key struct {
	Index  int
	Secret string
}

// KeyID is how the limiter knows a key: the SHA-256 of the key, so that
// channels sharing a key share its counts.
type KeyID [sha256.Size]byte

// IDOf is the KeyID of a key.
func IDOf(secret string) KeyID {
	return sha256.Sum256([]byte(secret))
}

// KeyHint shows the last four characters of a key, or nothing of a key too
// short to hide the rest.
func KeyHint(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	if len(runes) <= 8 {
		return "…"
	}
	return "…" + string(runes[len(runes)-4:])
}

// Pool is what a request may use: the enabled keys of its channel, the
// bucket of its model, and the channel's rows, which name the buckets of the
// models Google reports in a 429.
type Pool struct {
	ChannelID int
	// Random picks among equally good keys at random, as a multi-key channel
	// in random mode does; otherwise they take turns.
	Random bool
	Keys   []Key
	Bucket Bucket
	Rows   []dto.GeminiFreeKeyModelLimit
}

type event struct {
	seq    uint64
	at     time.Time
	units  int
	tokens int
}

// usage is a key's use of one bucket.
type usage struct {
	// events are the reservations of the last minute, oldest first.
	events   []event
	day      string
	dayCount int
	until    time.Time
	reason   Reason
}

type keyPause struct {
	until  time.Time
	reason Reason
}

type usageKey struct {
	key    KeyID
	bucket string
}

type cursorKey struct {
	channel int
	bucket  string
}

type cursor struct {
	next int
	used time.Time
}

// Limiter holds the counts and pauses of every key it has seen. It is safe
// for concurrent use: choosing a key and reserving it happen under one lock,
// so concurrent requests see each other.
type Limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	intN    func(int) int
	usages  map[usageKey]*usage
	pauses  map[KeyID]keyPause
	cursors map[cursorKey]*cursor
	seq     uint64
	pruned  time.Time
	// active is set once free key mode is in use: a request in the mode used
	// the limiter, or a channel in the mode is stored (MarkActive).
	active atomic.Bool
}

// New is a limiter reading the time from now (time.Now when nil).
func New(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		now:     now,
		intN:    rand.IntN,
		usages:  map[usageKey]*usage{},
		pauses:  map[KeyID]keyPause{},
		cursors: map[cursorKey]*cursor{},
	}
}

var shared atomic.Pointer[Limiter]

func init() {
	shared.Store(New(nil))
}

// Default is the process's limiter, which relays and channel selection share.
func Default() *Limiter {
	return shared.Load()
}

// SetDefault replaces the process's limiter and returns the previous one; it
// is meant for tests.
func SetDefault(limiter *Limiter) *Limiter {
	return shared.Swap(limiter)
}

// roll drops the events older than a minute and the count of a past day.
func (u *usage) roll(now time.Time, day string) {
	cut := now.Add(-window)
	expired := 0
	for expired < len(u.events) && !u.events[expired].at.After(cut) {
		expired++
	}
	if expired > 0 {
		u.events = append(u.events[:0], u.events[expired:]...)
	}
	if u.day != day {
		u.day, u.dayCount = day, 0
	}
}

func (u *usage) minute() (requests, tokens int) {
	for _, e := range u.events {
		requests += e.units
		tokens += e.tokens
	}
	return requests, tokens
}

// Report counts the keys of a pool by what keeps them from a request, the
// first that applies in this order.
type Report struct {
	Keys int
	// Tried keys were already tried by the request.
	Tried int
	// KeyError keys were rejected by Google.
	KeyError int
	// Paused keys pause for the bucket, by Reason.
	PausedMinute, PausedDay, PausedNotFree, PausedUnknown int
	// DayFull keys reached the requests per day.
	DayFull int
	// RPMFull and TPMFull keys have no room left this minute.
	RPMFull, TPMFull int
	// Ready keys have room for the request.
	Ready int
}

// Usable is how many keys could serve the bucket today: not tried, not
// paused and below the requests per day, whether or not they have room this
// minute.
func (r Report) Usable() int {
	return r.Ready + r.RPMFull + r.TPMFull
}

type keyState int

const (
	stateReady keyState = iota
	stateRPMFull
	stateTPMFull
	stateDayFull
	statePaused
	stateKeyError
	stateTried
)

// candidate is a key with the counts that order the choice.
type candidate struct {
	key      Key
	id       KeyID
	requests int
	dayCount int
}

// survey classifies the pool's keys for a request of units requests and
// tokens input tokens, and returns the keys that have room for it and those
// that are usable. The caller holds l.mu.
func (l *Limiter) survey(pool Pool, tokens, units int, tried []KeyID, now time.Time) (Report, []candidate, []candidate) {
	day := PacificDay(now)
	limits := pool.Bucket.Limits
	report := Report{Keys: len(pool.Keys)}
	var ready, usable []candidate
	for _, key := range pool.Keys {
		id := IDOf(key.Secret)
		state, requests, dayCount := l.stateOf(id, pool.Bucket.ID, limits, tokens, units, tried, now, day, &report)
		c := candidate{key: key, id: id, requests: requests, dayCount: dayCount}
		switch state {
		case stateReady:
			report.Ready++
			ready = append(ready, c)
			usable = append(usable, c)
		case stateRPMFull:
			report.RPMFull++
			usable = append(usable, c)
		case stateTPMFull:
			report.TPMFull++
			usable = append(usable, c)
		case stateDayFull:
			report.DayFull++
		case stateKeyError:
			report.KeyError++
		case stateTried:
			report.Tried++
		}
	}
	return report, ready, usable
}

// stateOf is what keeps key id from a request, and its requests this minute
// and today; a pause is counted in report by its reason. The caller holds
// l.mu.
func (l *Limiter) stateOf(id KeyID, bucket string, limits Limits, tokens, units int, tried []KeyID, now time.Time, day string, report *Report) (keyState, int, int) {
	if slices.Contains(tried, id) {
		return stateTried, 0, 0
	}
	if pause, ok := l.pauses[id]; ok && now.Before(pause.until) {
		return stateKeyError, 0, 0
	}
	u := l.usages[usageKey{id, bucket}]
	if u == nil {
		u = &usage{}
	} else {
		u.roll(now, day)
	}
	requests, minuteTokens := u.minute()
	if now.Before(u.until) {
		switch u.reason {
		case ReasonMinute:
			report.PausedMinute++
		case ReasonDay:
			report.PausedDay++
		case ReasonNotFree:
			report.PausedNotFree++
		default:
			report.PausedUnknown++
		}
		return statePaused, requests, u.dayCount
	}
	switch {
	case limits.RPD >= 0 && (u.dayCount >= limits.RPD || u.dayCount+units > limits.RPD):
		return stateDayFull, requests, u.dayCount
	case limits.RPM >= 0 && requests+units > limits.RPM:
		return stateRPMFull, requests, u.dayCount
	case limits.TPM >= 0 && minuteTokens+tokens > limits.TPM:
		return stateTPMFull, requests, u.dayCount
	}
	return stateReady, requests, u.dayCount
}

// choose orders candidates by fewest requests this minute, then fewest
// requests today, and breaks a tie at random for a random pool, otherwise
// with the pool's rotation: the first tied key at or after the cursor, by
// key index. advance moves the cursor past the chosen key. The caller holds
// l.mu.
func (l *Limiter) choose(pool Pool, candidates []candidate, now time.Time, advance bool) candidate {
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.requests < best.requests || c.requests == best.requests && c.dayCount < best.dayCount {
			best = c
		}
	}
	var ties []candidate
	for _, c := range candidates {
		if c.requests == best.requests && c.dayCount == best.dayCount {
			ties = append(ties, c)
		}
	}
	if pool.Random {
		return ties[l.intN(len(ties))]
	}
	at := cursorKey{pool.ChannelID, pool.Bucket.ID}
	next := 0
	if c := l.cursors[at]; c != nil {
		next = c.next
	}
	chosen := ties[0]
	for _, c := range ties {
		if c.key.Index < chosen.key.Index {
			chosen = c
		}
	}
	for _, c := range ties {
		if c.key.Index >= next && (chosen.key.Index < next || c.key.Index < chosen.key.Index) {
			chosen = c
		}
	}
	if advance {
		l.cursors[at] = &cursor{next: chosen.key.Index + 1, used: now}
	}
	return chosen
}

// Inspect reports the pool's keys for a request of units requests and
// tokens input tokens, without reserving anything.
func (l *Limiter) Inspect(pool Pool, tokens, units int, tried []KeyID) Report {
	l.active.Store(true)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	report, _, _ := l.survey(pool, tokens, units, tried, now)
	return report
}

// CanServe tells whether the pool's model is on the free tier and a key
// could serve it today: one not paused, not rejected and below the requests
// per day, whether or not it has room this minute.
func (l *Limiter) CanServe(pool Pool) bool {
	return !pool.Bucket.NotOnFreeTier() && l.Inspect(pool, 0, 0, nil).Usable() > 0
}

// Pick is the key a request would use now among the usable ones (see
// Report.Usable), without reserving it or moving the rotation.
func (l *Limiter) Pick(pool Pool, tried []KeyID) (Key, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	_, _, usable := l.survey(pool, 0, 0, tried, now)
	if len(usable) == 0 {
		return Key{}, false
	}
	return l.choose(pool, usable, now, false).key, true
}

// Ticket is a reservation of a key for one request: it counts in the key's
// requests per minute and per day and its input tokens per minute until
// rolled back.
type Ticket struct {
	Key    Key
	l      *Limiter
	pool   Pool
	id     KeyID
	seq    uint64
	units  int
	day    string
	closed bool // guarded by l.mu
}

// Reserve chooses a key with room for units requests (1 unless a batch) and
// tokens input tokens among the pool's keys not in tried, and counts the
// request on it. It returns nil and the report of the keys when none has
// room.
func (l *Limiter) Reserve(pool Pool, tokens, units int, tried []KeyID) (*Ticket, Report) {
	l.active.Store(true)
	units = max(units, 1)
	tokens = max(tokens, 0)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	report, ready, _ := l.survey(pool, tokens, units, tried, now)
	if len(ready) == 0 {
		return nil, report
	}
	chosen := l.choose(pool, ready, now, true)
	u := l.usage(chosen.id, pool.Bucket.ID, now)
	l.seq++
	u.events = append(u.events, event{seq: l.seq, at: now, units: units, tokens: tokens})
	u.dayCount += units
	return &Ticket{Key: chosen.key, l: l, pool: pool, id: chosen.id, seq: l.seq, units: units, day: u.day}, report
}

// usage is the usage of key id for bucket, created when missing and rolled
// to now. The caller holds l.mu.
func (l *Limiter) usage(id KeyID, bucket string, now time.Time) *usage {
	at := usageKey{id, bucket}
	u := l.usages[at]
	if u == nil {
		u = &usage{}
		l.usages[at] = u
	}
	u.roll(now, PacificDay(now))
	return u
}

// Rollback takes back the reservation of a request that never reached
// Google. It does nothing after Rollback or Finish.
func (t *Ticket) Rollback() {
	t.l.mu.Lock()
	defer t.l.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	u := t.l.usages[usageKey{t.id, t.pool.Bucket.ID}]
	if u == nil {
		return
	}
	u.events = slices.DeleteFunc(u.events, func(e event) bool { return e.seq == t.seq })
	if u.day == t.day {
		u.dayCount = max(u.dayCount-t.units, 0)
	}
}

// Finish records Google's answer to the reserved request, which stays
// counted, and returns what it means for the key (see Limiter.Observe).
func (t *Ticket) Finish(status int, body []byte) Verdict {
	t.l.mu.Lock()
	t.closed = true
	t.l.mu.Unlock()
	return t.l.Observe(t.pool, t.Key, status, body)
}

// Outcome is what an answer from Google means for the key that got it.
type Outcome string

const (
	// OutcomeAccepted: Google answered 200.
	OutcomeAccepted Outcome = "accepted"
	// OutcomeRateLimited: Google answered 429; the key pauses for the bucket.
	OutcomeRateLimited Outcome = "rate_limited"
	// OutcomeKeyRejected: Google rejected the key; it pauses for every model.
	OutcomeKeyRejected Outcome = "key_rejected"
	// OutcomeOther: any other answer, which says nothing about the key.
	OutcomeOther Outcome = "other"
)

// Verdict is what an answer from Google means for the key that got it, and
// until when the key pauses.
type Verdict struct {
	Outcome Outcome
	Reason  Reason
	Until   time.Time
}

// Observe records Google's answer of status and body to a request on key
// for the pool's bucket: a 429 pauses the key for the bucket, and for the
// bucket of the model Google names when that is another one, by the quota
// it reports (see RateLimitPause); an answer that rejects the key
// (IsKeyRejection) pauses the key for every model for an hour.
func (l *Limiter) Observe(pool Pool, key Key, status int, body []byte) Verdict {
	l.active.Store(true)
	switch {
	case status == 200:
		return Verdict{Outcome: OutcomeAccepted}
	case status == 429:
		l.mu.Lock()
		defer l.mu.Unlock()
		now := l.now()
		pause := RateLimitPause(body, now)
		id := IDOf(key.Secret)
		buckets := []Bucket{pool.Bucket}
		for _, model := range pause.Models {
			if other := ResolveBucket(pool.Rows, model); !slices.ContainsFunc(buckets, func(b Bucket) bool { return b.ID == other.ID }) {
				buckets = append(buckets, other)
			}
		}
		for _, bucket := range buckets {
			u := l.usage(id, bucket.ID, now)
			if pause.Until.After(u.until) {
				u.until, u.reason = pause.Until, pause.Reason
			}
			// A pause until midnight shows the day used up; a shorter one
			// is about Google's previous day and leaves ours as it is.
			if pause.Reason == ReasonDay && bucket.Limits.RPD > 0 && !pause.Until.Before(NextReset(now)) {
				u.dayCount = max(u.dayCount, bucket.Limits.RPD)
			}
		}
		return Verdict{Outcome: OutcomeRateLimited, Reason: pause.Reason, Until: pause.Until}
	case IsKeyRejection(status, body):
		l.mu.Lock()
		defer l.mu.Unlock()
		until := l.now().Add(keyErrorCooldown)
		l.pauses[IDOf(key.Secret)] = keyPause{until: until, reason: ReasonKeyError}
		return Verdict{Outcome: OutcomeKeyRejected, Reason: ReasonKeyError, Until: until}
	}
	return Verdict{Outcome: OutcomeOther}
}

// prune drops, at most once per pruneInterval, the state that no longer
// limits anything: usages without events this minute, count today or pause,
// expired key pauses and idle cursors. The caller holds l.mu.
func (l *Limiter) prune(now time.Time) {
	if now.Sub(l.pruned) < pruneInterval {
		return
	}
	l.pruned = now
	day := PacificDay(now)
	for at, u := range l.usages {
		u.roll(now, day)
		if len(u.events) == 0 && u.dayCount == 0 && !now.Before(u.until) {
			delete(l.usages, at)
		}
	}
	for id, pause := range l.pauses {
		if !now.Before(pause.until) {
			delete(l.pauses, id)
		}
	}
	for at, c := range l.cursors {
		if now.Sub(c.used) > cursorIdle {
			delete(l.cursors, at)
		}
	}
}

// ModelUsage is a key's use of one bucket for the usage panel.
type ModelUsage struct {
	Model          string
	Limits         Limits
	RPMUsed        int
	TPMUsed        int
	RPDUsed        int
	CooldownUntil  time.Time
	CooldownReason Reason
}

// KeyUsage is a key's use of its buckets for the usage panel. A pause of the
// key applies to every model.
type KeyUsage struct {
	Key            Key
	CooldownUntil  time.Time
	CooldownReason Reason
	Models         []ModelUsage
}

// Usage reports each key's use of the buckets of models (the channel's
// upstream model names) and of every other bucket it used today, by bucket
// name.
func (l *Limiter) Usage(keys []Key, rows []dto.GeminiFreeKeyModelLimit, models []string) []KeyUsage {
	configured := map[string]Bucket{}
	for _, model := range models {
		if bucket := ResolveBucket(rows, model); bucket.ID != "" {
			configured[bucket.ID] = bucket
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	day := PacificDay(now)
	used := map[KeyID][]string{}
	for at, u := range l.usages {
		u.roll(now, day)
		if len(u.events) > 0 || u.dayCount > 0 || now.Before(u.until) {
			used[at.key] = append(used[at.key], at.bucket)
		}
	}
	report := make([]KeyUsage, 0, len(keys))
	for _, key := range keys {
		id := IDOf(key.Secret)
		entry := KeyUsage{Key: key}
		if pause, ok := l.pauses[id]; ok && now.Before(pause.until) {
			entry.CooldownUntil, entry.CooldownReason = pause.until, pause.reason
		}
		buckets := maps.Clone(configured)
		for _, name := range used[id] {
			if _, ok := buckets[name]; !ok {
				buckets[name] = ResolveBucket(rows, name)
			}
		}
		for name, bucket := range buckets {
			model := ModelUsage{Model: name, Limits: bucket.Limits}
			if u := l.usages[usageKey{id, name}]; u != nil {
				model.RPMUsed, model.TPMUsed = u.minute()
				model.RPDUsed = u.dayCount
				if now.Before(u.until) {
					model.CooldownUntil, model.CooldownReason = u.until, u.reason
				}
			}
			entry.Models = append(entry.Models, model)
		}
		slices.SortFunc(entry.Models, func(a, b ModelUsage) int { return strings.Compare(a.Model, b.Model) })
		report = append(report, entry)
	}
	return report
}

// Active tells whether free key mode is in use in this process (a request in
// the mode used the limiter, or MarkActive was called), so that callers can
// skip work where the mode is not used.
func (l *Limiter) Active() bool {
	return l.active.Load()
}

// MarkActive records that free key mode is in use: a request in the mode came
// to the limiter, also one answered before it counted anything (a model off
// the free tier), or a channel in the mode is stored.
func (l *Limiter) MarkActive() {
	l.active.Store(true)
}

// Now is the limiter's time.
func (l *Limiter) Now() time.Time {
	return l.now()
}
