package model

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// quotaLimitQueryGate counts the queries of channel_quota_limits on a test
// database and can hold one after it read its rows, or fail them.
type quotaLimitQueryGate struct {
	queries     atomic.Int32
	hold        atomic.Bool
	fail        atomic.Bool
	held        chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

// open lets a held query finish; it may be called more than once.
func (g *quotaLimitQueryGate) open() {
	g.releaseOnce.Do(func() { close(g.release) })
}

// useChannelQuotaLimitTestDB gives the test its own database with the
// channel_quota_limits table and an empty snapshot.
func useChannelQuotaLimitTestDB(t *testing.T) (*gorm.DB, *quotaLimitQueryGate) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&ChannelQuotaLimit{}))
	gate := &quotaLimitQueryGate{held: make(chan struct{}, 1), release: make(chan struct{})}
	isLimitQuery := func(tx *gorm.DB) bool { return tx.Statement.Table == "channel_quota_limits" }
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:quota_limit_query_start", func(tx *gorm.DB) {
		if !isLimitQuery(tx) {
			return
		}
		gate.queries.Add(1)
		if gate.fail.Load() {
			_ = tx.AddError(errors.New("database unavailable"))
		}
	}))
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:quota_limit_query_hold", func(tx *gorm.DB) {
		if isLimitQuery(tx) && gate.hold.CompareAndSwap(true, false) {
			gate.held <- struct{}{}
			<-gate.release
		}
	}))

	previousDB := DB
	waitForChannelQuotaLimitReload(t)
	resetChannelQuotaLimitSnapshot()
	DB = db
	t.Cleanup(func() {
		gate.open()
		waitForChannelQuotaLimitReload(t)
		resetChannelQuotaLimitSnapshot()
		DB = previousDB
		_ = sqlDB.Close()
	})
	return db, gate
}

func resetChannelQuotaLimitSnapshot() {
	channelQuotaLimitMu.Lock()
	defer channelQuotaLimitMu.Unlock()
	channelQuotaLimitCurrent.Store(nil)
	clear(channelQuotaLimitWrites)
}

func waitForChannelQuotaLimitReload(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return !channelQuotaLimitReloading.Load() }, 5*time.Second, time.Millisecond)
}

// ageChannelQuotaLimitSnapshot makes the current snapshot older than the
// refresh interval.
func ageChannelQuotaLimitSnapshot(t *testing.T) {
	t.Helper()
	channelQuotaLimitMu.Lock()
	defer channelQuotaLimitMu.Unlock()
	current := channelQuotaLimitCurrent.Load()
	require.NotNil(t, current)
	aged := *current
	aged.loadedAt = time.Now().Add(-2 * channelQuotaLimitRefreshInterval)
	channelQuotaLimitCurrent.Store(&aged)
}

// The first lookup loads the limits from the database before it answers, and
// callers that arrive during that load wait for the same query.
func TestChannelQuotaLimitsFirstLookupLoadsSynchronously(t *testing.T) {
	db, gate := useChannelQuotaLimitTestDB(t)
	require.NoError(t, db.Create(&ChannelQuotaLimit{ChannelId: 9101, LimitQuota: 100, UsedQuota: 100, Message: "已用完"}).Error)

	gate.hold.Store(true)
	const callers = 20
	results := make(chan bool, callers)
	var started sync.WaitGroup
	for range callers {
		started.Go(func() {
			exhausted, message := ChannelQuotaExhaustion(9101)
			results <- exhausted && message == "已用完"
		})
	}
	<-gate.held
	select {
	case <-results:
		t.Fatal("a lookup answered before the first load finished")
	case <-time.After(20 * time.Millisecond):
	}
	gate.open()
	started.Wait()
	close(results)
	for exhausted := range results {
		assert.True(t, exhausted)
	}
	assert.EqualValues(t, 1, gate.queries.Load(), "one query for every waiting caller")
	assert.False(t, channelQuotaLimitReloading.Load(), "no background reload for a fresh snapshot")
}

// With a stale snapshot, lookups answer from it at once while one background
// reload runs; neither a slow query nor the 100 callers wait for each other.
// Changes made on this node while the query ran survive the reload, and the
// rows the query read replace the others.
func TestChannelQuotaLimitsServeStaleSnapshotWhileReloading(t *testing.T) {
	db, gate := useChannelQuotaLimitTestDB(t)
	require.NoError(t, db.Create(&ChannelQuotaLimit{ChannelId: 9102, LimitQuota: 100, UsedQuota: 0}).Error)
	exhausted, _ := ChannelQuotaExhaustion(9102)
	require.False(t, exhausted)
	require.EqualValues(t, 1, gate.queries.Load())

	// Another node uses up 9102 and limits 9104 in the database.
	require.NoError(t, db.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", 9102).Update("used_quota", 100).Error)
	require.NoError(t, db.Create(&ChannelQuotaLimit{ChannelId: 9104, LimitCount: 1, UsedCount: 1}).Error)
	ageChannelQuotaLimitSnapshot(t)
	gate.hold.Store(true)

	const callers = 100
	var answered sync.WaitGroup
	var stale atomic.Int32
	begin := time.Now()
	for range callers {
		answered.Go(func() {
			if exhausted, _ := ChannelQuotaExhaustion(9102); !exhausted {
				stale.Add(1)
			}
		})
	}
	<-gate.held // the reload read the rows and is held there
	done := make(chan struct{})
	go func() {
		answered.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lookups waited for the reload")
	}
	t.Logf("%d lookups answered in %s while the reload was held", callers, time.Since(begin))
	assert.Less(t, time.Since(begin), 500*time.Millisecond)
	assert.EqualValues(t, callers, stale.Load(), "every lookup answered from the stale snapshot")
	assert.EqualValues(t, 2, gate.queries.Load(), "one reload for all callers")

	// Local changes while the query ran: 9102 gets a refund, 9103 a limit.
	storeChannelQuotaLimit(9102, &ChannelQuotaLimit{ChannelId: 9102, LimitQuota: 100, UsedQuota: 50})
	storeChannelQuotaLimit(9103, &ChannelQuotaLimit{ChannelId: 9103, LimitCount: 2, UsedCount: 2, Message: "次数已用完"})
	gate.open()
	waitForChannelQuotaLimitReload(t)

	exhausted, _ = ChannelQuotaExhaustion(9102)
	assert.False(t, exhausted, "the local change made during the query wins over the row it read")
	exhausted, message := ChannelQuotaExhaustion(9103)
	assert.True(t, exhausted, "a limit stored during the query is kept")
	assert.Equal(t, "次数已用完", message)
	exhausted, _ = ChannelQuotaExhaustion(9104)
	assert.True(t, exhausted, "rows the reload read are installed")
	assert.EqualValues(t, 2, gate.queries.Load(), "the reloaded snapshot is fresh")

	// The next reload reads the database again for every channel.
	ageChannelQuotaLimitSnapshot(t)
	ChannelQuotaExhaustion(9102)
	waitForChannelQuotaLimitReload(t)
	exhausted, _ = ChannelQuotaExhaustion(9102)
	assert.True(t, exhausted, "the database row applies once no local change is newer")
	exhausted, _ = ChannelQuotaExhaustion(9103)
	assert.False(t, exhausted, "9103 has no row in the database")
}

// A reload whose query fails keeps the limits in use and retries only after
// the next refresh interval.
func TestChannelQuotaLimitsKeepStaleSnapshotWhenReloadFails(t *testing.T) {
	db, gate := useChannelQuotaLimitTestDB(t)
	require.NoError(t, db.Create(&ChannelQuotaLimit{ChannelId: 9105, LimitQuota: 10, UsedQuota: 10}).Error)
	exhausted, _ := ChannelQuotaExhaustion(9105)
	require.True(t, exhausted)

	ageChannelQuotaLimitSnapshot(t)
	gate.fail.Store(true)
	exhausted, _ = ChannelQuotaExhaustion(9105)
	assert.True(t, exhausted)
	waitForChannelQuotaLimitReload(t)
	assert.EqualValues(t, 2, gate.queries.Load())
	exhausted, _ = ChannelQuotaExhaustion(9105)
	assert.True(t, exhausted, "the limits stay in use")
	assert.EqualValues(t, 2, gate.queries.Load(), "no retry before the next interval")

	gate.fail.Store(false)
	require.NoError(t, db.Model(&ChannelQuotaLimit{}).Where("channel_id = ?", 9105).Update("used_quota", 0).Error)
	ageChannelQuotaLimitSnapshot(t)
	ChannelQuotaExhaustion(9105)
	waitForChannelQuotaLimitReload(t)
	assert.EqualValues(t, 3, gate.queries.Load())
	exhausted, _ = ChannelQuotaExhaustion(9105)
	assert.False(t, exhausted)
}
