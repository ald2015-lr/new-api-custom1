package controller

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// GetCheckinStatus 获取用户签到状态和历史记录
func GetCheckinStatus(c *gin.Context) {
	setting := operation_setting.GetCheckinSetting()
	if !setting.Enabled {
		common.ApiErrorT(c, "Check-in feature is not enabled")
		return
	}
	userId := c.GetInt("id")
	// 获取月份参数，默认为当前月份
	month := c.DefaultQuery("month", time.Now().Format("2006-01"))

	stats, err := model.GetUserCheckinStats(userId, month)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recharged, err := model.GetUserRechargeQuota(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled":                 setting.Enabled,
			"min_quota":               setting.MinQuota,
			"max_quota":               setting.MaxQuota,
			"paid_threshold_quota":    setting.PaidThresholdQuota,
			"paid_multiplier":         setting.PaidMultiplier,
			"free_expire_days":        setting.FreeExpireDays,
			"paid_expire_days":        setting.PaidExpireDays,
			"expire_only_when_unused": setting.ExpireOnlyWhenUnused,
			"recharged_quota":         recharged,
			"is_paid_user":            operation_setting.IsPaidCheckinUser(recharged),
			"stats":                   stats,
		},
	})
}

// DoCheckin 执行用户签到
func DoCheckin(c *gin.Context) {
	setting := operation_setting.GetCheckinSetting()
	if !setting.Enabled {
		common.ApiErrorT(c, "Check-in feature is not enabled")
		return
	}

	userId := c.GetInt("id")

	result, err := model.UserCheckin(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	model.RecordLog(userId, model.LogTypeSystem, result.LogMessage())
	common.ApiSuccessT(c, "Check-in successful", gin.H{
		"quota_awarded":           result.Checkin.QuotaAwarded,
		"base_quota":              result.BaseQuota,
		"checkin_date":            result.Checkin.CheckinDate,
		"paid_multiplier_applied": result.PaidMultiplierApplied,
		"paid_multiplier":         result.PaidMultiplier,
		"is_paid_user":            result.IsPaidUser,
		"expire_days":             result.ExpireDays,
		"expires_at":              result.ExpiresAt,
		"expire_only_when_unused": result.ExpireOnlyWhenUnused,
	})
}
