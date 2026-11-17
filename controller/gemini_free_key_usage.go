package controller

import (
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

type geminiFreeKeyModelUsage struct {
	Model          string `json:"model"`
	RPMUsed        int    `json:"rpm_used"`
	RPMLimit       int    `json:"rpm_limit"`
	TPMUsed        int    `json:"tpm_used"`
	TPMLimit       int    `json:"tpm_limit"`
	RPDUsed        int    `json:"rpd_used"`
	RPDLimit       int    `json:"rpd_limit"`
	CooldownUntil  int64  `json:"cooldown_until"`
	CooldownReason string `json:"cooldown_reason"`
}

type geminiFreeKeyKeyUsage struct {
	Index          int                       `json:"index"`
	KeyHint        string                    `json:"key_hint"`
	Enabled        bool                      `json:"enabled"`
	CooldownUntil  int64                     `json:"cooldown_until"`
	CooldownReason string                    `json:"cooldown_reason"`
	Models         []geminiFreeKeyModelUsage `json:"models"`
}

type geminiFreeKeyUsageResponse struct {
	Enabled     bool                    `json:"enabled"`
	Timezone    string                  `json:"timezone"`
	NextResetAt int64                   `json:"next_reset_at"`
	Keys        []geminiFreeKeyKeyUsage `json:"keys"`
}

// GetGeminiFreeKeyUsage reports, for administrators, how much of the free
// tier's limits each key of a Gemini channel used, per model: the models the
// channel serves and any other the key used today. Times are Unix seconds, 0
// for none; a key shows only its last four characters.
func GetGeminiFreeKeyUsage(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorT(c, "Invalid channel ID")
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiErrorT(c, "Channel does not exist")
		return
	}
	limiter := geminifreekey.Default()
	response := geminiFreeKeyUsageResponse{
		Timezone:    geminifreekey.Timezone,
		NextResetAt: geminifreekey.NextReset(limiter.Now()).Unix(),
		Keys:        []geminiFreeKeyKeyUsage{},
	}
	if channel.Type != constant.ChannelTypeGemini {
		common.ApiSuccess(c, response)
		return
	}
	var settings dto.ChannelOtherSettings
	if channel.OtherSettings != "" {
		_ = common.UnmarshalJsonStr(channel.OtherSettings, &settings)
	}
	response.Enabled = settings.GeminiFreeKeyEnabled

	var keys []geminifreekey.Key
	enabled := map[int]bool{}
	if channel.ChannelInfo.IsMultiKey {
		for i, key := range channel.GetKeys() {
			keys = append(keys, geminifreekey.Key{Index: i, Secret: key})
			status, ok := channel.ChannelInfo.MultiKeyStatusList[i]
			enabled[i] = !ok || status == common.ChannelStatusEnabled
		}
	} else if channel.Key != "" {
		keys = append(keys, geminifreekey.Key{Index: 0, Secret: channel.Key})
		enabled[0] = channel.Status == common.ChannelStatusEnabled
	}
	var mapping map[string]string
	if raw := channel.GetModelMapping(); raw != "" {
		_ = common.UnmarshalJsonStr(raw, &mapping)
	}
	var upstreamModels []string
	for _, name := range channel.GetModels() {
		if mapped := mapping[name]; mapped != "" {
			name = mapped
		}
		upstreamModels = append(upstreamModels, name)
	}

	unix := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	for _, usage := range limiter.Usage(keys, settings.EffectiveGeminiFreeKeyModelLimits(), upstreamModels) {
		entry := geminiFreeKeyKeyUsage{
			Index:          usage.Key.Index,
			KeyHint:        geminifreekey.KeyHint(usage.Key.Secret),
			Enabled:        enabled[usage.Key.Index],
			CooldownUntil:  unix(usage.CooldownUntil),
			CooldownReason: string(usage.CooldownReason),
			Models:         []geminiFreeKeyModelUsage{},
		}
		for _, used := range usage.Models {
			entry.Models = append(entry.Models, geminiFreeKeyModelUsage{
				Model:          used.Model,
				RPMUsed:        used.RPMUsed,
				RPMLimit:       used.Limits.RPM,
				TPMUsed:        used.TPMUsed,
				TPMLimit:       used.Limits.TPM,
				RPDUsed:        used.RPDUsed,
				RPDLimit:       used.Limits.RPD,
				CooldownUntil:  unix(used.CooldownUntil),
				CooldownReason: string(used.CooldownReason),
			})
		}
		response.Keys = append(response.Keys, entry)
	}
	common.ApiSuccess(c, response)
}
