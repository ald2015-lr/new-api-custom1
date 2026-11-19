package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/moderation"

	"github.com/gin-gonic/gin"
)

// TestChannelModeration makes one content moderation call with a harmless
// text on an OpenAI channel, with its first enabled key, base URL, proxy and
// header overrides, so an administrator can see whether the channel can
// moderate before turning moderation on. It changes nothing: no key polling,
// no cache, no log row.
func TestChannelModeration(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		common.ApiErrorT(c, "Invalid channel ID")
		return
	}
	channel, err := model.CacheGetChannel(channelID)
	if err != nil {
		channel, err = model.GetChannelById(channelID, true)
		if err != nil {
			common.ApiErrorT(c, "Channel does not exist")
			return
		}
	}
	if !relaycommon.SupportsOpenAISafetyFeatures(channel.Type) {
		common.ApiErrorT(c, "Content moderation is only available on OpenAI channels")
		return
	}
	key, keyIndex := channel.Key, 0
	if channel.ChannelInfo.IsMultiKey {
		// The first enabled key, without advancing the channel's polling.
		keys, enabled := channel.EnabledMultiKeys()
		key = ""
		if len(enabled) > 0 {
			key, keyIndex = keys[enabled[0]], enabled[0]
		}
	}
	if key == "" {
		common.ApiErrorT(c, "The channel has no enabled key")
		return
	}
	organization := ""
	if channel.OpenAIOrganization != nil {
		organization = *channel.OpenAIOrganization
	}
	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          channel.Type,
			ChannelId:            channel.Id,
			ChannelIsMultiKey:    channel.ChannelInfo.IsMultiKey,
			ChannelMultiKeyIndex: keyIndex,
			ChannelBaseUrl:       channel.GetBaseURL(),
			ApiKey:               key,
			Organization:         organization,
			HeadersOverride:      channel.GetHeaderOverride(),
			ChannelSetting:       channel.GetSetting(),
			ChannelOtherSettings: channel.GetOtherSettings(),
		},
	}
	result := moderation.Probe(c.Request.Context(), c, info)
	if c.Request.Context().Err() != nil {
		return // the administrator left; nobody reads the answer
	}
	outcome := "ok"
	if !result.OK {
		outcome = result.ErrorClass
	}
	logger.LogInfo(c, common.LogText("content moderation probe of channel #%d: %s (status %d, %d ms)", channel.Id, outcome, result.Status, result.LatencyMs))
	common.ApiSuccess(c, result)
}
