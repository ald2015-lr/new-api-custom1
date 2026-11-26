package service

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRankingBuilds replaces the rankings build for one test: each build
// counts, waits for release when it is set, and returns a new snapshot.
type fakeRankingBuilds struct {
	builds      atomic.Int32
	finished    atomic.Int32
	started     chan string
	release     chan struct{}
	releaseOnce sync.Once
	err         atomic.Pointer[error]
}

// releaseBuilds lets the held builds finish; it may be called more than once.
func (f *fakeRankingBuilds) releaseBuilds() {
	if f.release != nil {
		f.releaseOnce.Do(func() { close(f.release) })
	}
}

func useFakeRankingBuilds(t *testing.T, blocking bool) *fakeRankingBuilds {
	t.Helper()
	fake := &fakeRankingBuilds{started: make(chan string, 64)}
	if blocking {
		fake.release = make(chan struct{})
	}
	previousBuild := buildRankings
	buildRankings = func(config rankingPeriodConfig, now time.Time) (*RankingsResponse, error) {
		fake.builds.Add(1)
		defer fake.finished.Add(1)
		select {
		case fake.started <- config.id:
		default:
		}
		if fake.release != nil {
			<-fake.release
		}
		if err := fake.err.Load(); err != nil {
			return nil, *err
		}
		return &RankingsResponse{Models: []RankedModel{{ModelName: config.id + "-model", TotalTokens: now.UnixNano()}}}, nil
	}
	resetRankingCache()
	t.Cleanup(func() {
		// Background rebuilds the test started may still be running.
		fake.releaseBuilds()
		require.Eventually(t, func() bool { return fake.finished.Load() == fake.builds.Load() }, 5*time.Second, time.Millisecond)
		buildRankings = previousBuild
		resetRankingCache()
	})
	return fake
}

func resetRankingCache() {
	rankingCacheMu.Lock()
	defer rankingCacheMu.Unlock()
	rankingCache = map[string]rankingCacheItem{}
}

func setRankingCacheItem(id string, data *RankingsResponse, expiresAt time.Time) {
	rankingCacheMu.Lock()
	defer rankingCacheMu.Unlock()
	rankingCache[id] = rankingCacheItem{expiresAt: expiresAt, data: data}
}

// Concurrent requests for a period without a snapshot wait for one shared
// build, which is cached for the next requests.
func TestRankingsColdRequestsShareOneBuild(t *testing.T) {
	fake := useFakeRankingBuilds(t, true)
	const callers = 16
	results := make([]*RankingsResponse, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { results[i], errs[i] = GetRankingsSnapshot("week") })
	}
	require.Equal(t, "week", <-fake.started)
	// Let every caller reach the shared build before it finishes.
	time.Sleep(50 * time.Millisecond)
	fake.releaseBuilds()
	wg.Wait()

	assert.EqualValues(t, 1, fake.builds.Load(), "one build for all cold requests")
	for i := range callers {
		require.NoError(t, errs[i])
		assert.Same(t, results[0], results[i])
	}
	cached, err := GetRankingsSnapshot("")
	require.NoError(t, err)
	assert.Same(t, results[0], cached, "the empty period is the week, served from the cache")
	assert.EqualValues(t, 1, fake.builds.Load())
}

// A snapshot that expired less than one TTL ago is answered at once while one
// background build replaces it; the next requests get the new snapshot.
func TestRankingsServeExpiredSnapshotWhileOneRebuildRuns(t *testing.T) {
	fake := useFakeRankingBuilds(t, true)
	stale := &RankingsResponse{Models: []RankedModel{{ModelName: "stale"}}}
	setRankingCacheItem("today", stale, time.Now().Add(-time.Minute))

	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			data, err := GetRankingsSnapshot("today")
			assert.NoError(t, err)
			assert.Same(t, stale, data)
		})
	}
	answered := make(chan struct{})
	go func() {
		wg.Wait()
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		fake.releaseBuilds()
		t.Fatal("requests waited for the rebuild")
	}
	require.Equal(t, "today", <-fake.started)
	fake.releaseBuilds()
	require.Eventually(t, func() bool {
		data, err := GetRankingsSnapshot("today")
		return err == nil && data != stale
	}, 5*time.Second, time.Millisecond)
	assert.EqualValues(t, 1, fake.builds.Load(), "exactly one rebuild")
}

// A snapshot that expired more than one TTL ago is not served: the request
// waits for the build.
func TestRankingsDoNotServeVeryOldSnapshots(t *testing.T) {
	fake := useFakeRankingBuilds(t, false)
	old := &RankingsResponse{}
	setRankingCacheItem("month", old, time.Now().Add(-rankingCacheTTL-time.Second))

	data, err := GetRankingsSnapshot("month")
	require.NoError(t, err)
	assert.NotSame(t, old, data)
	assert.Equal(t, "month-model", data.Models[0].ModelName)
	assert.EqualValues(t, 1, fake.builds.Load())
}

// A failed build returns its error and caches nothing; a failed background
// rebuild keeps the expired snapshot in use.
func TestRankingsBuildErrors(t *testing.T) {
	fake := useFakeRankingBuilds(t, false)
	buildErr := errors.New("quota_data query failed")
	fake.err.Store(&buildErr)

	_, err := GetRankingsSnapshot("year")
	assert.ErrorIs(t, err, buildErr)
	_, err = GetRankingsSnapshot("year")
	assert.ErrorIs(t, err, buildErr)
	assert.EqualValues(t, 2, fake.builds.Load(), "errors are not cached")

	stale := &RankingsResponse{}
	setRankingCacheItem("year", stale, time.Now().Add(-time.Minute))
	data, err := GetRankingsSnapshot("year")
	require.NoError(t, err)
	assert.Same(t, stale, data)
	require.Eventually(t, func() bool { return fake.builds.Load() == 3 }, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		data, err := GetRankingsSnapshot("year")
		return err == nil && data == stale
	}, time.Second, time.Millisecond, "the expired snapshot stays in use")

	_, err = GetRankingsSnapshot("decade")
	assert.Error(t, err, "an unknown period is refused")
}
