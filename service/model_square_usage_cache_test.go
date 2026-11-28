package service

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeModelUsageLoads replaces the model usage query for one test, as
// fakeRankingBuilds does for the rankings: each query counts, waits for
// release when it is set, and returns new totals.
type fakeModelUsageLoads struct {
	loads       atomic.Int32
	finished    atomic.Int32
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	err         atomic.Pointer[error]
}

// releaseLoads lets the held queries finish; it may be called more than once.
func (f *fakeModelUsageLoads) releaseLoads() {
	if f.release != nil {
		f.releaseOnce.Do(func() { close(f.release) })
	}
}

func useFakeModelUsageLoads(t *testing.T, blocking bool) *fakeModelUsageLoads {
	t.Helper()
	fake := &fakeModelUsageLoads{started: make(chan struct{}, 64)}
	if blocking {
		fake.release = make(chan struct{})
	}
	previousLoad, previousDataExport := loadModelUsageTotals, common.DataExportEnabled
	loadModelUsageTotals = func(startTime, endTime int64) ([]model.ModelUsageTotal, error) {
		fake.loads.Add(1)
		defer fake.finished.Add(1)
		select {
		case fake.started <- struct{}{}:
		default:
		}
		if fake.release != nil {
			<-fake.release
		}
		if err := fake.err.Load(); err != nil {
			return nil, *err
		}
		return []model.ModelUsageTotal{{ModelName: "fresh", Requests: time.Now().UnixNano(), Tokens: 1}}, nil
	}
	common.DataExportEnabled = true
	resetModelUsageCache()
	t.Cleanup(func() {
		// Background queries the test started may still be running, or not
		// have reached loadModelUsageTotals yet: wait until every query the
		// group still holds has returned before it changes back.
		fake.releaseLoads()
		for _, days := range []int{1, 7, 30} {
			_, _, _ = modelUsageLoads.Do(strconv.Itoa(days), func() (any, error) { return nil, nil })
		}
		loadModelUsageTotals, common.DataExportEnabled = previousLoad, previousDataExport
		resetModelUsageCache()
	})
	return fake
}

func resetModelUsageCache() {
	modelUsageCacheMu.Lock()
	defer modelUsageCacheMu.Unlock()
	modelUsageCache = map[int]modelUsageCacheItem{}
}

func setModelUsageCacheItem(days int, totals []model.ModelUsageTotal, expiresAt time.Time) {
	modelUsageCacheMu.Lock()
	defer modelUsageCacheMu.Unlock()
	modelUsageCache[days] = modelUsageCacheItem{expiresAt: expiresAt, totals: totals}
}

// /api/pricing builds the model square order on every visit. Concurrent
// visits on a cold cache wait for one shared usage query, which is cached for
// the next visits.
func TestModelUsageColdRequestsShareOneQuery(t *testing.T) {
	fake := useFakeModelUsageLoads(t, true)
	setting := operation_setting.GetModelSquareSetting()
	previous := *setting
	t.Cleanup(func() { *setting = previous })
	*setting = operation_setting.ModelSquareSetting{SortMode: "usage", SortMetric: "requests", SortDays: 7}

	const visitors = 16
	orders := make([]ModelSquareOrder, visitors)
	var wg sync.WaitGroup
	for i := range visitors {
		wg.Go(func() { orders[i] = BuildModelSquareOrder([]model.Pricing{{ModelName: "fresh"}, {ModelName: "idle"}}) })
	}
	<-fake.started
	// Let every visitor reach the shared query before it finishes.
	time.Sleep(50 * time.Millisecond)
	fake.releaseLoads()
	wg.Wait()

	assert.EqualValues(t, 1, fake.loads.Load(), "one query for all cold requests")
	for i := range visitors {
		assert.Equal(t, []string{"fresh"}, orders[i].Recommended)
	}
	first, err := GetModelUsageTotals(7)
	require.NoError(t, err)
	second, err := GetModelUsageTotals(7)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.EqualValues(t, 1, fake.loads.Load(), "the next requests are served from the cache")
}

// Usage that expired less than one TTL ago is answered at once while one
// background query replaces it; the next requests get the new usage.
func TestModelUsageServesExpiredTotalsWhileOneQueryRuns(t *testing.T) {
	fake := useFakeModelUsageLoads(t, true)
	stale := []model.ModelUsageTotal{{ModelName: "stale", Requests: 1}}
	setModelUsageCacheItem(7, stale, time.Now().Add(-time.Minute))

	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			totals, err := GetModelUsageTotals(7)
			assert.NoError(t, err)
			assert.Equal(t, stale, totals)
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
		fake.releaseLoads()
		<-answered
		t.Fatal("requests waited for the query")
	}
	<-fake.started
	fake.releaseLoads()
	require.Eventually(t, func() bool {
		totals, err := GetModelUsageTotals(7)
		return err == nil && len(totals) == 1 && totals[0].ModelName == "fresh"
	}, 5*time.Second, time.Millisecond)
	assert.EqualValues(t, 1, fake.loads.Load(), "exactly one background query")
}

// Usage that expired more than one TTL ago is not served: the request waits
// for the query.
func TestModelUsageDoesNotServeVeryOldTotals(t *testing.T) {
	fake := useFakeModelUsageLoads(t, false)
	setModelUsageCacheItem(30, []model.ModelUsageTotal{{ModelName: "old"}}, time.Now().Add(-rankingCacheTTL-time.Second))

	totals, err := GetModelUsageTotals(30)
	require.NoError(t, err)
	require.Len(t, totals, 1)
	assert.Equal(t, "fresh", totals[0].ModelName)
	assert.EqualValues(t, 1, fake.loads.Load())
}

// A failed query returns its error and caches nothing; a failed background
// query keeps the expired usage in use. Without dashboard data nothing is
// queried.
func TestModelUsageQueryErrors(t *testing.T) {
	fake := useFakeModelUsageLoads(t, false)
	queryErr := errors.New("quota_data query failed")
	fake.err.Store(&queryErr)

	_, err := GetModelUsageTotals(1)
	assert.ErrorIs(t, err, queryErr)
	_, err = GetModelUsageTotals(1)
	assert.ErrorIs(t, err, queryErr)
	assert.EqualValues(t, 2, fake.loads.Load(), "errors are not cached")

	stale := []model.ModelUsageTotal{{ModelName: "stale"}}
	setModelUsageCacheItem(1, stale, time.Now().Add(-time.Minute))
	totals, err := GetModelUsageTotals(1)
	require.NoError(t, err)
	assert.Equal(t, stale, totals)
	// Once the background query has failed, the expired usage is still the
	// one served.
	require.Eventually(t, func() bool { return fake.finished.Load() == 3 }, 5*time.Second, time.Millisecond)
	totals, err = GetModelUsageTotals(1)
	require.NoError(t, err)
	assert.Equal(t, stale, totals, "the expired usage stays in use")

	_, _, _ = modelUsageLoads.Do("1", func() (any, error) { return nil, nil })
	loads := fake.loads.Load()
	common.DataExportEnabled = false
	totals, err = GetModelUsageTotals(1)
	require.NoError(t, err)
	assert.Nil(t, totals)
	assert.Equal(t, loads, fake.loads.Load(), "no query without dashboard data")
}
