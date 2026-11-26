package controller

import (
	"maps"
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// UpdateModelSquareOptions saves the Model Square display options that one
// save of the settings card changed, all of them or none
// (model.UpdateOptionsBulk). Saved one option per request, the card showed a
// toast per option and the refetched system options reset the switches not
// saved yet. Other options are refused: PUT /api/option/ checks some of them
// before saving.
func UpdateModelSquareOptions(c *gin.Context) {
	var request struct {
		Options map[string]any `json:"options"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.Options) == 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	values := make(map[string]string, len(request.Options))
	for key, value := range request.Options {
		if !operation_setting.IsModelSquareOptionKey(key) {
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
