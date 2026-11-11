package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useMemoryChannelCache(t *testing.T) {
	t.Helper()
	truncateTables(t)
	originalMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	// Rebuild from empty tables while the memory cache is still on, so no
	// test channel stays in the cache for later tests.
	t.Cleanup(func() {
		DB.Exec("DELETE FROM channels")
		DB.Exec("DELETE FROM abilities")
		InitChannelCache()
		common.MemoryCacheEnabled = originalMemoryCache
	})
}

// A channel edited between the channels and abilities queries can carry a
// group no ability lists yet; the periodic sync used to panic on it.
func TestInitChannelCacheIndexesGroupsMissingFromAbilities(t *testing.T) {
	useMemoryChannelCache(t)
	channel := Channel{Id: 940001, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "fresh-group", Models: "gpt-4o", Group: "default,vip"}
	require.NoError(t, channel.Insert())
	require.NoError(t, DB.Where("channel_id = ? AND "+commonGroupCol+" = ?", channel.Id, "vip").Delete(&Ability{}).Error)

	require.NotPanics(t, InitChannelCache)

	selected, err := GetRandomSatisfiedChannel("vip", "gpt-4o", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, channel.Id, selected.Id)
}

// A failed query (e.g. a stalled database) must not rebuild the cache from an
// empty result: the previous cache keeps serving requests.
func TestInitChannelCacheKeepsPreviousCacheWhenQueryFails(t *testing.T) {
	for name, tables := range map[string][]any{
		"channels query fails":  nil,
		"abilities query fails": {&Channel{}},
	} {
		t.Run(name, func(t *testing.T) {
			useMemoryChannelCache(t)
			channel := Channel{Id: 940101, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "cached", Models: "gpt-4o", Group: "default"}
			require.NoError(t, channel.Insert())
			InitChannelCache()

			broken, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, broken.AutoMigrate(tables...))
			originalDB := DB
			DB = broken
			restored := false
			restore := func() {
				if restored {
					return
				}
				restored = true
				DB = originalDB
				if sqlDB, err := broken.DB(); err == nil {
					_ = sqlDB.Close()
				}
			}
			t.Cleanup(restore)
			require.NotPanics(t, InitChannelCache)
			restore()

			selected, err := GetRandomSatisfiedChannel("default", "gpt-4o", 0, nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
			assert.Equal(t, channel.Id, selected.Id)
		})
	}
}

func TestSyncChannelCacheOnceRecoversFromPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		syncChannelCacheOnce(func() { panic("boom") })
	})
}
