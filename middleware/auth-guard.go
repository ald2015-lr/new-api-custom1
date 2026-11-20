package middleware

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"golang.org/x/text/cases"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Machine codes of the password reset email limit and the login lockout
// refusals (also read by the web).
const (
	PasswordResetEmailCooldownCode   = "PASSWORD_RESET_EMAIL_COOLDOWN"
	PasswordResetEmailDailyLimitCode = "PASSWORD_RESET_EMAIL_DAILY_LIMIT"
	LoginLockedCode                  = "LOGIN_LOCKED"
)

const (
	authGuardRedisPrefix      = "authguard:v1:"
	authGuardRedisTimeout     = 2 * time.Second
	authGuardMemoryMaxEntries = 100000
	// passwordResetEmailWindow is the rolling window of the daily maximum.
	passwordResetEmailWindow = 24 * time.Hour
)

// authGuardNow is the clock of every decision of this file; Redis only
// stores the values and expires idle keys. Tests replace it.
var authGuardNow = time.Now

// Two separate in-memory stores, so a flood of login attempts cannot evict
// the reset email state. They serve while Redis is off or failing.
var (
	passwordResetMemory = newAuthGuardMemory(authGuardMemoryMaxEntries)
	loginLockMemory     = newAuthGuardMemory(authGuardMemoryMaxEntries)
)

// UseAuthGuardForTest gives tests in other packages empty in-memory stores
// and the clock now. The returned function restores the previous state.
func UseAuthGuardForTest(now func() time.Time) (restore func()) {
	previousNow, previousReset, previousLogin := authGuardNow, passwordResetMemory, loginLockMemory
	authGuardNow = now
	passwordResetMemory, loginLockMemory = newAuthGuardMemory(authGuardMemoryMaxEntries), newAuthGuardMemory(authGuardMemoryMaxEntries)
	return func() {
		authGuardNow, passwordResetMemory, loginLockMemory = previousNow, previousReset, previousLogin
	}
}

// PasswordResetEmailRefusal says why the per-email limit refused a request
// and how long to wait (whole seconds, at least 1).
type PasswordResetEmailRefusal struct {
	Code        string
	WaitSeconds int64
}

// passwordResetEmailLimit returns the clamped cooldown and daily maximum. It
// reads the common vars only: GetStatus calls it while it holds
// OptionMapRWMutex.
func passwordResetEmailLimit() (cooldown time.Duration, dailyMax int) {
	seconds := min(max(common.PasswordResetEmailCooldownSeconds, operation_setting.PasswordResetEmailCooldownMinSeconds), operation_setting.PasswordResetEmailCooldownMaxSeconds)
	dailyMax = min(max(common.PasswordResetEmailDailyMax, 1), operation_setting.PasswordResetEmailDailyMaxLimit)
	return time.Duration(seconds) * time.Second, dailyMax
}

// loginLockoutLimit returns the clamped failure count and the window, which
// is also the lock duration.
func loginLockoutLimit() (maxFailures int, window time.Duration) {
	maxFailures = min(max(common.LoginLockoutMaxFailures, operation_setting.LoginLockoutMinFailures), operation_setting.LoginLockoutMaxFailuresLimit)
	minutes := min(max(common.LoginLockoutMinutes, 1), operation_setting.LoginLockoutMaxMinutes)
	return maxFailures, time.Duration(minutes) * time.Minute
}

// PasswordResetEmailCooldownSeconds is the clamped cooldown while the reset
// email limit is on, else 0 (for /api/status).
func PasswordResetEmailCooldownSeconds() int64 {
	if !common.PasswordResetEmailLimitEnabled {
		return 0
	}
	cooldown, _ := passwordResetEmailLimit()
	return int64(cooldown / time.Second)
}

// authGuardStoreContext keeps a store call alive after the client goes away,
// so a wrong password is still counted when the client disconnects at once.
func authGuardStoreContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), authGuardRedisTimeout)
}

func authGuardRedisFailed(ctx context.Context, operation string, err error) {
	logger.LogWarn(ctx, common.LogText("auth guard: Redis failed, using the in-memory store (%s): %v", operation, err))
}

// authGuardWaitSeconds turns a wait in milliseconds into whole seconds,
// rounded up, at least 1; 0 stays 0.
func authGuardWaitSeconds(waitMs int64) int64 {
	if waitMs <= 0 {
		return 0
	}
	return max((waitMs+999)/1000, 1)
}

func authGuardHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// passwordResetEmailSubject keys the reset email limit by the normalised
// address; the hash keeps it out of Redis key names.
func passwordResetEmailSubject(email string) string {
	return "e:" + authGuardHash(email)
}

// foldLoginName makes typed names that the database may match as one account
// share one counter: compatibility forms, letter case and accents are folded
// (Alice, alice, alicé and ａｌｉｃｅ are one name), so the lock state of a
// typed name never says whether an account exists.
func foldLoginName(name string) string {
	folded := cases.Fold().String(norm.NFKC.String(strings.TrimSpace(name)))
	folded, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), folded)
	if err != nil {
		return cases.Fold().String(strings.TrimSpace(name))
	}
	return folded
}

// LoginLockSubjects returns the counters a password login attempt touches:
// the typed name (n:) unless it is blank, and the account (u:) when userID is
// positive. LoginLockSubjects(7, "") is exactly ["u:7"].
func LoginLockSubjects(userID int, identifier string) []string {
	subjects := make([]string, 0, 2)
	if strings.TrimSpace(identifier) != "" {
		subjects = append(subjects, "n:"+authGuardHash(foldLoginName(identifier)))
	}
	if userID > 0 {
		subjects = append(subjects, "u:"+strconv.Itoa(userID))
	}
	return subjects
}

// loginLockClearSubjects returns the account counter and the counters of the
// names that sign in to it.
func loginLockClearSubjects(userID int, names []string) []string {
	subjects := LoginLockSubjects(userID, "")
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			subjects = append(subjects, LoginLockSubjects(0, name)...)
		}
	}
	return subjects
}

// --- Password reset email limit (P1) ---

// passwordResetEmailTakeScript keeps one rolling 24-hour log of admitted
// requests per address. Reply {status, wait_ms}: 0 admitted (recorded),
// 1 cooldown, 2 daily limit. Refusals record nothing.
const passwordResetEmailTakeScript = `
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[4])
local max = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
local count = redis.call('ZCARD', KEYS[1])
if count >= max then
  local freeing = redis.call('ZRANGE', KEYS[1], count - max, count - max, 'WITHSCORES')
  return {2, tonumber(freeing[2]) + window - now}
end
if count > 0 then
  local newest = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
  local wait = tonumber(newest[2]) + tonumber(ARGV[2]) - now
  if wait > 0 then return {1, wait} end
end
redis.call('ZADD', KEYS[1], now, ARGV[5])
redis.call('PEXPIRE', KEYS[1], window)
return {0, 0}
`

// TakePasswordResetEmail admits one reset email for the normalised address
// while PasswordResetEmailLimitEnabled is on, and records it. It returns nil
// when admitted. A refused request records nothing, so refused clicks never
// use up the daily maximum. The daily maximum is checked first: its wait is
// the longer, more useful one.
func TakePasswordResetEmail(ctx context.Context, email string) *PasswordResetEmailRefusal {
	if !common.PasswordResetEmailLimitEnabled {
		return nil
	}
	cooldown, dailyMax := passwordResetEmailLimit()
	subject := passwordResetEmailSubject(email)
	now := authGuardNow().UnixMilli()
	window := passwordResetEmailWindow.Milliseconds()
	status, waitMs := int64(-1), int64(0)
	if common.RedisEnabled && common.RDB != nil {
		storeCtx, cancel := authGuardStoreContext(ctx)
		values, err := common.RDB.Eval(storeCtx, passwordResetEmailTakeScript, []string{authGuardRedisPrefix + "reset:" + subject},
			strconv.FormatInt(now, 10), strconv.FormatInt(cooldown.Milliseconds(), 10), strconv.Itoa(dailyMax),
			strconv.FormatInt(window, 10), common.GetUUID()).Slice()
		cancel()
		if err == nil {
			status, waitMs, err = authGuardStatusReply(values)
		}
		if err != nil {
			authGuardRedisFailed(ctx, "reset take", err)
			status = -1
		}
	}
	if status < 0 {
		status, waitMs = passwordResetMemory.takeResetEmail(subject, now, cooldown.Milliseconds(), window, dailyMax)
	}
	switch status {
	case 1:
		return &PasswordResetEmailRefusal{Code: PasswordResetEmailCooldownCode, WaitSeconds: authGuardWaitSeconds(waitMs)}
	case 2:
		return &PasswordResetEmailRefusal{Code: PasswordResetEmailDailyLimitCode, WaitSeconds: authGuardWaitSeconds(waitMs)}
	default:
		return nil
	}
}

// ClearPasswordResetEmailLimit forgets the admitted requests of an address.
// It runs even while the limit is off.
func ClearPasswordResetEmailLimit(ctx context.Context, email string) {
	subject := passwordResetEmailSubject(email)
	if common.RedisEnabled && common.RDB != nil {
		storeCtx, cancel := authGuardStoreContext(ctx)
		err := common.RDB.Del(storeCtx, authGuardRedisPrefix+"reset:"+subject).Err()
		cancel()
		if err != nil {
			authGuardRedisFailed(ctx, "reset clear", err)
		}
	}
	passwordResetMemory.clear([]string{subject}, authGuardNow().UnixMilli())
}

// --- Login lockout (P7) ---

// Login lockout scripts. KEYS hold pairs: KEYS[2i-1] is the failure log of a
// subject (failures and in-flight checks), KEYS[2i] its lock-until value in
// unix milliseconds. Go passes every number as a decimal string.

// loginBeginScript refuses when a subject is locked (status 1) or already
// holds max entries in the window (status 2); otherwise it reserves one slot
// on every subject (status 0). Reply {status, wait_ms}.
const loginBeginScript = `
local now, window, max = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
local n = #KEYS / 2
local wait = 0
for i = 1, n do
  local lockedUntil = tonumber(redis.call('GET', KEYS[2*i]) or '0')
  if lockedUntil - now > wait then wait = lockedUntil - now end
end
if wait > 0 then return {1, wait} end
for i = 1, n do
  local fail = KEYS[2*i-1]
  redis.call('ZREMRANGEBYSCORE', fail, '-inf', now - window)
  local count = redis.call('ZCARD', fail)
  if count >= max then
    local freeing = redis.call('ZRANGE', fail, count - max, count - max, 'WITHSCORES')
    local w = tonumber(freeing[2]) + window - now
    if w > wait then wait = w end
  end
end
if wait > 0 then return {2, wait} end
for i = 1, n do
  redis.call('ZADD', KEYS[2*i-1], now, ARGV[4])
  redis.call('PEXPIRE', KEYS[2*i-1], window)
end
return {0, 0}
`

// loginFailScript keeps the reservation as a failure and locks every subject
// whose log now holds max entries. Reply {started, wait_ms}: wait is the
// longest lock now active on the subjects.
const loginFailScript = `
local now, window, max = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
local started, wait = 0, 0
for i = 1, #KEYS / 2 do
  local fail, lock = KEYS[2*i-1], KEYS[2*i]
  local lockedUntil = tonumber(redis.call('GET', lock) or '0')
  if lockedUntil > now then
    if lockedUntil - now > wait then wait = lockedUntil - now end
  else
    redis.call('ZREMRANGEBYSCORE', fail, '-inf', now - window)
    if redis.call('ZCARD', fail) >= max then
      redis.call('SET', lock, ARGV[4], 'PX', window)
      redis.call('DEL', fail)
      started = 1
      if window > wait then wait = window end
    end
  end
end
return {started, wait}
`

// loginReleaseScript removes a reservation; KEYS are the failure logs only.
const loginReleaseScript = `
for i = 1, #KEYS do redis.call('ZREM', KEYS[i], ARGV[1]) end
return 0
`

// loginClearScript deletes the logs and locks and reports whether a lock
// was active.
const loginClearScript = `
local was = 0
for i = 1, #KEYS / 2 do
  if tonumber(redis.call('GET', KEYS[2*i]) or '0') > tonumber(ARGV[1]) then was = 1 end
end
redis.call('DEL', unpack(KEYS))
return was
`

func loginLockKeys(subjects []string) []string {
	keys := make([]string, 0, 2*len(subjects))
	for _, subject := range subjects {
		keys = append(keys, authGuardRedisPrefix+"login:fail:"+subject, authGuardRedisPrefix+"login:lock:"+subject)
	}
	return keys
}

func loginFailKeys(subjects []string) []string {
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		keys = append(keys, authGuardRedisPrefix+"login:fail:"+subject)
	}
	return keys
}

// LoginAttempt is one password (or 2FA code) check that holds a reserved
// failure slot on every subject. Exactly one of Fail or Release ends it; its
// methods are nil-safe and idempotent, so callers may defer Release right
// after BeginLoginAttempt. It belongs to one request.
type LoginAttempt struct {
	subjects    []string
	member      string
	inRedis     bool
	maxFailures int
	window      time.Duration
	done        bool
}

// BeginLoginAttempt checks the subjects and reserves one failure slot on each.
//   - (nil, 0): the lockout is off, or there are no subjects; check the
//     password and count nothing.
//   - (attempt, 0): admitted; exactly one of attempt.Fail / attempt.Release
//     follows.
//   - (nil, wait > 0): refused, a subject is locked or already holds the
//     maximum of failures and checks in flight; answer LOGIN_LOCKED with wait
//     seconds and check nothing. A refusal is not counted.
func BeginLoginAttempt(ctx context.Context, subjects []string) (*LoginAttempt, int64) {
	if !common.LoginLockoutEnabled || len(subjects) == 0 {
		return nil, 0
	}
	maxFailures, window := loginLockoutLimit()
	attempt := &LoginAttempt{subjects: subjects, member: common.GetUUID(), maxFailures: maxFailures, window: window}
	now := authGuardNow().UnixMilli()
	if common.RedisEnabled && common.RDB != nil {
		storeCtx, cancel := authGuardStoreContext(ctx)
		values, err := common.RDB.Eval(storeCtx, loginBeginScript, loginLockKeys(subjects),
			strconv.FormatInt(now, 10), strconv.FormatInt(window.Milliseconds(), 10), strconv.Itoa(maxFailures), attempt.member).Slice()
		cancel()
		var status, waitMs int64
		if err == nil {
			status, waitMs, err = authGuardStatusReply(values)
		}
		if err == nil {
			if status != 0 {
				return nil, authGuardWaitSeconds(waitMs)
			}
			attempt.inRedis = true
			return attempt, 0
		}
		authGuardRedisFailed(ctx, "login begin", err)
	}
	if waitMs := loginLockMemory.beginLogin(subjects, attempt.member, now, window.Milliseconds(), maxFailures); waitMs > 0 {
		return nil, authGuardWaitSeconds(waitMs)
	}
	return attempt, 0
}

// Fail keeps the reservation as a failure. A subject that now holds the
// maximum of failures is locked for the lockout duration and its failure log
// is deleted. lockSeconds is the longest lock now active on the subjects (0
// when none); started reports that this call started a lock.
func (a *LoginAttempt) Fail(ctx context.Context) (lockSeconds int64, started bool) {
	if a == nil || a.done {
		return 0, false
	}
	a.done = true
	now := authGuardNow().UnixMilli()
	window := a.window.Milliseconds()
	lockUntil := now + window
	if a.inRedis {
		storeCtx, cancel := authGuardStoreContext(ctx)
		values, err := common.RDB.Eval(storeCtx, loginFailScript, loginLockKeys(a.subjects),
			strconv.FormatInt(now, 10), strconv.FormatInt(window, 10), strconv.Itoa(a.maxFailures), strconv.FormatInt(lockUntil, 10)).Slice()
		cancel()
		var startedValue, waitMs int64
		if err == nil {
			startedValue, waitMs, err = authGuardStatusReply(values)
		}
		if err != nil {
			// The reservation already in Redis stays a failure.
			authGuardRedisFailed(ctx, "login fail", err)
			return 0, false
		}
		return authGuardWaitSeconds(waitMs), startedValue == 1
	}
	waitMs, started := loginLockMemory.failLogin(a.subjects, now, window, a.maxFailures, lockUntil)
	return authGuardWaitSeconds(waitMs), started
}

// Release removes the reservation: the check was not a counted failure (the
// right password, a disabled account, or a request that ended before the
// check).
func (a *LoginAttempt) Release(ctx context.Context) {
	if a == nil || a.done {
		return
	}
	a.done = true
	if a.inRedis {
		storeCtx, cancel := authGuardStoreContext(ctx)
		err := common.RDB.Eval(storeCtx, loginReleaseScript, loginFailKeys(a.subjects), a.member).Err()
		cancel()
		if err != nil {
			authGuardRedisFailed(ctx, "login release", err)
		}
		return
	}
	loginLockMemory.releaseLogin(a.subjects, a.member)
}

// ClearLoginLockout deletes the failures and locks of the account and of the
// names that sign in to it, and reports whether a lock was active. It runs
// even while the lockout is off, and always clears the in-memory store too:
// a node may have counted there during a Redis outage.
func ClearLoginLockout(ctx context.Context, userID int, names ...string) (wasLocked bool) {
	subjects := loginLockClearSubjects(userID, names)
	if len(subjects) == 0 {
		return false
	}
	now := authGuardNow().UnixMilli()
	if common.RedisEnabled && common.RDB != nil {
		storeCtx, cancel := authGuardStoreContext(ctx)
		was, err := common.RDB.Eval(storeCtx, loginClearScript, loginLockKeys(subjects), strconv.FormatInt(now, 10)).Int64()
		cancel()
		if err != nil {
			authGuardRedisFailed(ctx, "login clear", err)
		}
		wasLocked = was == 1
	}
	return loginLockMemory.clear(subjects, now) || wasLocked
}

// authGuardStatusReply reads the {status, wait_ms} reply of the scripts.
func authGuardStatusReply(values []any) (int64, int64, error) {
	if len(values) != 2 {
		return 0, 0, fmt.Errorf("unexpected auth guard reply length %d", len(values))
	}
	status, err := redisReplyInteger(values[0])
	if err != nil {
		return 0, 0, err
	}
	waitMs, err := redisReplyInteger(values[1])
	if err != nil {
		return 0, 0, err
	}
	return status, waitMs, nil
}

// --- In-memory store ---

// authGuardMemory implements the semantics of the scripts above under one
// mutex. Account subjects (u:) are pinned: never evicted, and bounded by the
// number of accounts. Name and address subjects (n:, e:) are bounded: at most
// limit entries, kept in update order so the least recently updated one is at
// the front and can be dropped in O(1). An entry with an active lock is never
// evicted; a new name is left untracked instead while the front one is
// locked.
type authGuardMemory struct {
	mutex   sync.Mutex
	pinned  map[string]*authGuardEntry
	bounded map[string]*list.Element // values are *authGuardEntry
	order   *list.List
	limit   int
	sweepAt int
}

type authGuardEntry struct {
	subject     string
	marks       []authGuardMark // ascending by at
	lockedUntil int64           // unix ms, login only
	expiresAt   int64           // unix ms after which the entry carries no state
}

type authGuardMark struct {
	at     int64
	member string
}

const authGuardMinPinnedSweep = 1024

func newAuthGuardMemory(limit int) *authGuardMemory {
	return &authGuardMemory{
		pinned:  make(map[string]*authGuardEntry),
		bounded: make(map[string]*list.Element),
		order:   list.New(),
		limit:   limit,
		sweepAt: authGuardMinPinnedSweep,
	}
}

func authGuardPinned(subject string) bool {
	return strings.HasPrefix(subject, "u:")
}

// lookup returns the entry of a subject, or nil when it is not tracked.
func (m *authGuardMemory) lookup(subject string) *authGuardEntry {
	if authGuardPinned(subject) {
		return m.pinned[subject]
	}
	if element, ok := m.bounded[subject]; ok {
		return element.Value.(*authGuardEntry)
	}
	return nil
}

// track returns the entry of a subject, creating it when needed. It returns
// nil when a new bounded subject cannot be tracked because the store is full
// of active locks.
func (m *authGuardMemory) track(subject string, now int64) *authGuardEntry {
	if entry := m.lookup(subject); entry != nil {
		return entry
	}
	entry := &authGuardEntry{subject: subject}
	if authGuardPinned(subject) {
		if len(m.pinned) >= m.sweepAt {
			for key, pinned := range m.pinned {
				if pinned.expiresAt <= now {
					delete(m.pinned, key)
				}
			}
			m.sweepAt = max(authGuardMinPinnedSweep, 2*len(m.pinned))
		}
		m.pinned[subject] = entry
		return entry
	}
	for front := m.order.Front(); front != nil && front.Value.(*authGuardEntry).expiresAt <= now; front = m.order.Front() {
		m.remove(front.Value.(*authGuardEntry))
	}
	if len(m.bounded) >= m.limit {
		front := m.order.Front()
		if front == nil || front.Value.(*authGuardEntry).lockedUntil > now {
			return nil
		}
		m.remove(front.Value.(*authGuardEntry))
	}
	m.bounded[subject] = m.order.PushBack(entry)
	return entry
}

// touched records an update of an entry: its expiry and its place in the
// update order.
func (m *authGuardMemory) touched(entry *authGuardEntry, window int64) {
	entry.expiresAt = entry.lockedUntil
	if count := len(entry.marks); count > 0 {
		entry.expiresAt = max(entry.expiresAt, entry.marks[count-1].at+window)
	}
	if element, ok := m.bounded[entry.subject]; ok {
		m.order.MoveToBack(element)
	}
}

func (m *authGuardMemory) remove(entry *authGuardEntry) {
	if authGuardPinned(entry.subject) {
		delete(m.pinned, entry.subject)
		return
	}
	if element, ok := m.bounded[entry.subject]; ok {
		m.order.Remove(element)
		delete(m.bounded, entry.subject)
	}
}

// prune drops the marks that left the window (score <= now - window, as
// ZREMRANGEBYSCORE does in the scripts).
func (entry *authGuardEntry) prune(now, window int64) {
	kept := 0
	for kept < len(entry.marks) && entry.marks[kept].at <= now-window {
		kept++
	}
	if kept > 0 {
		entry.marks = append(entry.marks[:0], entry.marks[kept:]...)
	}
}

// freeingWait is the time until enough marks leave the window for one more
// (the mark at index count-limit, ascending), or 0 while there is room.
func (entry *authGuardEntry) freeingWait(now, window int64, limit int) int64 {
	count := len(entry.marks)
	if count < limit {
		return 0
	}
	return entry.marks[count-limit].at + window - now
}

func (m *authGuardMemory) takeResetEmail(subject string, now, cooldown, window int64, dailyMax int) (int64, int64) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	entry := m.lookup(subject)
	if entry != nil {
		entry.prune(now, window)
		if wait := entry.freeingWait(now, window, dailyMax); wait > 0 {
			return 2, wait
		}
		if count := len(entry.marks); count > 0 {
			if wait := entry.marks[count-1].at + cooldown - now; wait > 0 {
				return 1, wait
			}
		}
	}
	if entry = m.track(subject, now); entry != nil {
		entry.marks = append(entry.marks, authGuardMark{at: now, member: common.GetUUID()})
		m.touched(entry, window)
	}
	return 0, 0
}

// beginLogin returns the wait in milliseconds when a subject is locked or
// full, else 0 after reserving one slot on every tracked subject.
func (m *authGuardMemory) beginLogin(subjects []string, member string, now, window int64, maxFailures int) int64 {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	var wait int64
	for _, subject := range subjects {
		if entry := m.lookup(subject); entry != nil {
			wait = max(wait, entry.lockedUntil-now)
		}
	}
	if wait > 0 {
		return wait
	}
	for _, subject := range subjects {
		if entry := m.lookup(subject); entry != nil {
			entry.prune(now, window)
			wait = max(wait, entry.freeingWait(now, window, maxFailures))
		}
	}
	if wait > 0 {
		return wait
	}
	for _, subject := range subjects {
		if entry := m.track(subject, now); entry != nil {
			entry.marks = append(entry.marks, authGuardMark{at: now, member: member})
			m.touched(entry, window)
		}
	}
	return 0
}

func (m *authGuardMemory) failLogin(subjects []string, now, window int64, maxFailures int, lockUntil int64) (int64, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	var wait int64
	started := false
	for _, subject := range subjects {
		entry := m.lookup(subject)
		if entry == nil {
			continue
		}
		if entry.lockedUntil > now {
			wait = max(wait, entry.lockedUntil-now)
			continue
		}
		entry.prune(now, window)
		if len(entry.marks) >= maxFailures {
			entry.lockedUntil = lockUntil
			entry.marks = nil
			started = true
			wait = max(wait, window)
			m.touched(entry, window)
		}
	}
	return wait, started
}

func (m *authGuardMemory) releaseLogin(subjects []string, member string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for _, subject := range subjects {
		entry := m.lookup(subject)
		if entry == nil {
			continue
		}
		for i, mark := range entry.marks {
			if mark.member == member {
				entry.marks = append(entry.marks[:i], entry.marks[i+1:]...)
				break
			}
		}
	}
}

// clear forgets the subjects and reports whether one of them was locked.
func (m *authGuardMemory) clear(subjects []string, now int64) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	wasLocked := false
	for _, subject := range subjects {
		if entry := m.lookup(subject); entry != nil {
			wasLocked = wasLocked || entry.lockedUntil > now
			m.remove(entry)
		}
	}
	return wasLocked
}
