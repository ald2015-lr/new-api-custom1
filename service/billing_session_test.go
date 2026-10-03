package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A subscription_first user whose subscription cannot cover the request falls back to
// the wallet according to the plan's current allow_wallet_overflow setting, even when
// the subscription row's snapshot was persisted as false by earlier full-row saves.
func TestNewBillingSessionSubscriptionFirstWalletOverflowFollowsPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		walletQuota  = 1_000_000
		subTotal     = int64(1_000_000)
		subUsed      = int64(997_000)
		requestQuota = 5000
	)
	trialEnd := time.Date(3025, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()

	cases := []struct {
		name          string
		planAllows    bool
		expectFunding bool
	}{
		{name: "plan allows wallet overflow", planAllows: true, expectFunding: true},
		{name: "plan disallows wallet overflow", planAllows: false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			userId := 810 + i
			planId := 820 + i
			subId := 830 + i
			seedUser(t, userId, walletQuota)
			require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
				Id:                  planId,
				Title:               "Trial",
				DurationUnit:        model.SubscriptionDurationYear,
				DurationValue:       1000,
				TotalAmount:         subTotal,
				AllowWalletOverflow: common.GetPointer(tc.planAllows),
			}).Error)
			model.InvalidateSubscriptionPlanCache(planId)
			t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(planId) })
			require.NoError(t, model.DB.Create(&model.UserSubscription{
				Id:          subId,
				UserId:      userId,
				PlanId:      planId,
				AmountTotal: subTotal,
				AmountUsed:  subUsed,
				StartTime:   time.Now().Unix(),
				EndTime:     trialEnd,
				Status:      "active",
				// Legacy NULL snapshot already written back as false before the fix.
				AllowWalletOverflow: false,
			}).Error)

			relayInfo := &relaycommon.RelayInfo{
				UserId:          userId,
				RequestId:       fmt.Sprintf("req-wallet-overflow-%d", i),
				IsPlayground:    true,
				ForcePreConsume: true,
				OriginModelName: "gpt-test",
				UserSetting:     dto.UserSetting{BillingPreference: "subscription_first"},
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

			session, apiErr := NewBillingSession(ctx, relayInfo, requestQuota)

			assert.Equal(t, subUsed, getSubscriptionUsed(t, subId))
			if tc.expectFunding {
				require.Nil(t, apiErr)
				require.NotNil(t, session)
				assert.Equal(t, BillingSourceWallet, relayInfo.BillingSource)
				assert.Equal(t, requestQuota, relayInfo.FinalPreConsumedQuota)
				assert.Equal(t, walletQuota-requestQuota, getUserQuota(t, userId))
				return
			}
			require.NotNil(t, apiErr)
			assert.Nil(t, session)
			assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
			assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.False(t, types.IsRecordErrorLog(apiErr))
			assert.Equal(t, "订阅额度不足，且当前订阅套餐不允许额度用尽后使用钱包余额: subscription quota insufficient, need=5000", apiErr.Error())
			assert.Equal(t, walletQuota, getUserQuota(t, userId))
		})
	}
}

// Settling more than the subscription has left charges the subscription up to its
// total and, when the plan allows wallet overflow, the rest to the wallet. Before,
// the whole settlement failed and the overage was never charged.
func TestSubscriptionSettleOverageGoesToWalletWhenPlanAllows(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		walletQuota = 1_000_000
		subTotal    = int64(1_000_000)
		subUsed     = int64(997_000) // 3000 left
		preConsume  = 2000
		actual      = 5000
	)
	cases := []struct {
		name        string
		planAllows  bool
		wantWallet  int
		wantOverage int64
	}{
		{name: "plan allows wallet overflow", planAllows: true, wantWallet: walletQuota - 2000, wantOverage: 2000},
		{name: "plan disallows wallet overflow", planAllows: false, wantWallet: walletQuota},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			userId, planId, subId := 840+i, 850+i, 860+i
			seedUser(t, userId, walletQuota)
			require.NoError(t, model.DB.Create(&model.SubscriptionPlan{
				Id: planId, Title: "Monthly", DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
				TotalAmount: subTotal, AllowWalletOverflow: common.GetPointer(tc.planAllows),
			}).Error)
			model.InvalidateSubscriptionPlanCache(planId)
			t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(planId) })
			require.NoError(t, model.DB.Create(&model.UserSubscription{
				Id: subId, UserId: userId, PlanId: planId, AmountTotal: subTotal, AmountUsed: subUsed,
				StartTime: time.Now().Unix(), EndTime: time.Now().Add(24 * time.Hour).Unix(), Status: "active",
				AllowWalletOverflow: tc.planAllows,
			}).Error)

			relayInfo := &relaycommon.RelayInfo{
				UserId:          userId,
				RequestId:       fmt.Sprintf("req-settle-overage-%d", i),
				IsPlayground:    true,
				ForcePreConsume: true,
				OriginModelName: "gpt-test",
				UserSetting:     dto.UserSetting{BillingPreference: "subscription_first"},
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			session, apiErr := NewBillingSession(ctx, relayInfo, preConsume)
			require.Nil(t, apiErr)
			require.Equal(t, BillingSourceSubscription, relayInfo.BillingSource)

			require.NoError(t, session.Settle(actual))

			assert.Equal(t, subTotal, getSubscriptionUsed(t, subId), "subscription is used up to its total")
			assert.Equal(t, tc.wantWallet, getUserQuota(t, userId))
			assert.Equal(t, int64(1000), relayInfo.SubscriptionPostDelta, "only the part the subscription could cover")
			assert.Equal(t, tc.wantOverage, relayInfo.SubscriptionWalletOverflow)
			assert.False(t, session.NeedsRefund(), "a settled session must not be refunded")
		})
	}
}
