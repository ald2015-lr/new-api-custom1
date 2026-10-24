package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// GetSelfBonus returns the caller's check-in and invite reward settings, the
// active expiring rewards in the wallet and the invitation balance, and the
// invites still waiting for the recharge threshold. It changes nothing.
func GetSelfBonus(c *gin.Context) {
	summary, err := model.GetUserBonusSummary(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, summary)
}
