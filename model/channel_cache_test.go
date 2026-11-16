package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"
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

// Selection skips a Gemini channel in free key mode that cannot serve the
// model now: the model (after the channel's model mapping) is not on the free
// tier, or every enabled key is paused or at its requests per day for it. A
// channel with any such key left, a channel with the mode off, and the last
// channel serving the model stay, in the memory cache and the database alike.
func TestSelectionSkipsGeminiFreeKeyChannelsWithoutRoom(t *testing.T) {
	for _, memoryCache := range []bool{true, false} {
		t.Run(map[bool]string{true: "memory cache", false: "database"}[memoryCache], func(t *testing.T) {
			truncateTables(t)
			originalMemoryCache := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = memoryCache
			t.Cleanup(func() {
				DB.Exec("DELETE FROM channels")
				DB.Exec("DELETE FROM abilities")
				common.MemoryCacheEnabled = true
				InitChannelCache()
				common.MemoryCacheEnabled = originalMemoryCache
			})
			limiter := geminifreekey.New(nil)
			previous := geminifreekey.SetDefault(limiter)
			t.Cleanup(func() { geminifreekey.SetDefault(previous) })

			const on = `{"gemini_free_key_enabled":true}`
			create := func(id int, key string, priority int64, settings, mapping string, info ChannelInfo) {
				channel := Channel{Id: id, Type: constant.ChannelTypeGemini, Key: key, Status: common.ChannelStatusEnabled, Name: "gfk", Models: "gemini-2.5-flash,pro-alias,gemini-2.5-flash-latest", Group: "default", Priority: &priority, OtherSettings: settings, ChannelInfo: info}
				if mapping != "" {
					channel.ModelMapping = &mapping
				}
				if id == 950002 {
					channel.Type, channel.Models = constant.ChannelTypeOpenAI, "gemini-2.5-flash,pro-alias"
				}
				require.NoError(t, channel.Insert())
			}
			multiKey := ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyStatusList: map[int]int{2: common.ChannelStatusManuallyDisabled}}
			create(950001, "gfk-a0\ngfk-a1\ngfk-a2", 10, on, `{"pro-alias":"gemini-2.5-pro"}`, multiKey)
			create(950002, "sk-paid", 0, "", "", ChannelInfo{})
			if memoryCache {
				InitChannelCache()
			}
			selected := func(modelName string) int {
				t.Helper()
				channel, err := GetRandomSatisfiedChannel("default", modelName, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, channel)
				return channel.Id
			}
			rows := dto.DefaultGeminiFreeKeyModelLimits()
			flash := geminifreekey.Pool{ChannelID: 950001, Bucket: geminifreekey.ResolveBucket(rows, "gemini-2.5-flash"), Rows: rows}
			pauseForTheDay := func(key string) {
				limiter.Observe(flash, geminifreekey.Key{Secret: key}, 429, []byte(`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"20"}]}]}}`))
			}

			// The database path reads Gemini channels once this process relayed
			// in free key mode.
			limiter.Inspect(flash, 0, 1, nil)
			assert.Equal(t, 950001, selected("gemini-2.5-flash"))
			assert.Equal(t, 950002, selected("pro-alias"), "mapped to a model the free tier does not serve")
			pauseForTheDay("gfk-a0")
			assert.Equal(t, 950001, selected("gemini-2.5-flash"), "key 1 can still serve it")
			// Key 2 is disabled, so its room does not count.
			pauseForTheDay("gfk-a1")
			assert.Equal(t, 950002, selected("gemini-2.5-flash"))
			// gemini-2.5-flash-latest counts as gemini-2.5-flash, and no other
			// channel serves it.
			assert.Equal(t, 950001, selected("gemini-2.5-flash-latest"), "the only channel serving the model stays")

			// With the mode off nothing is skipped.
			require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 950001).Update("settings", `{"gemini_free_key_enabled":false}`).Error)
			if memoryCache {
				InitChannelCache()
			}
			assert.Equal(t, 950001, selected("gemini-2.5-flash"))
			assert.Equal(t, 950001, selected("pro-alias"))
		})
	}
}
