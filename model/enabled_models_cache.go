package model

import (
	"slices"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

// enabledAbilityModels holds what GetGroupEnabledModels returns for every group
// and what GetEnabledModels returns, taken from the abilities each channel
// cache reload reads anyway.
type enabledAbilityModels struct {
	byGroup map[string][]string
	all     []string
}

var enabledModelsSnapshot atomic.Pointer[enabledAbilityModels]

// buildEnabledAbilityModels lists the distinct models of the enabled
// abilities, per group and over all groups, in the order the abilities were
// read. InitChannelCache reads them in primary key order (group, model,
// channel), so each group lists its models in the database's own model order,
// as GetGroupEnabledModels does on SQLite and MySQL (PostgreSQL gives that
// query no defined order). The order of all is not meaningful: its one
// caller uses it as a set.
func buildEnabledAbilityModels(abilities []*Ability) *enabledAbilityModels {
	snapshot := &enabledAbilityModels{byGroup: make(map[string][]string)}
	names := make(map[string]string) // one copy of each model name
	inGroup := make(map[string]map[string]struct{})
	for _, ability := range abilities {
		if !ability.Enabled {
			continue
		}
		name, seen := names[ability.Model]
		if !seen {
			name = ability.Model
			names[name] = name
			snapshot.all = append(snapshot.all, name)
		}
		models := inGroup[ability.Group]
		if models == nil {
			models = make(map[string]struct{})
			inGroup[ability.Group] = models
		}
		if _, ok := models[name]; ok {
			continue
		}
		models[name] = struct{}{}
		snapshot.byGroup[ability.Group] = append(snapshot.byGroup[ability.Group], name)
	}
	return snapshot
}

// cachedEnabledModels returns the snapshot while the memory cache is on and
// loaded, nil otherwise.
func cachedEnabledModels() *enabledAbilityModels {
	if !common.MemoryCacheEnabled {
		return nil
	}
	return enabledModelsSnapshot.Load()
}

// GetGroupEnabledModelsCached is GetGroupEnabledModels served from the memory
// channel cache while it is on: the same list as of the last channel cache
// reload, which follows every channel change and sync, without a query per
// group. While the memory cache is off it reads the database.
func GetGroupEnabledModelsCached(group string) []string {
	if snapshot := cachedEnabledModels(); snapshot != nil {
		return slices.Clone(snapshot.byGroup[group])
	}
	return GetGroupEnabledModels(group)
}

// GetEnabledModelsCached is GetEnabledModels served from the memory channel
// cache while it is on, like GetGroupEnabledModelsCached.
func GetEnabledModelsCached() []string {
	if snapshot := cachedEnabledModels(); snapshot != nil {
		return slices.Clone(snapshot.all)
	}
	return GetEnabledModels()
}
