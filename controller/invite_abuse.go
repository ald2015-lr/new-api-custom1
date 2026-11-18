package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// GetInviteAbusePreview reports what the invite abuse rule would disable now,
// whether or not it is enabled. The optional threshold and min_redeem_quota
// query parameters (validated like the options) replace the saved settings,
// so the settings form can preview values it has not saved yet.
func GetInviteAbusePreview(c *gin.Context) {
	settings := model.CurrentInviteAbuseSettings()
	if raw := c.Query("threshold"); raw != "" {
		if err := operation_setting.ValidateBonusOption(operation_setting.InviteAbuseBanThresholdKey, raw); err != nil {
			common.ApiError(c, err)
			return
		}
		settings.Threshold, _ = strconv.Atoi(raw)
	}
	if raw := c.Query("min_redeem_quota"); raw != "" {
		if err := operation_setting.ValidateBonusOption(operation_setting.InviteAbuseBanMinRedeemQuotaKey, raw); err != nil {
			common.ApiError(c, err)
			return
		}
		settings.MinRedeemQuota, _ = strconv.ParseInt(raw, 10, 64)
	}
	preview, err := model.PreviewInviteAbuse(settings)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}
