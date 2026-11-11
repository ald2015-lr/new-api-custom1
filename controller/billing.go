package controller

import (
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetSubscription(c *gin.Context) {
	var remainQuota int
	var usedQuota int
	var err error
	var token *model.Token
	var expiredTime int64
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		expiredTime = token.ExpiredTime
		remainQuota = token.RemainQuota
		usedQuota = token.UsedQuota
	} else {
		userId := c.GetInt("id")
		remainQuota, err = model.GetUserQuota(userId, false)
		usedQuota, err = model.GetUserUsedQuota(userId)
	}
	if expiredTime <= 0 {
		expiredTime = 0
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "upstream_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	quota := remainQuota + usedQuota
	amount := float64(quota)
	// OpenAI 兼容接口中的 *_USD 字段含义保持“额度单位”对应值：
	// 我们将其解释为以“站点展示类型”为准：
	// - USD: 直接除以 QuotaPerUnit
	// - CNY: 先转 USD 再乘汇率
	// - TOKENS: 直接使用 tokens 数量
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// amount 保持 tokens 数值
	default:
		amount = amount / common.QuotaPerUnit
	}
	if token != nil && token.UnlimitedQuota {
		amount = 100000000
	}
	subscription := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	}
	c.JSON(200, subscription)
	return
}

func GetUsage(c *gin.Context) {
	var quota int
	var err error
	var token *model.Token
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		quota = token.UsedQuota
	} else {
		userId := c.GetInt("id")
		quota, err = model.GetUserUsedQuota(userId)
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	amount := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// tokens 保持原值
	default:
		amount = amount / common.QuotaPerUnit
	}
	usage := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: amount * 100,
	}
	c.JSON(200, usage)
	return
}

// GetNovelAISubscription answers NovelAI's GET /user/subscription for the
// calling token without asking any upstream; see novelAISubscription.
func GetNovelAISubscription(c *gin.Context) {
	token, ok := novelAICallingToken(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, novelAISubscription(c, token))
}

// GetNovelAIUserData answers NovelAI's GET /user/data, the account summary
// clients load at sign-in, around the same subscription.
func GetNovelAIUserData(c *gin.Context) {
	token, ok := novelAICallingToken(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"priority":     gin.H{"maxPriorityActions": 1000, "nextRefillAt": 0, "taskPriority": 10},
		"subscription": novelAISubscription(c, token),
		"keystore":     nil,
		"settings":     "{}",
		"information": gin.H{
			"emailVerified":               true,
			"emailVerificationLetterSent": false,
			"trialActivated":              false,
			"trialActionsLeft":            0,
			"trialImagesLeft":             0,
			"accountCreatedAt":            token.CreatedTime,
		},
	})
}

// novelAICallingToken loads the token TokenAuth accepted, for its expiry and
// creation time, and answers with a NovelAI-shaped error when it cannot.
func novelAICallingToken(c *gin.Context) (*model.Token, bool) {
	token, err := model.GetTokenByKey(c.GetString("token_key"), false)
	if err != nil {
		logger.LogError(c, common.LogText("NovelAI account request: failed to load token %d: %s", c.GetInt("token_id"), err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"statusCode": http.StatusInternalServerError, "message": i18n.T(c, i18n.MsgTokenGetInfoFailed)})
		return nil, false
	}
	return token, true
}

// novelAISubscription describes the calling token as a NovelAI Opus (tier 3)
// subscription with unlimited image generation; new-api bills each image
// itself. The Anlas balance (trainingStepsLeft.fixedTrainingStepsLeft) is the
// quota the token can still spend at 1 Anlas = 0.01 USD: the token's remaining
// quota capped by the wallet, or the wallet alone for an unlimited token,
// rounded down and saturated at the int32 bound. A token that never expires
// reports the int32 maximum as its expiry.
func novelAISubscription(c *gin.Context, token *model.Token) gin.H {
	remaining := common.GetContextKeyInt(c, constant.ContextKeyUserQuota)
	if !token.UnlimitedQuota {
		remaining = min(remaining, token.RemainQuota)
	}
	anlas := common.QuotaFromFloat(float64(max(remaining, 0)) * 100 / common.QuotaPerUnit)
	expiresAt := int64(math.MaxInt32)
	if token.ExpiredTime > 0 {
		expiresAt = token.ExpiredTime
	}
	return gin.H{
		"tier":      3,
		"active":    true,
		"expiresAt": expiresAt,
		"perks": gin.H{
			"maxPriorityActions":             1000,
			"startPriority":                  10,
			"moduleTrainingSteps":            0,
			"unlimitedMaxPriority":           true,
			"voiceGeneration":                false,
			"imageGeneration":                true,
			"unlimitedImageGeneration":       true,
			"unlimitedImageGenerationLimits": []any{},
			"contextTokens":                  8192,
		},
		"paymentProcessorData": nil,
		"trainingStepsLeft": gin.H{
			"fixedTrainingStepsLeft": anlas,
			"purchasedTrainingSteps": 0,
		},
		"accountType": 0,
	}
}
