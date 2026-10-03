package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newChannelQuotaSelectContext(t *testing.T, tokenGroup string) (*gin.Context, *RetryParam) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, tokenGroup)
	retry := 0
	return ctx, &RetryParam{Ctx: ctx, TokenGroup: tokenGroup, ModelName: "quota-limit-model", RequestPath: "/v1/chat/completions", Retry: &retry}
}

func TestChannelQuotaLimitSkipsExhaustedChannelsAndReportsCustomMessage(t *testing.T) {
	require.NoError(t, i18n.Init())
	db := setupChannelSelectAutoGroupsTest(t)
	require.NoError(t, db.AutoMigrate(&model.ChannelQuotaLimit{}))
	const modelName = "quota-limit-model"
	createChannelSelectAutoGroupsChannel(t, db, 2201, "default", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2202, "default", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2203, "vip", modelName)
	model.InitChannelCache()
	t.Cleanup(func() {
		for _, id := range []int{2201, 2202, 2203} {
			_, _ = model.SetChannelQuotaLimit(id, 0, 0, false, "")
		}
	})

	// Usage is counted at settlement; 600 + 600 reaches the 1000 limit.
	_, err := model.SetChannelQuotaLimit(2201, 1000, 0, false, "")
	require.NoError(t, err)
	model.UpdateChannelUsedQuota(2201, 600)
	exhausted, _ := model.ChannelQuotaExhaustion(2201)
	assert.False(t, exhausted)
	model.UpdateChannelUsedQuota(2201, 600)
	exhausted, _ = model.ChannelQuotaExhaustion(2201)
	require.True(t, exhausted)

	for range 20 {
		ctx, param := newChannelQuotaSelectContext(t, "default")
		channel, _, selectErr := SelectChannelForRequest(ctx, modelName, param)
		require.Nil(t, selectErr)
		assert.Equal(t, 2202, channel.Id, "the exhausted channel is skipped")
	}

	// Once every channel of the group is exhausted the custom message is returned.
	_, err = model.SetChannelQuotaLimit(2202, 100, 0, false, "该分组已达限额，请切换为其它分组")
	require.NoError(t, err)
	model.UpdateChannelUsedQuota(2202, 100)
	ctx, param := newChannelQuotaSelectContext(t, "default")
	_, _, selectErr := SelectChannelForRequest(ctx, modelName, param)
	require.NotNil(t, selectErr)
	assert.Equal(t, http.StatusTooManyRequests, selectErr.StatusCode)
	assert.Equal(t, ChannelQuotaExhaustedErrorCode, selectErr.Code)
	assert.Equal(t, "该分组已达限额，请切换为其它分组", selectErr.Message)

	// Auto tokens move on to the next group; with nothing left the default text is used.
	ctx, param = newChannelQuotaSelectContext(t, "auto")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"default", "vip"})
	channel, group, selectErr := SelectChannelForRequest(ctx, modelName, param)
	require.Nil(t, selectErr)
	assert.Equal(t, 2203, channel.Id)
	assert.Equal(t, "vip", group)
	_, err = model.SetChannelQuotaLimit(2203, 50, 0, true, "")
	require.NoError(t, err)
	model.UpdateChannelUsedQuota(2203, 80)
	ctx, param = newChannelQuotaSelectContext(t, "auto")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"default", "vip"})
	_, _, selectErr = SelectChannelForRequest(ctx, modelName, param)
	require.NotNil(t, selectErr)
	assert.Equal(t, http.StatusTooManyRequests, selectErr.StatusCode)
	assert.Equal(t, "该分组已达限额，请切换为其它分组", selectErr.Message, "the first group's custom message is kept")
	// A group whose exhausted channels have no custom message uses the default text.
	ctx, param = newChannelQuotaSelectContext(t, "vip")
	_, _, selectErr = SelectChannelForRequest(ctx, modelName, param)
	require.NotNil(t, selectErr)
	assert.Empty(t, selectErr.Message)
	assert.Equal(t, i18n.MsgChannelQuotaExhausted, selectErr.MessageID)
	assert.Equal(t, "该分组已达限额，请切换为其它分组", i18n.Translate("zh-CN", selectErr.MessageID))

	// A refund lowers the counted usage; removing the limit makes the channel usable.
	model.UpdateChannelUsedQuota(2201, -300)
	exhausted, _ = model.ChannelQuotaExhaustion(2201)
	assert.False(t, exhausted)
	_, err = model.SetChannelQuotaLimit(2202, 0, 0, false, "")
	require.NoError(t, err)
	exhausted, _ = model.ChannelQuotaExhaustion(2202)
	assert.False(t, exhausted)
}

func TestChannelQuotaLimitDailyResetAndManualReset(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	require.NoError(t, db.AutoMigrate(&model.ChannelQuotaLimit{}))
	createChannelSelectAutoGroupsChannel(t, db, 2301, "default", "quota-limit-model")
	createChannelSelectAutoGroupsChannel(t, db, 2302, "default", "quota-limit-model")
	t.Cleanup(func() {
		_, _ = model.SetChannelQuotaLimit(2301, 0, 0, false, "")
		_, _ = model.SetChannelQuotaLimit(2302, 0, 0, false, "")
	})

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour).Unix()
	daily := model.ChannelQuotaLimit{LimitQuota: 1000, DailyReset: true, UsedQuota: 1000, PeriodStart: yesterday}
	assert.Zero(t, daily.CurrentUsed(now), "yesterday's usage does not count today")
	assert.False(t, daily.Exhausted(now))
	total := daily
	total.DailyReset = false
	assert.True(t, total.Exhausted(now), "without daily reset usage keeps counting")

	_, err := model.SetChannelQuotaLimit(2301, 1000, 0, true, "")
	require.NoError(t, err)
	model.UpdateChannelUsedQuota(2301, 1000)
	exhausted, _ := model.ChannelQuotaExhaustion(2301)
	require.True(t, exhausted)
	// Pretend the usage was recorded yesterday: the next charge starts a new day.
	require.NoError(t, db.Model(&model.ChannelQuotaLimit{}).Where("channel_id = ?", 2301).Update("period_start", yesterday).Error)
	model.UpdateChannelUsedQuota(2301, 10)
	limits, err := model.GetChannelQuotaLimits()
	require.NoError(t, err)
	require.Len(t, limits, 1)
	assert.EqualValues(t, 10, limits[0].UsedQuota)
	exhausted, _ = model.ChannelQuotaExhaustion(2301)
	assert.False(t, exhausted)

	_, err = model.SetChannelQuotaLimit(2302, 100, 0, false, "")
	require.NoError(t, err)
	model.UpdateChannelUsedQuota(2302, 150)
	exhausted, _ = model.ChannelQuotaExhaustion(2302)
	require.True(t, exhausted)
	reset, err := model.ResetChannelQuotaLimitUsage(2302)
	require.NoError(t, err)
	assert.Zero(t, reset.UsedQuota)
	exhausted, _ = model.ChannelQuotaExhaustion(2302)
	assert.False(t, exhausted)
}

func TestChannelQuotaLimitByRequestCount(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	require.NoError(t, db.AutoMigrate(&model.ChannelQuotaLimit{}))
	createChannelSelectAutoGroupsChannel(t, db, 2401, "default", "quota-limit-model")
	t.Cleanup(func() { _, _ = model.SetChannelQuotaLimit(2401, 0, 0, false, "") })

	// Only a request limit: two billed requests exhaust it, whatever they cost.
	limit, err := model.SetChannelQuotaLimit(2401, 0, 2, false, "次数已用完")
	require.NoError(t, err)
	require.NotNil(t, limit, "a request-only limit is kept")
	model.UpdateChannelUsedQuotaForRequest(2401, 10)
	model.UpdateChannelUsedQuota(2401, 500) // a settlement adjustment is not a request
	exhausted, _ := model.ChannelQuotaExhaustion(2401)
	assert.False(t, exhausted)
	model.UpdateChannelUsedQuotaForRequest(2401, 0) // zero-cost requests still count
	exhausted, message := model.ChannelQuotaExhaustion(2401)
	require.True(t, exhausted)
	assert.Equal(t, "次数已用完", message)

	// A refunded request gives its count back.
	model.UpdateChannelUsedQuotaForRequestRefund(2401, -10)
	exhausted, _ = model.ChannelQuotaExhaustion(2401)
	assert.False(t, exhausted)

	// Both limits: whichever is reached first stops the channel.
	_, err = model.SetChannelQuotaLimit(2401, 400, 100, true, "")
	require.NoError(t, err)
	exhausted, _ = model.ChannelQuotaExhaustion(2401)
	assert.True(t, exhausted, "500 quota already used reaches the 400 quota limit")

	// Daily reset and manual reset clear the request count too.
	now := time.Now()
	stale := model.ChannelQuotaLimit{LimitCount: 1, UsedCount: 5, DailyReset: true, PeriodStart: now.Add(-24 * time.Hour).Unix()}
	assert.Zero(t, stale.CurrentUsedCount(now))
	assert.False(t, stale.Exhausted(now))
	reset, err := model.ResetChannelQuotaLimitUsage(2401)
	require.NoError(t, err)
	assert.Zero(t, reset.UsedCount)
	assert.Zero(t, reset.UsedQuota)
}
