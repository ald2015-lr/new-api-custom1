package model

import (
	"fmt"
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

// The free key skip keeps the priority tiers a request walks: a free channel
// the request tried and exhausted keeps its tier, so that the retry goes on to
// the next tier, but is not chosen again while its tier has another channel;
// one exhausted before the request is passed over from its first attempt. A
// single-key channel counts its one key, and a channel of another type is
// never skipped, whatever its settings say. The memory cache and the database
// agree.
func TestSelectionKeepsTheTiersOfAGeminiFreeKeyRequest(t *testing.T) {
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
			create := func(id, channelType int, key, models string, priority int64, weight uint, settings string) {
				channel := Channel{Id: id, Type: channelType, Key: key, Status: common.ChannelStatusEnabled, Name: "gfk-tiers", Models: models, Group: "default", Priority: &priority, Weight: &weight, OtherSettings: settings}
				require.NoError(t, channel.Insert())
			}
			create(960001, constant.ChannelTypeGemini, "gfk-tier-key", "gemini-2.5-flash", 10, 0, on)
			create(960002, constant.ChannelTypeOpenAI, "sk-tier-5", "gemini-2.5-flash", 5, 0, "")
			create(960003, constant.ChannelTypeOpenAI, "sk-tier-0", "gemini-2.5-flash", 0, 0, "")
			create(960004, constant.ChannelTypeGemini, "gfk-same-tier-key", "gemini-3-flash-preview", 0, 100, on)
			create(960005, constant.ChannelTypeOpenAI, "sk-same-tier", "gemini-3-flash-preview", 0, 1, "")
			create(960006, constant.ChannelTypeOpenAI, "gfk-other-type-key", "gemini-3.6-flash", 10, 0, on)
			create(960007, constant.ChannelTypeOpenAI, "sk-other-type", "gemini-3.6-flash", 0, 0, "")
			if memoryCache {
				InitChannelCache()
			}
			picked := func(modelName string, retry int, tried ...int) int {
				t.Helper()
				channel, err := GetRandomSatisfiedChannel("default", modelName, retry, nil, tried...)
				require.NoError(t, err)
				require.NotNil(t, channel)
				return channel.Id
			}
			rows := dto.DefaultGeminiFreeKeyModelLimits()
			pauseForTheDay := func(modelName, key string) {
				pool := geminifreekey.Pool{Bucket: geminifreekey.ResolveBucket(rows, modelName), Rows: rows}
				limiter.Observe(pool, geminifreekey.Key{Secret: key}, 429, []byte(`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"20"}]}]}}`))
			}

			assert.Equal(t, 960001, picked("gemini-2.5-flash", 0))
			assert.Equal(t, 960002, picked("gemini-2.5-flash", 1, 960001))
			// The free channel's one key runs out on the request's first
			// attempt: the retry still goes to the next tier.
			pauseForTheDay("gemini-2.5-flash", "gfk-tier-key")
			assert.Equal(t, 960002, picked("gemini-2.5-flash", 1, 960001))
			assert.Equal(t, 960003, picked("gemini-2.5-flash", 2, 960001, 960002))
			// A request that starts after it ran out passes over it.
			assert.Equal(t, 960002, picked("gemini-2.5-flash", 0))
			assert.Equal(t, 960003, picked("gemini-2.5-flash", 1, 960002))

			// In one tier the free channel the request exhausted is not chosen
			// again, however heavy it is.
			pauseForTheDay("gemini-3-flash-preview", "gfk-same-tier-key")
			for range 3 {
				assert.Equal(t, 960005, picked("gemini-3-flash-preview", 1, 960004))
			}

			// Only a Gemini channel is in free key mode.
			pauseForTheDay("gemini-3.6-flash", "gfk-other-type-key")
			assert.Equal(t, 960006, picked("gemini-3.6-flash", 0))
		})
	}
}

// useDatabaseSelection selects from the database (memory cache off), as after
// a restart without the memory cache, with a fresh free key limiter that no
// request has used yet.
func useDatabaseSelection(t *testing.T) *geminifreekey.Limiter {
	t.Helper()
	truncateTables(t)
	originalMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	limiter := geminifreekey.New(nil)
	previous := geminifreekey.SetDefault(limiter)
	t.Cleanup(func() {
		geminifreekey.SetDefault(previous)
		DB.Exec("DELETE FROM channels")
		DB.Exec("DELETE FROM abilities")
		common.MemoryCacheEnabled = true
		InitChannelCache()
		common.MemoryCacheEnabled = originalMemoryCache
	})
	return limiter
}

// A Gemini channel saved in free key mode (added, added in a batch or copied,
// edited to turn the mode on, or edited in another field) marks the free key
// limiter active, so that selection from the database passes it over for a
// model the free tier does not serve from the very first request, before
// anything was relayed in the mode.
func TestDatabaseSelectionSkipsASavedGeminiFreeKeyChannelFromTheFirstRequest(t *testing.T) {
	const on = `{"gemini_free_key_enabled":true}`
	for name, save := range map[string]func(t *testing.T, free *Channel){
		"insert": func(t *testing.T, free *Channel) {
			require.NoError(t, free.Insert())
		},
		"batch insert": func(t *testing.T, free *Channel) {
			require.NoError(t, BatchInsertChannels([]Channel{*free}))
		},
		"update turning the mode on": func(t *testing.T, free *Channel) {
			free.OtherSettings = ""
			require.NoError(t, free.Insert())
			require.False(t, geminifreekey.Default().Active())
			free.OtherSettings = on
			require.NoError(t, free.Update())
		},
		"update of another field": func(t *testing.T, free *Channel) {
			require.NoError(t, DB.Create(free).Error)
			require.NoError(t, free.AddAbilities(nil))
			require.False(t, geminifreekey.Default().Active())
			require.NoError(t, (&Channel{Id: free.Id, Name: "gfk-renamed"}).Update())
		},
	} {
		t.Run(name, func(t *testing.T) {
			limiter := useDatabaseSelection(t)
			paidPriority, freePriority := int64(0), int64(10)
			paid := Channel{Id: 970002, Type: constant.ChannelTypeGemini, Key: "sk-paid", Status: common.ChannelStatusEnabled, Name: "paid", Models: "gemini-2.5-pro", Group: "default", Priority: &paidPriority}
			require.NoError(t, paid.Insert())
			free := &Channel{Id: 970001, Type: constant.ChannelTypeGemini, Key: "gfk-first-key", Status: common.ChannelStatusEnabled, Name: "gfk", Models: "gemini-2.5-pro", Group: "default", Priority: &freePriority, OtherSettings: on}
			save(t, free)

			assert.True(t, limiter.Active())
			channel, err := GetRandomSatisfiedChannel("default", "gemini-2.5-pro", 0, nil)
			require.NoError(t, err)
			require.NotNil(t, channel)
			assert.Equal(t, paid.Id, channel.Id, "the free tier does not serve gemini-2.5-pro")
		})
	}
}

// At startup a stored Gemini channel in free key mode, also a disabled one or
// one stored without the model (an older version, a restore), marks the free
// key limiter active, so that a fresh process selecting from the database
// passes it over from its first request. A database error leaves the limiter
// inactive.
func TestInitGeminiFreeKeyLimiterActivatesTheModeOfAStoredChannel(t *testing.T) {
	limiter := useDatabaseSelection(t)
	paidPriority, freePriority := int64(0), int64(10)
	for _, channel := range []*Channel{
		{Id: 971001, Type: constant.ChannelTypeGemini, Key: "gfk-start-key", Status: common.ChannelStatusEnabled, Name: "gfk", Models: "gemini-2.5-pro", Group: "default", Priority: &freePriority, OtherSettings: `{"gemini_free_key_enabled":true}`},
		{Id: 971002, Type: constant.ChannelTypeGemini, Key: "sk-paid", Status: common.ChannelStatusEnabled, Name: "paid", Models: "gemini-2.5-pro", Group: "default", Priority: &paidPriority},
	} {
		require.NoError(t, DB.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(nil))
	}
	require.False(t, limiter.Active())

	broken, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	originalDB := DB
	DB = broken
	require.NotPanics(t, InitGeminiFreeKeyLimiter)
	DB = originalDB
	if sqlDB, err := broken.DB(); err == nil {
		_ = sqlDB.Close()
	}
	assert.False(t, limiter.Active(), "a failed read activates nothing")

	InitGeminiFreeKeyLimiter()
	assert.True(t, limiter.Active())
	channel, err := GetRandomSatisfiedChannel("default", "gemini-2.5-pro", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 971002, channel.Id, "the free tier does not serve gemini-2.5-pro")

	// A disabled channel in the mode counts too: enabling it again does not
	// save it through the model.
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 971001).Update("status", common.ChannelStatusManuallyDisabled).Error)
	fresh := geminifreekey.New(nil)
	geminifreekey.SetDefault(fresh)
	InitGeminiFreeKeyLimiter()
	assert.True(t, fresh.Active())
}

// A deployment without a Gemini channel in free key mode keeps the free key
// limiter inactive at startup and whenever it saves a channel, so that
// selection from the database reads no channel for the mode: one query fewer
// than once the mode is in use. The startup check is one query.
func TestADeploymentWithoutGeminiFreeKeyChannelsKeepsTheLimiterInactive(t *testing.T) {
	limiter := useDatabaseSelection(t)
	priority := int64(0)
	channel := func(id, channelType int, settings string) Channel {
		return Channel{Id: id, Type: channelType, Key: fmt.Sprintf("sk-%d", id), Status: common.ChannelStatusEnabled, Name: "no-gfk", Models: "gemini-2.5-pro", Group: "default", Priority: &priority, OtherSettings: settings}
	}
	inserted := channel(972001, constant.ChannelTypeGemini, "")
	require.NoError(t, inserted.Insert())
	require.NoError(t, BatchInsertChannels([]Channel{
		channel(972002, constant.ChannelTypeGemini, `{"gemini_free_key_enabled":false}`),
		channel(972003, constant.ChannelTypeOpenAI, `{"gemini_free_key_enabled":true}`),
		channel(972004, constant.ChannelTypeGemini, `{"gemini_free_key_reveal_tier_errors":true,"gemini_free_key_max_attempts":3}`),
	}))
	inserted.OtherSettings = `{"gemini_free_key_enabled":false}`
	require.NoError(t, inserted.Update())
	stored := channel(972005, constant.ChannelTypeGemini, "not json")
	require.NoError(t, DB.Create(&stored).Error)

	channelQueries := 0
	const counter = "test:count_channel_queries"
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register(counter, func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" {
			channelQueries++
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Query().Remove(counter) })
	InitGeminiFreeKeyLimiter()
	assert.Equal(t, 1, channelQueries, "the startup check")
	assert.False(t, limiter.Active())

	selectionQueries := func() int {
		t.Helper()
		channelQueries = 0
		selected, err := GetRandomSatisfiedChannel("default", "gemini-2.5-pro", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, selected)
		return channelQueries
	}
	inactive := selectionQueries()
	assert.False(t, limiter.Active())
	limiter.MarkActive()
	assert.Equal(t, inactive+1, selectionQueries(), "only an active limiter reads the Gemini channels")
}
