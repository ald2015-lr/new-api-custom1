package model

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// geminiFreeKeySettings are the other settings of channel when it is a Gemini
// channel in free key mode; ok is false for any other channel.
func geminiFreeKeySettings(channel *Channel) (settings dto.ChannelOtherSettings, ok bool) {
	if channel == nil || channel.Type != constant.ChannelTypeGemini || channel.OtherSettings == "" {
		return settings, false
	}
	if common.UnmarshalJsonStr(channel.OtherSettings, &settings) != nil {
		return settings, false
	}
	return settings, settings.GeminiFreeKeyEnabled
}

// noteGeminiFreeKeyChannel marks the free key limiter active when channel, as
// just saved, is a Gemini channel in free key mode, so that selection from the
// database passes it over (dropGeminiFreeKeyExhaustedAbilities) from the next
// request on rather than from the first one relayed in the mode.
func noteGeminiFreeKeyChannel(channel *Channel) {
	if _, ok := geminiFreeKeySettings(channel); ok {
		geminifreekey.Default().MarkActive()
	}
}

// InitGeminiFreeKeyLimiter marks the free key limiter active at startup when
// a stored Gemini channel, enabled or not, is in free key mode, so that a
// fresh process selecting from the database passes such a channel over from
// its first request. A deployment without one keeps the limiter inactive, and
// its selections read no channel for the mode. On a database error the
// limiter stays as it was: the first request relayed in the mode activates it.
func InitGeminiFreeKeyLimiter() {
	var channels []*Channel
	if err := DB.Select("id", "type", "settings").Where("type = ?", constant.ChannelTypeGemini).Find(&channels).Error; err != nil {
		common.SysError(common.LogText("failed to read Gemini free key channels: %s", err.Error()))
		return
	}
	for _, channel := range channels {
		if _, ok := geminiFreeKeySettings(channel); ok {
			geminifreekey.Default().MarkActive()
			return
		}
	}
}

// geminiFreeKeyCannotServe tells whether channel is a Gemini channel in free
// key mode that cannot serve modelName at all now: the model (after the
// channel's model mapping) is not on the free tier, or every enabled key is
// paused or at its requests per day for it (geminifreekey.Limiter.CanServe).
// It is a best effort that keeps the channel when unsure; the relay decides.
// Callers holding channelSyncLock pass holdsCacheLock, which reads the keys'
// status only when the channel's polling lock is free: UpdateChannelStatus
// takes that lock before channelSyncLock.
func geminiFreeKeyCannotServe(channel *Channel, modelName string, holdsCacheLock bool) bool {
	settings, ok := geminiFreeKeySettings(channel)
	if !ok {
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
// the retry moves past. A channel the request already tried (tried) stays, so
// that the priority tiers, which the retry index walks, stay as they were for
// the request's first attempt; exhausted names it and every other channel
// that cannot serve the model, which the tier's choice passes over while it
// has another channel. The caller holds channelSyncLock.
func dropGeminiFreeKeyExhaustedChannels(ids []int, modelName string, tried []int) (kept []int, exhausted map[int]bool) {
	kept = make([]int, 0, len(ids))
	for _, id := range ids {
		if !geminiFreeKeyCannotServe(channelsIDM[id], modelName, true) {
			kept = append(kept, id)
			continue
		}
		if exhausted == nil {
			exhausted = map[int]bool{}
		}
		exhausted[id] = true
		if slices.Contains(tried, id) {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return ids, exhausted
	}
	return kept, exhausted
}

// dropGeminiFreeKeyExhaustedAbilities is dropGeminiFreeKeyExhaustedChannels
// for the abilities of database selection. It reads the Gemini channels among
// them only while the free key limiter is active: a Gemini channel in free key
// mode was stored at startup (InitGeminiFreeKeyLimiter) or saved since, or
// this process relayed in the mode. So it costs nothing where the mode is not
// used.
func dropGeminiFreeKeyExhaustedAbilities(abilities []Ability, modelName string, tried []int) ([]Ability, map[int]bool) {
	if len(abilities) == 0 || !geminifreekey.Default().Active() {
		return abilities, nil
	}
	ids := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		ids = append(ids, ability.ChannelId)
	}
	var channels []*Channel
	if err := DB.Where("id IN ? AND type = ?", ids, constant.ChannelTypeGemini).Find(&channels).Error; err != nil || len(channels) == 0 {
		return abilities, nil
	}
	exhausted := map[int]bool{}
	for _, channel := range channels {
		if geminiFreeKeyCannotServe(channel, modelName, false) {
			exhausted[channel.Id] = true
		}
	}
	if len(exhausted) == 0 {
		return abilities, nil
	}
	kept := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		if !exhausted[ability.ChannelId] || slices.Contains(tried, ability.ChannelId) {
			kept = append(kept, ability)
		}
	}
	if len(kept) == 0 {
		return abilities, exhausted
	}
	return kept, exhausted
}
