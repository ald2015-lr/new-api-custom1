package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FlushBatchUpdates, which the shutdown calls, writes every delta the batch
// updater holds: the user's quota, used quota and request count, the token's
// remaining and used quota and the channel's used quota get every queued
// change, and the stores are left empty.
func TestFlushBatchUpdatesWritesEveryQueuedDelta(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 500)
	channel := Channel{Name: "flush-channel", Key: "flush-key", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)

	require.NoError(t, DecreaseUserQuota(user.Id, 300, false))
	require.NoError(t, IncreaseUserQuota(user.Id, 50, false))
	UpdateUserUsedQuotaAndRequestCount(user.Id, 300)
	UpdateUserUsedQuotaAndRequestCount(user.Id, 20)
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 200))
	UpdateChannelUsedQuota(channel.Id, 320)
	require.Equal(t, 1000, getUserQuotaFromDB(t, user.Id), "the deltas wait for the next flush")

	FlushBatchUpdates()

	var flushedUser User
	require.NoError(t, DB.First(&flushedUser, user.Id).Error)
	assert.Equal(t, 750, flushedUser.Quota)
	assert.Equal(t, 320, flushedUser.UsedQuota)
	assert.Equal(t, 2, flushedUser.RequestCount)
	flushedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 300, flushedToken.RemainQuota)
	assert.Equal(t, 200, flushedToken.UsedQuota)
	var flushedChannel Channel
	require.NoError(t, DB.First(&flushedChannel, channel.Id).Error)
	assert.EqualValues(t, 320, flushedChannel.UsedQuota)
	for i := range BatchUpdateTypeCount {
		batchUpdateLocks[i].Lock()
		assert.Empty(t, batchUpdateStores[i], "store %d", i)
		batchUpdateLocks[i].Unlock()
	}

	// Nothing is written twice.
	FlushBatchUpdates()
	assert.Equal(t, 750, getUserQuotaFromDB(t, user.Id))
}

// A flush the periodic updater is running finishes before FlushBatchUpdates
// returns, so the exit right after it cannot cut those writes short.
func TestFlushBatchUpdatesWaitsForARunningFlush(t *testing.T) {
	resetBatchUpdateTestState(t)
	batchFlushMu.Lock()
	done := make(chan struct{})
	go func() {
		FlushBatchUpdates()
		close(done)
	}()
	select {
	case <-done:
		batchFlushMu.Unlock()
		t.Fatal("FlushBatchUpdates returned while another flush was running")
	case <-time.After(50 * time.Millisecond):
	}
	batchFlushMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FlushBatchUpdates did not return after the running flush ended")
	}
}
