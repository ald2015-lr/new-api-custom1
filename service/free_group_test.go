package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useFreeGroups(t *testing.T, value string) {
	t.Helper()
	previous := operation_setting.GetFreeGroups()
	require.NoError(t, operation_setting.ValidateGroupBillingOption(operation_setting.FreeGroupsOptionKey, value))
	operation_setting.LoadGroupBillingOption(operation_setting.FreeGroupsOptionKey, value)
	t.Cleanup(func() {
		encoded, err := common.Marshal(previous)
		require.NoError(t, err)
		operation_setting.LoadGroupBillingOption(operation_setting.FreeGroupsOptionKey, string(encoded))
	})
}

func TestParseFreeGroups(t *testing.T) {
	groups, err := operation_setting.ParseFreeGroups(`[" welfare ","vip","welfare"]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "welfare"}, groups)
	for _, invalid := range []string{`["auto"]`, `[""]`, `{"welfare":true}`, `[1]`} {
		_, err := operation_setting.ParseFreeGroups(invalid)
		assert.Error(t, err, invalid)
	}
	empty, err := operation_setting.ParseFreeGroups("")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// Free groups never charge the wallet, subscription or token, whatever the
// billing preference, while the real cost is still reported to the caller.
func TestFreeGroupDoesNotChargeUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useFreeGroups(t, `["welfare"]`)

	cases := []struct {
		name       string
		preference string
	}{
		{name: "wallet", preference: "wallet_first"},
		{name: "subscription first", preference: "subscription_first"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			userId, tokenId, subId := 870+i, 880+i, 890+i
			seedUser(t, userId, 100_000)
			seedToken(t, tokenId, userId, fmt.Sprintf("free-group-key-%d", i), 50_000)
			seedSubscription(t, subId, userId, 1_000_000, 0)
			relayInfo := &relaycommon.RelayInfo{
				UserId:          userId,
				TokenId:         tokenId,
				TokenKey:        fmt.Sprintf("free-group-key-%d", i),
				UsingGroup:      "welfare",
				RequestId:       fmt.Sprintf("req-free-group-%d", i),
				OriginModelName: "gpt-test",
				UserSetting:     dto.UserSetting{BillingPreference: tc.preference},
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

			require.Nil(t, PreConsumeBilling(ctx, 5000, relayInfo))
			assert.Nil(t, relayInfo.Billing, "nothing is reserved for a free group")
			assert.Equal(t, BillingSourceFreeGroup, relayInfo.BillingSource)
			require.NoError(t, SettleBilling(ctx, relayInfo, 7000))
			require.NoError(t, PostConsumeQuota(relayInfo, 300, 0, false))

			assert.Equal(t, 100_000, getUserQuota(t, userId))
			assert.Equal(t, 50_000, getTokenRemainQuota(t, tokenId))
			assert.Zero(t, getSubscriptionUsed(t, subId))
		})
	}
}

// An auto request that reserved on a paid group and finished on a free group
// gets its reservation back.
func TestFreeGroupRefundsReservationFromEarlierPaidGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useFreeGroups(t, `["welfare"]`)
	truncate(t)
	seedUser(t, 895, 100_000)
	relayInfo := &relaycommon.RelayInfo{
		UserId:          895,
		UsingGroup:      "default",
		RequestId:       "req-free-group-switch",
		IsPlayground:    true,
		ForcePreConsume: true,
		OriginModelName: "gpt-test",
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, PreConsumeBilling(ctx, 5000, relayInfo))
	require.NotNil(t, relayInfo.Billing)
	assert.Equal(t, 95_000, getUserQuota(t, 895))

	relayInfo.UsingGroup = "welfare"
	require.NoError(t, SettleBilling(ctx, relayInfo, 7000))

	assert.Equal(t, 100_000, getUserQuota(t, 895))
	assert.Equal(t, BillingSourceFreeGroup, relayInfo.BillingSource)
}

func TestFreeGroupTaskAdjustmentsSkipUser(t *testing.T) {
	truncate(t)
	seedUser(t, 896, 100_000)
	seedToken(t, 897, 896, "free-group-task-key", 50_000)
	task := &model.Task{UserId: 896, Quota: 4000}
	task.PrivateData.BillingSource = BillingSourceFreeGroup
	task.PrivateData.TokenId = 897

	require.NoError(t, taskAdjustFunding(task, 2000))
	require.NoError(t, taskAdjustFunding(task, -4000))
	taskAdjustTokenQuota(context.Background(), task, 2000)

	assert.Equal(t, 100_000, getUserQuota(t, 896))
	assert.Equal(t, 50_000, getTokenRemainQuota(t, 897))
}
