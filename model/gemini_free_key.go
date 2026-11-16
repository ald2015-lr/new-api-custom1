package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// geminiFreeKeyCannotServe tells whether channel is a Gemini channel in free
// key mode that cannot serve modelName at all now: the model (after the
// channel's model mapping) is not on the free tier, or every enabled key is
// paused or at its requests per day for it (geminifreekey.Limiter.CanServe).
// It is a best effort that keeps the channel when unsure; the relay decides.
// Callers holding channelSyncLock pass holdsCacheLock, which reads the keys'
// status only when the channel's polling lock is free: UpdateChannelStatus
// takes that lock before channelSyncLock.
func geminiFreeKeyCannotServe(channel *Channel, modelName string, holdsCacheLock bool) bool {
	if channel == nil || channel.Type != constant.ChannelTypeGemini || channel.OtherSettings == "" {
		return false
	}
	var settings dto.ChannelOtherSettings
	if common.UnmarshalJsonStr(channel.OtherSettings, &settings) != nil || !settings.GeminiFreeKeyEnabled {
		return false
	}
	upstreamModel := modelName
	if mapping := channel.GetModelMapping(); mapping != "" && mapping != "{}" {
		var mapped map[string]string
		if common.UnmarshalJsonStr(mapping, &mapped) == nil && mapped[modelName] != "" {
			upstreamModel = mapped[modelName]
		}
	}
	rows := settings.EffectiveGeminiFreeKeyModelLimits()
	pool := geminifreekey.Pool{ChannelID: channel.Id, Bucket: geminifreekey.ResolveBucket(rows, upstreamModel), Rows: rows}
	if pool.Bucket.NotOnFreeTier() {
		return true
	}
	if !channel.ChannelInfo.IsMultiKey {
		pool.Keys = []geminifreekey.Key{{Index: 0, Secret: channel.Key}}
		return !geminifreekey.Default().CanServe(pool)
	}
	lock := GetChannelPollingLock(channel.Id)
	if holdsCacheLock {
		if !lock.TryLock() {
			return false
		}
	} else {
		lock.Lock()
	}
	keys := channel.GetKeys()
	for i, key := range keys {
		if status, ok := channel.ChannelInfo.MultiKeyStatusList[i]; !ok || status == common.ChannelStatusEnabled {
			pool.Keys = append(pool.Keys, geminifreekey.Key{Index: i, Secret: key})
		}
	}
	lock.Unlock()
	return len(pool.Keys) > 0 && !geminifreekey.Default().CanServe(pool)
}

// dropGeminiFreeKeyExhaustedChannels removes from the cached channel ids those
// that cannot serve modelName now (geminiFreeKeyCannotServe), unless that
// would remove them all: the relay then answers with its rate limit, which
// the retry moves past. The caller holds channelSyncLock.
func dropGeminiFreeKeyExhaustedChannels(ids []int, modelName string) []int {
	kept := make([]int, 0, len(ids))
	for _, id := range ids {
		if !geminiFreeKeyCannotServe(channelsIDM[id], modelName, true) {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 || len(kept) == len(ids) {
		return ids
	}
	return kept
}

// dropGeminiFreeKeyExhaustedAbilities is dropGeminiFreeKeyExhaustedChannels
// for the abilities of database selection. It reads the Gemini channels among
// them only once this process has relayed in free key mode, so that it costs
// nothing where the mode is not used.
func dropGeminiFreeKeyExhaustedAbilities(abilities []Ability, modelName string) []Ability {
	if len(abilities) == 0 || !geminifreekey.Default().Active() {
		return abilities
	}
	ids := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		ids = append(ids, ability.ChannelId)
	}
	var channels []*Channel
	if err := DB.Where("id IN ? AND type = ?", ids, constant.ChannelTypeGemini).Find(&channels).Error; err != nil || len(channels) == 0 {
		return abilities
	}
	exhausted := map[int]bool{}
	for _, channel := range channels {
		if geminiFreeKeyCannotServe(channel, modelName, false) {
			exhausted[channel.Id] = true
		}
	}
	if len(exhausted) == 0 {
		return abilities
	}
	kept := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		if !exhausted[ability.ChannelId] {
			kept = append(kept, ability)
		}
	}
	if len(kept) == 0 {
		return abilities
	}
	return kept
}
