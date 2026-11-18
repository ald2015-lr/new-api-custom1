package controller

import (
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// UpdateInviteAbuseOptions saves the invite abuse rule's options that one
// save of the settings form changed, all of them or none
// (model.UpdateOptionsBulk). Saved one option per request, a save that
// stopped half-way (a failed request, a closed tab) left the rule with some
// new values and some old ones, and once that had settled the rule disabled
// accounts that neither the old nor the new settings call abusive. Other
// options are refused: PUT /api/option/ checks some of them before saving.
func UpdateInviteAbuseOptions(c *gin.Context) {
	var request struct {
		Options map[string]any `json:"options"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.Options) == 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	values := make(map[string]string, len(request.Options))
	for key, value := range request.Options {
		if !slices.Contains(operation_setting.InviteAbuseBanOptionKeys, key) {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
			return
		}
		values[key] = common.Interface2String(value)
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		common.ApiError(c, err)
		return
	}
	// Like PUT /api/option/: the name of each saved option, not its value.
	for _, key := range slices.Sorted(maps.Keys(values)) {
		recordManageAudit(c, "option.update", map[string]any{"key": key})
	}
	common.ApiSuccess(c, nil)
}

// GetInviteAbusePreview reports what the invite abuse rule would disable now,
// whether or not it is enabled. The optional threshold, min_redeem_quota and
// exempt_paid_inviter query parameters (validated like the options) replace
// the saved settings, so the settings form can preview values it has not
// saved yet.
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
	if raw := c.Query("exempt_paid_inviter"); raw != "" {
		if err := operation_setting.ValidateBonusOption(operation_setting.InviteAbuseBanExemptPaidInviterKey, raw); err != nil {
			common.ApiError(c, err)
			return
		}
		settings.ExemptPaidInviter = raw == "true"
	}
	preview, err := model.PreviewInviteAbuse(settings)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}
