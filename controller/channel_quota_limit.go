package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type channelQuotaLimitResponse struct {
	model.ChannelQuotaLimit
	CurrentUsed int64 `json:"current_used"`
	Exhausted   bool  `json:"exhausted"`
}

func newChannelQuotaLimitResponse(limit *model.ChannelQuotaLimit, now time.Time) *channelQuotaLimitResponse {
	if limit == nil {
		return nil
	}
	return &channelQuotaLimitResponse{
		ChannelQuotaLimit: *limit,
		CurrentUsed:       limit.CurrentUsed(now),
		Exhausted:         limit.Exhausted(now),
	}
}

// GetChannelQuotaLimits lists every channel quota limit with its current usage.
func GetChannelQuotaLimits(c *gin.Context) {
	limits, err := model.GetChannelQuotaLimits()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	now := time.Now()
	items := make([]*channelQuotaLimitResponse, 0, len(limits))
	for i := range limits {
		items = append(items, newChannelQuotaLimitResponse(&limits[i], now))
	}
	common.ApiSuccess(c, items)
}

type updateChannelQuotaLimitRequest struct {
	LimitQuota int64  `json:"limit_quota"`
	DailyReset bool   `json:"daily_reset"`
	Message    string `json:"message"`
}

func channelQuotaLimitChannelId(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "无效的渠道 ID")
		return 0, false
	}
	if _, err := model.GetChannelById(id, false); err != nil {
		common.ApiErrorMsg(c, "渠道不存在")
		return 0, false
	}
	return id, true
}

// UpdateChannelQuotaLimit sets, changes or (with limit_quota 0) removes a
// channel's quota limit. Usage counted so far is kept.
func UpdateChannelQuotaLimit(c *gin.Context) {
	id, ok := channelQuotaLimitChannelId(c)
	if !ok {
		return
	}
	var req updateChannelQuotaLimitRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.LimitQuota < 0 || req.LimitQuota > int64(common.MaxWalletQuota) {
		common.ApiErrorMsg(c, "限额必须是 0 到最大额度之间的数")
		return
	}
	if utf8.RuneCountInString(req.Message) > model.ChannelQuotaMessageMaxLength {
		common.ApiErrorMsg(c, fmt.Sprintf("自定义报错不能超过 %d 个字符", model.ChannelQuotaMessageMaxLength))
		return
	}
	limit, err := model.SetChannelQuotaLimit(id, req.LimitQuota, req.DailyReset, req.Message)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, newChannelQuotaLimitResponse(limit, time.Now()))
}

// ResetChannelQuotaLimit sets a channel's counted usage back to zero.
func ResetChannelQuotaLimit(c *gin.Context) {
	id, ok := channelQuotaLimitChannelId(c)
	if !ok {
		return
	}
	limit, err := model.ResetChannelQuotaLimitUsage(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiErrorMsg(c, "该渠道没有设置限额")
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, newChannelQuotaLimitResponse(limit, time.Now()))
}
