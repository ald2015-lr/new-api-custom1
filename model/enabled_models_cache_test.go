package model

import (
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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

// The memory cache lists what the abilities queries return: per group and
// over all groups, the distinct models of the enabled abilities.
func TestEnabledModelsCacheMatchesTheAbilitiesQueries(t *testing.T) {
	resetPricingEndpointTestTables(t)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "gpt-4o", ChannelId: 1, Enabled: true},
		{Group: "default", Model: "gpt-4o", ChannelId: 2, Enabled: true},
		{Group: "default", Model: "claude-sonnet-4", ChannelId: 2, Enabled: true},
		{Group: "default", Model: "disabled-only", ChannelId: 3, Enabled: false},
		{Group: "vip", Model: "gpt-4o", ChannelId: 4, Enabled: true},
		{Group: "vip", Model: "vip-only", ChannelId: 4, Enabled: true},
		{Group: "vip", Model: "disabled-only", ChannelId: 5, Enabled: false},
	}).Error)
	InitChannelCache()

	for _, group := range []string{"default", "vip", "missing"} {
		assert.ElementsMatch(t, GetGroupEnabledModels(group), GetGroupEnabledModelsCached(group), group)
	}
	assert.Equal(t, []string{"gpt-4o", "claude-sonnet-4"}, GetGroupEnabledModelsCached("default"))
	assert.Equal(t, []string{"gpt-4o", "vip-only"}, GetGroupEnabledModelsCached("vip"))
	assert.Empty(t, GetGroupEnabledModelsCached("missing"))
	assert.ElementsMatch(t, GetEnabledModels(), GetEnabledModelsCached())
	assert.Equal(t, []string{"gpt-4o", "claude-sonnet-4", "vip-only"}, GetEnabledModelsCached())
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
