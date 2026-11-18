package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/gin-gonic/gin"
)

// LogTypeModeration marks a request that content moderation rejected on an
// OpenAI channel. Fork-specific: the value stays clear of upstream's 0-7 and
// must never be renumbered (stored rows keep it).
const LogTypeModeration = 100

// ModerationLogParams are the columns of a content moderation log row.
type ModerationLogParams struct {
	UserId         int
	ChannelId      int    // the moderated channel of the attempt
	ModelName      string // the model the user asked for
	TokenName      string
	TokenId        int
	Group          string
	IsStream       bool
	UseTimeSeconds int
	Content        *common.Message
	Other          *LogOther
}

// RecordModerationLog stores a request that content moderation rejected. It
// is written whatever ERROR_LOG_ENABLED and LogConsumeEnabled say: the
// channel's moderation switch is the gate.
func RecordModerationLog(c *gin.Context, params ModerationLogParams) {
	other := params.Other
	if other == nil {
		other = NewLogOther()
	}
	recordIP := false
	if settings, err := GetUserSetting(params.UserId, false); err == nil {
		recordIP = settings.RecordIpLog
	}
	ip := ""
	if recordIP {
		ip = c.ClientIP()
	}
	log := &Log{
		UserId:    params.UserId,
		Username:  c.GetString("username"),
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeModeration,
		Content:   other.setContent([]*common.Message{params.Content}),
		TokenName: params.TokenName,
		ModelName: params.ModelName,
		UseTime:   params.UseTimeSeconds,
		IsStream:  params.IsStream,
		ChannelId: params.ChannelId,
		TokenId:   params.TokenId,
		Group:     params.Group,
		Ip:        ip,
		RequestId: c.GetString(common.RequestIdKey),
	}
	log.Other = other.JSONString()
	if err := createLog(log); err != nil {
		logger.LogError(c, common.LogText("failed to record log: %s", err.Error()))
	}
}
