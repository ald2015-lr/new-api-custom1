package common

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryRateLimiterAllocatesTimestampsOnDemand(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)

	require.True(t, limiter.Request("client", 1_000_000, 60))

	entry := limiter.store["client"]
	require.NotNil(t, entry)
	assert.Equal(t, 1, entry.requests.length)
	assert.Same(t, entry.requests.head, entry.requests.tail)
	assert.Nil(t, entry.requests.head.next)

	limiter.deleteExpiredEntries(time.Now().Add(time.Hour))
	assert.Contains(t, limiter.store, "client")
}

func TestInMemoryRateLimiterEnforcesWindowBoundary(t *testing.T) {
	var queue rateLimitQueue
	queue.append(100)
	queue.append(100)

	queue.removeExpired(159, 60)
	assert.Equal(t, 2, queue.length)

	queue.removeExpired(160, 60)
	assert.Zero(t, queue.length)
	assert.Nil(t, queue.head)
	assert.Nil(t, queue.tail)
}

func TestInMemoryRateLimiterRemovesAllExpiredRequestsPerCall(t *testing.T) {
	var queue rateLimitQueue
	for range 5 {
		queue.append(100)
	}
	queue.append(110)

	queue.removeExpired(110, 10)
	assert.Equal(t, 1, queue.length)
	require.NotNil(t, queue.head)
	assert.EqualValues(t, 110, queue.head.timestamp)
	assert.Same(t, queue.head, queue.tail)
}

func TestInMemoryRateLimiterKeepsNonExpiredRequests(t *testing.T) {
	var queue rateLimitQueue
	for range 3 {
		queue.append(100)
	}

	queue.removeExpired(109, 10)
	assert.Equal(t, 3, queue.length)
	assert.NotNil(t, queue.head)
	assert.NotNil(t, queue.tail)
}

func TestInMemoryRateLimiterEvictsOnlyExpiredLRUTail(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)
	limiter.expirationDuration = 10 * time.Second

	require.True(t, limiter.Request("active", 1, 100))
	require.True(t, limiter.Request("idle", 1, 100))
	assert.False(t, limiter.Request("active", 1, 100))

	assert.Equal(t, "active", limiter.lru.Front().Value.(*rateLimitEntry).key)
	assert.Equal(t, "idle", limiter.lru.Back().Value.(*rateLimitEntry).key)

	idleEntry := limiter.store["idle"]
	now := time.Now()
	idleEntry.lastActive = now.Add(-10 * time.Second)
	limiter.store["active"].lastActive = now
	limiter.deleteExpiredEntries(now)

	assert.NotContains(t, limiter.store, "idle")
	assert.Contains(t, limiter.store, "active")
	assert.Equal(t, 1, limiter.lru.Len())
	assert.Nil(t, idleEntry.element)
	assert.Nil(t, idleEntry.requests.head)
	assert.Nil(t, idleEntry.requests.tail)
	assert.Zero(t, idleEntry.requests.length)
}

func TestInMemoryRateLimiterCleanupTicksEvictExpiredKeys(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)
	limiter.expirationDuration = 10 * time.Second
	require.True(t, limiter.Request("idle", 1, 100))

	now := time.Now()
	limiter.store["idle"].lastActive = now.Add(-10 * time.Second)
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		limiter.clearExpiredItems(ticks)
		close(done)
	}()

	ticks <- now
	close(ticks)
	<-done

	assert.Empty(t, limiter.store)
	assert.Zero(t, limiter.lru.Len())
}

func TestInMemoryRateLimiterInitKeepsFirstConfiguration(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(time.Hour)
	limiter.Init(0)

	assert.Equal(t, time.Hour, limiter.expirationDuration)
	require.True(t, limiter.Request("client", 1, 60))
	limiter.deleteExpiredEntries(time.Now().Add(time.Hour))
	assert.NotContains(t, limiter.store, "client")
}

func TestInMemoryRateLimiterConcurrentInitializationAndRequests(t *testing.T) {
	var limiter InMemoryRateLimiter
	var allowed atomic.Int64
	var waitGroup sync.WaitGroup

	for range 100 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			limiter.Init(0)
			if limiter.Request("client", 10, 60) {
				allowed.Add(1)
			}
		}()
	}

	waitGroup.Wait()
	assert.EqualValues(t, 10, allowed.Load())
	assert.Len(t, limiter.store, 1)
	assert.Equal(t, 10, limiter.store["client"].requests.length)
}

// A rejected request is told how long until the window has room for it. When
// the limit was lowered while the window held more requests than the new
// limit, that is when enough requests have left the window for the count to
// drop below the new limit, not when the oldest one has.
func TestInMemoryRateLimiterRetryAfterFollowsALoweredLimit(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)
	now := time.Now()
	entry := limiter.touchEntry("client", now)
	for _, age := range []int64{50, 40, 30, 20, 10} {
		entry.requests.append(now.Unix() - age)
	}

	allowed, retryAfter := limiter.RequestWithRetryAfter("client", 5, 60)
	assert.False(t, allowed)
	assert.InDelta(t, 10, retryAfter, 1, "the oldest request leaves the window after 10 seconds")

	allowed, retryAfter = limiter.RequestWithRetryAfter("client", 2, 60)
	assert.False(t, allowed)
	assert.InDelta(t, 40, retryAfter, 1, "four of the five requests have to leave the window")
	assert.Equal(t, 5, entry.requests.length, "rejected requests are not recorded")
}

// Request rejects from the request count alone, also after the limit was
// lowered far below the requests in the window. Walking them, as
// RequestWithRetryAfter does to find its wait, would hold the mutex the API,
// login and model limiters share for as long as the backlog is long. This
// window claims far more requests than it links, so a walk would run off its
// end.
func TestInMemoryRateLimiterRequestRejectsWithoutWalkingTheWindow(t *testing.T) {
	var limiter InMemoryRateLimiter
	limiter.Init(0)
	now := time.Now()
	entry := limiter.touchEntry("client", now)
	entry.requests.append(now.Unix() - 10)
	entry.requests.length = 200_000

	assert.NotPanics(t, func() {
		assert.False(t, limiter.Request("client", 1000, 60))
	})
	assert.Equal(t, 200_000, entry.requests.length, "the rejected request is not recorded")
}
