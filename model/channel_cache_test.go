package model

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
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

// countAbilityQueries counts the queries of the abilities table from now on.
func countAbilityQueries(t *testing.T) *atomic.Int32 {
	t.Helper()
	var queries atomic.Int32
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register("test:count_ability_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			queries.Add(1)
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Query().Remove("test:count_ability_queries") })
	return &queries
}

// forEachChannelCacheDatabase runs test with the memory cache on and DB set to
// empty channels and abilities tables on SQLite and, when TEST_MYSQL_DSN and
// TEST_POSTGRES_DSN are set, on MySQL and PostgreSQL.
func forEachChannelCacheDatabase(t *testing.T, test func(t *testing.T, dialect string)) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType, previousMemoryCache := DB, common.MainDatabaseType(), common.MemoryCacheEnabled
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			common.MemoryCacheEnabled = true
			t.Cleanup(func() {
				db.Exec("DELETE FROM abilities")
				db.Exec("DELETE FROM channels")
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
				InitChannelCache()
				common.MemoryCacheEnabled = previousMemoryCache
				assert.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
			require.NoError(t, db.Exec("DELETE FROM abilities").Error)
			require.NoError(t, db.Exec("DELETE FROM channels").Error)
			test(t, dialect)
		})
	}
}

// groupModelsInModelOrder is GetGroupEnabledModels sorted by the database's
// own ordering of the model column.
func groupModelsInModelOrder(t *testing.T, group string) []string {
	t.Helper()
	var models []string
	require.NoError(t, DB.Table("abilities").Where(commonGroupCol+" = ? and enabled = ?", group, true).
		Distinct("model").Order("model").Pluck("model", &models).Error)
	return models
}

// The memory cache lists what the abilities queries return: per group the
// distinct models of the enabled abilities in the same order (the model
// pickers list them as they come, and the dashboard takes the first), and the
// same models over all groups. An edited channel writes its abilities again
// after the others, and letter case orders models as the database does.
func TestEnabledModelsCacheMatchesTheAbilitiesQueries(t *testing.T) {
	forEachChannelCacheDatabase(t, func(t *testing.T, dialect string) {
		require.NoError(t, DB.Create(&[]Ability{
			{Group: "default", Model: "gpt-4o", ChannelId: 1, Enabled: true},
			{Group: "default", Model: "gpt-4o", ChannelId: 2, Enabled: true},
			{Group: "default", Model: "claude-sonnet-4", ChannelId: 2, Enabled: true},
			{Group: "default", Model: "disabled-only", ChannelId: 3, Enabled: false},
			{Group: "default", Model: "Qwen-Max", ChannelId: 3, Enabled: true},
			{Group: "vip", Model: "gpt-4o", ChannelId: 4, Enabled: true},
			{Group: "vip", Model: "vip-only", ChannelId: 4, Enabled: true},
			{Group: "vip", Model: "disabled-only", ChannelId: 5, Enabled: false},
		}).Error)
		require.NoError(t, DB.Where("channel_id = ?", 1).Delete(&Ability{}).Error)
		require.NoError(t, DB.Create(&[]Ability{
			{Group: "default", Model: "gpt-4o", ChannelId: 1, Enabled: true},
			{Group: "default", Model: "deepseek-chat", ChannelId: 1, Enabled: true},
		}).Error)
		InitChannelCache()

		for _, group := range []string{"default", "vip"} {
			cached := GetGroupEnabledModelsCached(group)
			assert.Equal(t, groupModelsInModelOrder(t, group), cached, group)
			// PostgreSQL's plain DISTINCT has no defined order.
			if dialect != "postgres" {
				assert.Equal(t, GetGroupEnabledModels(group), cached, group)
			}
		}
		assert.ElementsMatch(t, []string{"Qwen-Max", "claude-sonnet-4", "deepseek-chat", "gpt-4o"}, GetGroupEnabledModelsCached("default"))
		assert.Equal(t, []string{"gpt-4o", "vip-only"}, GetGroupEnabledModelsCached("vip"))
		assert.Empty(t, GetGroupEnabledModelsCached("missing"))
		assert.ElementsMatch(t, GetEnabledModels(), GetEnabledModelsCached())
	})
}

// A channel that failing requests disable automatically leaves the cached
// model lists at once, as it leaves channel selection and the abilities
// queries (the periodic sync reloads the cache only later): a single-key
// channel, and a multi-key channel once its last key is disabled. A model
// another enabled channel serves stays.
func TestEnabledModelsCacheDropsAnAutomaticallyDisabledChannel(t *testing.T) {
	forEachChannelCacheDatabase(t, func(t *testing.T, dialect string) {
		create := func(id int, key string, models string, info ChannelInfo) {
			channel := &Channel{Id: id, Type: constant.ChannelTypeOpenAI, Key: key, Status: common.ChannelStatusEnabled,
				Name: fmt.Sprintf("auto-%d", id), Models: models, Group: "default,vip", ChannelInfo: info}
			require.NoError(t, DB.Create(channel).Error)
			require.NoError(t, channel.AddAbilities(nil))
		}
		create(77001, "sk-solo", "solo-model,shared-model", ChannelInfo{})
		create(77002, "sk-a\nsk-b", "multi-model,shared-model", ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModeRandom})
		create(77003, "sk-other", "shared-model", ChannelInfo{})
		InitChannelCache()
		matchTheDatabase := func(step string) {
			t.Helper()
			for _, group := range []string{"default", "vip"} {
				assert.Equal(t, groupModelsInModelOrder(t, group), GetGroupEnabledModelsCached(group), "%s: %s", step, group)
			}
			assert.ElementsMatch(t, GetEnabledModels(), GetEnabledModelsCached(), step)
		}
		matchTheDatabase("reloaded")
		require.Equal(t, []string{"multi-model", "shared-model", "solo-model"}, GetGroupEnabledModelsCached("vip"))

		require.True(t, UpdateChannelStatus(77001, "sk-solo", common.ChannelStatusAutoDisabled, "upstream error"))
		matchTheDatabase("single-key channel disabled")
		assert.Equal(t, []string{"multi-model", "shared-model"}, GetGroupEnabledModelsCached("default"))

		UpdateChannelStatus(77002, "sk-a", common.ChannelStatusAutoDisabled, "upstream error")
		matchTheDatabase("one key of two disabled")
		assert.Equal(t, []string{"multi-model", "shared-model"}, GetGroupEnabledModelsCached("default"))
		require.True(t, UpdateChannelStatus(77002, "sk-b", common.ChannelStatusAutoDisabled, "upstream error"))
		matchTheDatabase("every key disabled")
		assert.Equal(t, []string{"shared-model"}, GetGroupEnabledModelsCached("default"))
		assert.Equal(t, []string{"shared-model"}, GetEnabledModelsCached())
	})
}

// While the memory cache is on, the lists come from the last channel cache
// reload without a query, and callers get copies; while it is off they are
// read from the database.
func TestEnabledModelsCacheServesTheLastReloadWithoutQueries(t *testing.T) {
	resetPricingEndpointTestTables(t)
	// Rebuild the cache from the emptied tables when the test ends.
	t.Cleanup(func() { common.MemoryCacheEnabled = true })
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "gpt-4o", ChannelId: 1, Enabled: true}).Error)
	InitChannelCache()
	queries := countAbilityQueries(t)

	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "gpt-4.1", ChannelId: 2, Enabled: true}).Error)
	assert.Equal(t, []string{"gpt-4o"}, GetGroupEnabledModelsCached("default"), "a new ability shows after the next reload")
	assert.Equal(t, []string{"gpt-4o"}, GetEnabledModelsCached())
	assert.Zero(t, queries.Load(), "no query while the memory cache is on")

	models := GetGroupEnabledModelsCached("default")
	models[0] = "changed"
	assert.Equal(t, []string{"gpt-4o"}, GetGroupEnabledModelsCached("default"), "callers get a copy")

	InitChannelCache()
	assert.ElementsMatch(t, []string{"gpt-4o", "gpt-4.1"}, GetGroupEnabledModelsCached("default"))

	common.MemoryCacheEnabled = false
	queries.Store(0)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "o3", ChannelId: 3, Enabled: true}).Error)
	assert.ElementsMatch(t, []string{"gpt-4o", "gpt-4.1", "o3"}, GetGroupEnabledModelsCached("default"))
	assert.ElementsMatch(t, []string{"gpt-4o", "gpt-4.1", "o3"}, GetEnabledModelsCached())
	assert.EqualValues(t, 2, queries.Load(), "the database is read while the memory cache is off")
}
