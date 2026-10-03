package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedSubscriptionResetPlan(t *testing.T, plan *SubscriptionPlan) {
	t.Helper()
	require.NoError(t, DB.Create(plan).Error)
}

func seedSubscriptionResetSub(t *testing.T, sub *UserSubscription) {
	t.Helper()
	require.NoError(t, DB.Create(sub).Error)
}

func getSubscriptionResetSub(t *testing.T, id int) UserSubscription {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", id).First(&sub).Error)
	return sub
}

func TestAdminResetUserSubscriptionsByPlanResetsAllActiveMatchesAndAdvancesTime(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9101,
		Title:            "Pro",
		PriceAmount:      10,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      1000,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	otherPlan := &SubscriptionPlan{
		Id:               9102,
		Title:            "Basic",
		PriceAmount:      1,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      100,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetPlan(t, otherPlan)

	activeEnd := now + 30*24*3600
	expiredEnd := now - 1
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9201, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 300, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9202, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9203, UserId: 101, PlanId: otherPlan.Id, AmountTotal: 100, AmountUsed: 60, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9204, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 700, StartTime: now - 7200, EndTime: expiredEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9205, UserId: 102, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 800, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9206, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 900, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 120})

	beforeReset := GetDBTimestamp()
	result, err := AdminResetUserSubscriptionsByPlan(101, plan.Id, true)
	afterReset := GetDBTimestamp()

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, plan.Id, result.PlanId)
	assert.Equal(t, 2, result.MatchedCount)
	assert.Equal(t, 2, result.ResetCount)
	assert.Equal(t, 1, result.UserCount)
	assert.Equal(t, []int{101}, result.AffectedUserIds)
	assert.True(t, result.AdvanceResetTime)

	for _, id := range []int{9201, 9202} {
		sub := getSubscriptionResetSub(t, id)
		assert.Zero(t, sub.AmountUsed)
		assert.GreaterOrEqual(t, sub.LastResetTime, beforeReset)
		assert.LessOrEqual(t, sub.LastResetTime, afterReset)
		assert.Equal(t, calcNextResetTime(time.Unix(sub.LastResetTime, 0), plan, sub.EndTime), sub.NextResetTime)
	}
	assert.EqualValues(t, 60, getSubscriptionResetSub(t, 9203).AmountUsed)
	assert.EqualValues(t, 700, getSubscriptionResetSub(t, 9204).AmountUsed)
	assert.EqualValues(t, 800, getSubscriptionResetSub(t, 9205).AmountUsed)
	assert.EqualValues(t, 900, getSubscriptionResetSub(t, 9206).AmountUsed)
}

func TestAdminResetUserSubscriptionsByPlanKeepsResetTimes(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9301,
		Title:            "Team",
		PriceAmount:      20,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      2000,
		QuotaResetPeriod: SubscriptionResetMonthly,
	}
	seedSubscriptionResetPlan(t, plan)

	lastReset := now - 86400
	nextReset := now + 86400
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9302, UserId: 201, PlanId: plan.Id, AmountTotal: 2000, AmountUsed: 1200, StartTime: now - 172800, EndTime: now + 30*24*3600, Status: "active", LastResetTime: lastReset, NextResetTime: nextReset})

	result, err := AdminResetUserSubscriptionsByPlan(201, plan.Id, false)

	require.NoError(t, err)
	assert.False(t, result.AdvanceResetTime)
	sub := getSubscriptionResetSub(t, 9302)
	assert.Zero(t, sub.AmountUsed)
	assert.Equal(t, lastReset, sub.LastResetTime)
	assert.Equal(t, nextReset, sub.NextResetTime)
}

func TestAdminResetUserSubscriptionsByPlanNoActiveMatchReturnsError(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            9401,
		Title:         "Expired",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9402, UserId: 301, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 7200, EndTime: now - 1, Status: "active"})

	result, err := AdminResetUserSubscriptionsByPlan(301, plan.Id, true)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, strings.Contains(err.Error(), "该用户没有有效的此套餐订阅"))
}

func TestAdminResetPlanSubscriptionsResetsAllActiveUsers(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9501,
		Title:            "Business",
		PriceAmount:      30,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      3000,
		QuotaResetPeriod: SubscriptionResetNever,
	}
	seedSubscriptionResetPlan(t, plan)

	activeEnd := now + 30*24*3600
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9502, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1000, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9503, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1100, StartTime: now - 3500, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9504, UserId: 402, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1200, StartTime: now - 3400, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9505, UserId: 403, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1300, StartTime: now - 7200, EndTime: now - 1, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9506, UserId: 404, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1400, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 10})

	result, err := AdminResetPlanSubscriptions(plan.Id, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.MatchedCount)
	assert.Equal(t, 3, result.ResetCount)
	assert.Equal(t, 2, result.UserCount)
	assert.Equal(t, []int{401, 402}, result.AffectedUserIds)
	for _, id := range []int{9502, 9503, 9504} {
		sub := getSubscriptionResetSub(t, id)
		assert.Zero(t, sub.AmountUsed)
		assert.Zero(t, sub.LastResetTime)
		assert.Zero(t, sub.NextResetTime)
	}
	assert.EqualValues(t, 1300, getSubscriptionResetSub(t, 9505).AmountUsed)
	assert.EqualValues(t, 1400, getSubscriptionResetSub(t, 9506).AmountUsed)
}

func TestAdminResetPlanSubscriptionsNoMatchSucceeds(t *testing.T) {
	truncateTables(t)

	plan := &SubscriptionPlan{
		Id:            9601,
		Title:         "Empty",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, plan)

	result, err := AdminResetPlanSubscriptions(plan.Id, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.MatchedCount)
	assert.Zero(t, result.ResetCount)
	assert.Zero(t, result.UserCount)
	assert.Empty(t, result.AffectedUserIds)
}

func setSubscriptionWalletOverflowSnapshotNull(t *testing.T, id int) {
	t.Helper()
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", id).Update("allow_wallet_overflow", nil).Error)
	require.Nil(t, getSubscriptionWalletOverflowSnapshot(t, id))
}

func getSubscriptionWalletOverflowSnapshot(t *testing.T, id int) *bool {
	t.Helper()
	var row struct {
		AllowWalletOverflow *bool
	}
	require.NoError(t, DB.Model(&UserSubscription{}).Select("allow_wallet_overflow").Where("id = ?", id).Scan(&row).Error)
	return row.AllowWalletOverflow
}

// Rows created before allow_wallet_overflow existed hold NULL. Consumption and reset
// writes must leave the column untouched instead of persisting the bool zero value,
// which would silently turn legacy subscriptions strict.
func TestSubscriptionWritesKeepLegacyNullWalletOverflowSnapshot(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:                  9701,
		Title:               "Legacy",
		PriceAmount:         10,
		DurationUnit:        SubscriptionDurationMonth,
		DurationValue:       1,
		TotalAmount:         1000,
		QuotaResetPeriod:    SubscriptionResetDaily,
		AllowWalletOverflow: common.GetPointer(true),
	}
	seedSubscriptionResetPlan(t, plan)
	InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(plan.Id) })

	end := now + 30*24*3600
	// 9702 has no reset schedule yet, so pre-consume first initialises it and then consumes.
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9702, UserId: 701, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 100, StartTime: now, EndTime: end, Status: "active"})
	// 9703 is due for the periodic reset.
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9703, UserId: 702, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 400, StartTime: now - 3*86400, EndTime: end, Status: "active", LastResetTime: now - 2*86400, NextResetTime: now - 10})
	// 9704 is reset by an administrator.
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9704, UserId: 703, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now, EndTime: end, Status: "active", LastResetTime: now, NextResetTime: now + 86400})
	for _, id := range []int{9702, 9703, 9704} {
		setSubscriptionWalletOverflowSnapshotNull(t, id)
	}

	result, err := PreConsumeUserSubscription("req-legacy-null", 701, "gpt-test", 0, 200)
	require.NoError(t, err)
	assert.Equal(t, 9702, result.UserSubscriptionId)
	sub := getSubscriptionResetSub(t, 9702)
	assert.EqualValues(t, 300, sub.AmountUsed)
	assert.Positive(t, sub.NextResetTime)
	assert.Nil(t, getSubscriptionWalletOverflowSnapshot(t, 9702))

	require.NoError(t, PostConsumeUserSubscriptionDelta(9702, 50))
	assert.EqualValues(t, 350, getSubscriptionResetSub(t, 9702).AmountUsed)
	assert.Nil(t, getSubscriptionWalletOverflowSnapshot(t, 9702))

	resetCount, err := ResetDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, 1, resetCount)
	assert.Zero(t, getSubscriptionResetSub(t, 9703).AmountUsed)
	assert.Nil(t, getSubscriptionWalletOverflowSnapshot(t, 9703))

	_, err = AdminResetUserSubscriptionsByPlan(703, plan.Id, true)
	require.NoError(t, err)
	assert.Zero(t, getSubscriptionResetSub(t, 9704).AmountUsed)
	assert.Nil(t, getSubscriptionWalletOverflowSnapshot(t, 9704))
}

func TestUserActiveSubscriptionsAllowWalletOverflowFollowsLivePlan(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	strict := common.GetPointer(false)
	allowed := common.GetPointer(true)
	cases := []struct {
		name          string
		planExists    bool
		planSetting   *bool // nil stores NULL on the plan row
		snapshot      *bool // nil stores NULL on the subscription row
		inactiveOnly  bool
		expectAllowed bool
	}{
		{name: "plan allows over strict snapshot", planExists: true, planSetting: allowed, snapshot: strict, expectAllowed: true},
		{name: "plan unset over strict snapshot", planExists: true, planSetting: nil, snapshot: strict, expectAllowed: true},
		{name: "plan disallows over permissive snapshot", planExists: true, planSetting: strict, snapshot: allowed, expectAllowed: false},
		{name: "plan missing with NULL snapshot", snapshot: nil, expectAllowed: true},
		{name: "plan missing with strict snapshot", snapshot: strict, expectAllowed: false},
		{name: "only inactive strict subscriptions", planExists: true, planSetting: strict, snapshot: strict, inactiveOnly: true, expectAllowed: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userId := 710 + i
			planId := 9710 + i
			subId := 9720 + i
			if tc.planExists {
				seedSubscriptionResetPlan(t, &SubscriptionPlan{Id: planId, Title: tc.name, DurationUnit: SubscriptionDurationMonth, DurationValue: 1, AllowWalletOverflow: tc.planSetting})
				InvalidateSubscriptionPlanCache(planId)
				t.Cleanup(func() { InvalidateSubscriptionPlanCache(planId) })
			}
			sub := &UserSubscription{Id: subId, UserId: userId, PlanId: planId, AmountTotal: 1000, StartTime: now - 60, EndTime: now + 86400, Status: "active", AllowWalletOverflow: tc.snapshot != nil && *tc.snapshot}
			if tc.inactiveOnly {
				sub.Status = "cancelled"
			}
			seedSubscriptionResetSub(t, sub)
			if tc.snapshot == nil {
				setSubscriptionWalletOverflowSnapshotNull(t, subId)
			}

			got, err := UserActiveSubscriptionsAllowWalletOverflow(userId)
			require.NoError(t, err)
			assert.Equal(t, tc.expectAllowed, got)
		})
	}

	// An admin switching the plan to strict applies on the next request, even when the
	// plan is still held in the plan cache.
	userId, planId := 790, 9790
	seedSubscriptionResetPlan(t, &SubscriptionPlan{Id: planId, Title: "Toggle", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, AllowWalletOverflow: allowed})
	InvalidateSubscriptionPlanCache(planId)
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(planId) })
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9791, UserId: userId, PlanId: planId, AmountTotal: 1000, StartTime: now - 60, EndTime: now + 86400, Status: "active", AllowWalletOverflow: true})
	_, err := GetSubscriptionPlanById(planId)
	require.NoError(t, err)
	got, err := UserActiveSubscriptionsAllowWalletOverflow(userId)
	require.NoError(t, err)
	assert.True(t, got)
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", planId).Update("allow_wallet_overflow", false).Error)
	got, err = UserActiveSubscriptionsAllowWalletOverflow(userId)
	require.NoError(t, err)
	assert.False(t, got)
}
