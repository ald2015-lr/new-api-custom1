package model

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const bonusTestDay = int64(bonusSecondsPerDay)

// useBonusTestSettings pins every reward setting the ledger reads and restores
// them afterwards: QuotaPerUnit 500000, check-in reward 1000, paid threshold
// 1000000 (×2), 30-day expiries, fixed expiry, compliance confirmed,
// QuotaForInviter 250000 and QuotaForInvitee 100000, invite threshold 0.
func useBonusTestSettings(t *testing.T) {
	t.Helper()
	checkin := operation_setting.GetCheckinSetting()
	savedCheckin := *checkin
	payment := operation_setting.GetPaymentSetting()
	savedConfirmed, savedVersion := payment.ComplianceConfirmed, payment.ComplianceTermsVersion
	savedQuotaPerUnit := common.QuotaPerUnit
	savedInviter, savedInvitee, savedNewUser := common.QuotaForInviter, common.QuotaForInvitee, common.QuotaForNewUser
	savedThreshold, savedDays, savedSliding := common.InviteRechargeThresholdQuota, common.InviteRewardExpireDays, common.InviteRewardExpireOnlyWhenUnused
	t.Cleanup(func() {
		*checkin = savedCheckin
		payment.ComplianceConfirmed, payment.ComplianceTermsVersion = savedConfirmed, savedVersion
		common.QuotaPerUnit = savedQuotaPerUnit
		common.QuotaForInviter, common.QuotaForInvitee, common.QuotaForNewUser = savedInviter, savedInvitee, savedNewUser
		common.InviteRechargeThresholdQuota, common.InviteRewardExpireDays, common.InviteRewardExpireOnlyWhenUnused = savedThreshold, savedDays, savedSliding
	})
	*checkin = operation_setting.CheckinSetting{
		Enabled:            true,
		MinQuota:           1000,
		MaxQuota:           1000,
		PaidThresholdQuota: 1_000_000,
		PaidMultiplier:     2,
		FreeExpireDays:     30,
		PaidExpireDays:     30,
	}
	payment.ComplianceConfirmed, payment.ComplianceTermsVersion = true, operation_setting.CurrentComplianceTermsVersion
	common.QuotaPerUnit = 500_000
	common.QuotaForInviter, common.QuotaForInvitee, common.QuotaForNewUser = 250_000, 100_000, 0
	common.InviteRechargeThresholdQuota, common.InviteRewardExpireDays, common.InviteRewardExpireOnlyWhenUnused = 0, 30, false
}

func setupBonusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useBonusTestSettings(t)
}

var bonusTestRefSeq int

// grantTestBonus credits a reward through the ledger at the given time, exactly
// like a check-in or an invite grant would, and returns its grant.
func grantTestBonus(t *testing.T, userId int, source string, amount int64, days int, at int64) *BonusGrant {
	t.Helper()
	bonusTestRefSeq++
	spec := bonusGrantSpec{Source: source, Amount: amount, ExpireDays: days, RefType: BonusRefCheckin, RefId: bonusTestRefSeq}
	var grant *BonusGrant
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		acct, err := lockBonusAccount(tx, userId)
		if err != nil {
			return err
		}
		if source == BonusSourceInviter {
			grant, err = grantAffBonusTx(tx, acct, spec, at)
		} else {
			grant, err = grantWalletBonusTx(tx, acct, spec, at)
		}
		return err
	}))
	return grant
}

func getBonusGrant(t *testing.T, id int) BonusGrant {
	t.Helper()
	var grant BonusGrant
	require.NoError(t, DB.First(&grant, id).Error)
	return grant
}

func getUserAffState(t *testing.T, id int) User {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota", "aff_count", "aff_quota", "aff_history").Where("id = ?", id).Take(&user).Error)
	return user
}

// countSystemLogs counts the user's system logs whose message key contains key.
func countSystemLogs(t *testing.T, userId int, key string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("user_id = ? AND type = ? AND other LIKE ?", userId, LogTypeSystem, "%"+key+"%").Count(&count).Error)
	return count
}

func seedTestTopUp(t *testing.T, topUp TopUp) {
	t.Helper()
	topUp.TradeNo = "bonus-test-" + common.GetRandomString(12)
	if topUp.Status == "" {
		topUp.Status = common.TopUpStatusSuccess
	}
	require.NoError(t, DB.Create(&topUp).Error)
}

func seedUsedRedemption(t *testing.T, userId int, quota int) Redemption {
	t.Helper()
	redemption := Redemption{
		Key:        common.GetRandomString(32),
		Status:     common.RedemptionCodeStatusUsed,
		Quota:      quota,
		UsedUserId: userId,
		Name:       "bonus-test",
	}
	require.NoError(t, DB.Create(&redemption).Error)
	return redemption
}

func seedTestSubscription(t *testing.T, userId int) int {
	t.Helper()
	plan := SubscriptionPlan{Title: "bonus test", DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 1_000_000, Enabled: true}
	require.NoError(t, DB.Create(&plan).Error)
	InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(plan.Id) })
	subscription := UserSubscription{
		UserId: userId, PlanId: plan.Id, AmountTotal: 1_000_000, Status: "active",
		StartTime: time.Now().Unix(), EndTime: time.Now().Add(24 * time.Hour).Unix(),
	}
	require.NoError(t, DB.Create(&subscription).Error)
	return subscription.Id
}

// useSharedMemoryTestDB swaps in a database with several connections for code
// that reads DB inside its own transaction (PurchaseSubscriptionWithBalance).
func useSharedMemoryTestDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(2)
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&User{}, &Log{}, &TopUp{}, &Redemption{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionOrder{}, &BonusGrant{}))
}

func TestUserRechargeQuotaCountsTopUpsAndRedemptionsOnly(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, userId int)
		want  int64
	}{
		{name: "epay top-up in units", want: 1_000_000, setup: func(t *testing.T, userId int) {
			seedTestTopUp(t, TopUp{UserId: userId, Amount: 2, Money: 14, PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay})
		}},
		{name: "stripe top-up by paid money", want: 750_000, setup: func(t *testing.T, userId int) {
			seedTestTopUp(t, TopUp{UserId: userId, Amount: 10, Money: 1.5, PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe})
		}},
		{name: "creem top-up in quota", want: 300_000, setup: func(t *testing.T, userId int) {
			seedTestTopUp(t, TopUp{UserId: userId, Amount: 300_000, Money: 3, PaymentMethod: PaymentMethodCreem, PaymentProvider: PaymentProviderCreem})
		}},
		{name: "legacy creem order without provider", want: 200_000, setup: func(t *testing.T, userId int) {
			seedTestTopUp(t, TopUp{UserId: userId, Amount: 200_000, Money: 2, PaymentMethod: PaymentMethodCreem})
		}},
		{name: "used redemption codes, also deleted ones", want: 150_000, setup: func(t *testing.T, userId int) {
			seedUsedRedemption(t, userId, 100_000)
			deleted := seedUsedRedemption(t, userId, 50_000)
			require.NoError(t, DB.Delete(&deleted).Error)
		}},
		{name: "pending top-up", setup: func(t *testing.T, userId int) {
			seedTestTopUp(t, TopUp{UserId: userId, Amount: 2, Money: 14, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending})
		}},
		{name: "subscription purchase mirror", setup: func(t *testing.T, userId int) {
			seedTestTopUp(t, TopUp{UserId: userId, Amount: 0, Money: 9.9, PaymentMethod: PaymentMethodStripe})
		}},
		{name: "admin quota adjustment", setup: func(t *testing.T, userId int) {
			_, err := AdjustUserQuota(userId, common.RoleRootUser, "add", 2_000_000)
			require.NoError(t, err)
		}},
		{name: "usage refund", setup: func(t *testing.T, userId int) {
			require.NoError(t, RefundUserQuota(userId, 2_000_000, common.GetTimestamp()))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, 0)
			other := createReserveTestUser(t, 0)
			seedUsedRedemption(t, other.Id, 999)

			tc.setup(t, user.Id)

			recharged, err := GetUserRechargeQuota(user.Id)
			require.NoError(t, err)
			assert.Equal(t, tc.want, recharged)
			totals, err := GetUsersRechargeQuota([]int{user.Id, other.Id})
			require.NoError(t, err)
			assert.Equal(t, tc.want, totals[user.Id])
			assert.EqualValues(t, 999, totals[other.Id])
		})
	}
}

// Paying a subscription with the balance is wallet spending (it consumes
// bonuses first and is a use) but never a recharge.
func TestSubscriptionBalancePurchaseIsWalletSpendingNotRecharge(t *testing.T) {
	useBonusTestSettings(t)
	useSharedMemoryTestDB(t)
	user := createReserveTestUser(t, 1_000_000)
	plan := SubscriptionPlan{Title: "balance plan", PriceAmount: 1, DurationUnit: SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 100, Enabled: true}
	require.NoError(t, DB.Create(&plan).Error)
	InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(plan.Id) })
	before := common.GetTimestamp()

	require.NoError(t, PurchaseSubscriptionWithBalance(user.Id, plan.Id))

	ledger := getWalletLedger(t, user.Id)
	assert.Equal(t, 500_000, ledger.Quota)
	assert.EqualValues(t, 500_000, ledger.WalletSpentQuota)
	assert.GreaterOrEqual(t, ledger.WalletUsedAt, before)
	recharged, err := GetUserRechargeQuota(user.Id)
	require.NoError(t, err)
	assert.Zero(t, recharged)
}

func TestCheckinRewardFreeVersusPaid(t *testing.T) {
	t.Run("free user gets the base reward with the free expiry", func(t *testing.T) {
		setupBonusTest(t)
		operation_setting.GetCheckinSetting().FreeExpireDays = 30
		user := createReserveTestUser(t, 0)
		before := common.GetTimestamp()

		result, err := UserCheckin(user.Id)
		require.NoError(t, err)

		assert.Equal(t, 1000, result.Checkin.QuotaAwarded)
		assert.Equal(t, 1000, result.BaseQuota)
		assert.False(t, result.IsPaidUser)
		assert.False(t, result.PaidMultiplierApplied)
		assert.Equal(t, 30, result.ExpireDays)
		assert.False(t, result.ExpireOnlyWhenUnused)
		assert.GreaterOrEqual(t, result.ExpiresAt, before+30*bonusTestDay)
		assert.Equal(t, 1000, getUserQuotaFromDB(t, user.Id))
		var grant BonusGrant
		require.NoError(t, DB.Where("user_id = ?", user.Id).Take(&grant).Error)
		assert.Equal(t, BonusSourceCheckinFree, grant.Source)
		assert.Equal(t, BonusPoolWallet, grant.Pool)
		assert.EqualValues(t, 1000, grant.Amount)
		assert.Equal(t, 30*bonusTestDay, grant.ExpireSeconds)
		assert.Equal(t, result.ExpiresAt, grant.ExpiresAt)
		assert.Equal(t, BonusRefCheckin, grant.RefType)
		assert.Equal(t, result.Checkin.Id, grant.RefId)
		assert.Equal(t, "Daily check-in, received {{quota}}, expires at {{expires_at}}", result.LogMessage().Key)

		_, err = UserCheckin(user.Id)
		assert.EqualError(t, err, "Already checked in today")
		assert.Equal(t, 1000, getUserQuotaFromDB(t, user.Id))
	})

	t.Run("paid user gets the multiplied reward rounded down with the paid expiry", func(t *testing.T) {
		setupBonusTest(t)
		setting := operation_setting.GetCheckinSetting()
		setting.MinQuota, setting.MaxQuota = 1001, 1001
		setting.PaidMultiplier = 1.5
		setting.PaidExpireDays = 7
		user := createReserveTestUser(t, 0)
		seedUsedRedemption(t, user.Id, 1_000_000)

		result, err := UserCheckin(user.Id)
		require.NoError(t, err)

		assert.Equal(t, 1501, result.Checkin.QuotaAwarded)
		assert.Equal(t, 1001, result.BaseQuota)
		assert.True(t, result.IsPaidUser)
		assert.True(t, result.PaidMultiplierApplied)
		assert.Equal(t, 1.5, result.PaidMultiplier)
		assert.Equal(t, 7, result.ExpireDays)
		var stored Checkin
		require.NoError(t, DB.First(&stored, result.Checkin.Id).Error)
		assert.Equal(t, 1501, stored.QuotaAwarded)
		var grant BonusGrant
		require.NoError(t, DB.Where("user_id = ?", user.Id).Take(&grant).Error)
		assert.Equal(t, BonusSourceCheckinPaid, grant.Source)
		assert.EqualValues(t, 1501, grant.Amount)
		assert.Equal(t, 7*bonusTestDay, grant.ExpireSeconds)
		message := result.LogMessage()
		assert.Equal(t, "Daily check-in (paid user ×{{multiplier}}), received {{quota}}, expires at {{expires_at}}", message.Key)
		assert.Equal(t, "1.5", message.Params["multiplier"])
	})

	t.Run("a reward that never expires is plain balance", func(t *testing.T) {
		setupBonusTest(t)
		operation_setting.GetCheckinSetting().FreeExpireDays = 0
		user := createReserveTestUser(t, 0)

		result, err := UserCheckin(user.Id)
		require.NoError(t, err)

		assert.Equal(t, 1000, getUserQuotaFromDB(t, user.Id))
		assert.Zero(t, result.ExpireDays)
		assert.Zero(t, result.ExpiresAt)
		var grants int64
		require.NoError(t, DB.Model(&BonusGrant{}).Where("user_id = ?", user.Id).Count(&grants).Error)
		assert.Zero(t, grants)
		assert.Equal(t, "Daily check-in, received {{quota}}", result.LogMessage().Key)
	})
}

func TestWalletBonusConsumedFirst(t *testing.T) {
	t0 := common.GetTimestamp() - 40*bonusTestDay

	// Example 1 of the design: spending draws from the bonus first and only
	// the unspent remainder expires, so the final balance is the paid top-up.
	t.Run("only the unspent remainder expires", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 1_000_000)
		first := grantTestBonus(t, user.Id, BonusSourceCheckinPaid, 10_000, 30, t0)
		require.NoError(t, DecreaseUserQuota(user.Id, 6_000, false))
		second := grantTestBonus(t, user.Id, BonusSourceCheckinPaid, 16_000, 30, t0+bonusTestDay)
		assert.EqualValues(t, 4_000, getBonusGrant(t, first.Id).Remaining, "usage before a grant is settled before it")
		require.NoError(t, DecreaseUserQuota(user.Id, 15_000, false))

		dayThirty := t0 + 30*bonusTestDay
		require.NoError(t, ExpireUserBonusGrants(user.Id, dayThirty, dayThirty))
		firstAfter, secondAfter := getBonusGrant(t, first.Id), getBonusGrant(t, second.Id)
		assert.Equal(t, BonusStatusUsed, firstAfter.Status)
		assert.Zero(t, firstAfter.DeductedQuota)
		assert.Equal(t, BonusStatusActive, secondAfter.Status)
		assert.EqualValues(t, 5_000, secondAfter.Remaining)
		assert.Equal(t, 1_005_000, getUserQuotaFromDB(t, user.Id))

		dayThirtyOne := t0 + 31*bonusTestDay
		require.NoError(t, ExpireUserBonusGrants(user.Id, dayThirtyOne, dayThirtyOne))
		secondAfter = getBonusGrant(t, second.Id)
		assert.Equal(t, BonusStatusExpired, secondAfter.Status)
		assert.EqualValues(t, 5_000, secondAfter.DeductedQuota)
		assert.Equal(t, dayThirtyOne, secondAfter.ClosedAt)
		assert.Equal(t, 1_000_000, getUserQuotaFromDB(t, user.Id))
		assert.EqualValues(t, 1, countSystemLogs(t, user.Id, "Check-in reward of {{amount}} expired, {{quota}} unused quota deducted"))
	})

	t.Run("spending more than the bonus deducts nothing", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 1_000_000)
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, t0)
		require.NoError(t, DecreaseUserQuota(user.Id, 50_000, false))

		require.NoError(t, ExpireUserBonusGrants(user.Id, t0+30*bonusTestDay, t0+30*bonusTestDay))

		assert.Equal(t, BonusStatusUsed, getBonusGrant(t, grant.Id).Status)
		assert.Equal(t, 960_000, getUserQuotaFromDB(t, user.Id))
	})

	t.Run("the earliest deadline is consumed first", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 0)
		long := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 5_000, 30, t0)
		short := grantTestBonus(t, user.Id, BonusSourceCheckinPaid, 5_000, 7, t0)
		require.NoError(t, DecreaseUserQuota(user.Id, 3_000, false))

		require.NoError(t, ExpireUserBonusGrants(user.Id, t0+7*bonusTestDay, t0+7*bonusTestDay))

		shortAfter, longAfter := getBonusGrant(t, short.Id), getBonusGrant(t, long.Id)
		assert.Equal(t, BonusStatusExpired, shortAfter.Status)
		assert.EqualValues(t, 2_000, shortAfter.DeductedQuota)
		assert.Equal(t, BonusStatusActive, longAfter.Status)
		assert.EqualValues(t, 5_000, longAfter.Remaining)
		assert.Equal(t, 5_000, getUserQuotaFromDB(t, user.Id))
	})

	t.Run("credits, admin adjustments and transfers are not consumption", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 1_000_000)
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, t0)
		for _, adjustment := range []struct {
			mode  string
			value int
		}{{"add", 5_000}, {"subtract", 5_000}, {"override", 1_020_000}} {
			_, err := AdjustUserQuota(user.Id, common.RoleRootUser, adjustment.mode, adjustment.value)
			require.NoError(t, err)
		}
		pending := TopUp{UserId: user.Id, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending}
		seedTestTopUp(t, pending)
		var stored TopUp
		require.NoError(t, DB.Where("user_id = ? AND status = ?", user.Id, common.TopUpStatusPending).Take(&stored).Error)
		require.NoError(t, ManualCompleteTopUp(stored.TradeNo, "127.0.0.1"))
		require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("aff_quota", 500_000).Error)
		require.NoError(t, (&User{Id: user.Id}).TransferAffQuotaToQuota(500_000))
		assert.Zero(t, getWalletLedger(t, user.Id).WalletSpentQuota)

		require.NoError(t, ExpireUserBonusGrants(user.Id, t0+30*bonusTestDay, t0+30*bonusTestDay))

		assert.EqualValues(t, 10_000, getBonusGrant(t, grant.Id).DeductedQuota)
		assert.Equal(t, 2_010_000, getUserQuotaFromDB(t, user.Id))
	})

	// Example 2 of the design: a refund of a charge made before a grant was
	// settled stays paid balance, so the expiry cannot take paid money.
	t.Run("a refund across a grant keeps paid money", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 1_000_000)
		now := common.GetTimestamp()
		chargedAt := now - 300
		require.NoError(t, DecreaseUserQuota(user.Id, 3_000, false))
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, now)
		require.NoError(t, RefundUserQuota(user.Id, 3_000, chargedAt))
		assert.EqualValues(t, 3_000, getWalletLedger(t, user.Id).WalletSpentQuota)
		require.NoError(t, DecreaseUserQuota(user.Id, 4_000, false))

		require.NoError(t, ExpireUserBonusGrants(user.Id, now+30*bonusTestDay, now+30*bonusTestDay))

		assert.EqualValues(t, 6_000, getBonusGrant(t, grant.Id).DeductedQuota)
		assert.Equal(t, 1_000_000, getUserQuotaFromDB(t, user.Id))
	})

	t.Run("a refund of a charge after the grant restores the bonus", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 1_000_000)
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, t0)
		require.NoError(t, DecreaseUserQuota(user.Id, 2_000, false))
		require.NoError(t, RefundUserQuota(user.Id, 1_700, common.GetTimestamp()))

		require.NoError(t, ExpireUserBonusGrants(user.Id, t0+30*bonusTestDay, t0+30*bonusTestDay))

		assert.EqualValues(t, 9_700, getBonusGrant(t, grant.Id).DeductedQuota)
		assert.Equal(t, 1_000_000, getUserQuotaFromDB(t, user.Id))
	})

	for _, tc := range []struct {
		name      string
		balance   int
		deducted  int64
		wantQuota int
	}{
		{name: "a balance below the remainder goes to zero", balance: 3_000, deducted: 3_000, wantQuota: 0},
		{name: "a negative balance is left alone", balance: -500, deducted: 0, wantQuota: -500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, 0)
			grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, t0)
			_, err := AdjustUserQuota(user.Id, common.RoleRootUser, "override", tc.balance)
			require.NoError(t, err)

			require.NoError(t, ExpireUserBonusGrants(user.Id, t0+30*bonusTestDay, t0+30*bonusTestDay))

			after := getBonusGrant(t, grant.Id)
			assert.Equal(t, BonusStatusExpired, after.Status)
			assert.Equal(t, tc.deducted, after.DeductedQuota)
			assert.Equal(t, tc.wantQuota, getUserQuotaFromDB(t, user.Id))
		})
	}

	t.Run("the deduction is capped by the cached balance", func(t *testing.T) {
		setupBonusTest(t)
		useUserCacheMiniRedis(t)
		user := createReserveTestUser(t, 0)
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, t0)
		_, err := GetUserCache(user.Id)
		require.NoError(t, err)
		result, err := cacheApplyUserQuotaDelta(user.Id, -7_000)
		require.NoError(t, err)
		require.Equal(t, cacheQuotaOK, result)

		require.NoError(t, ExpireUserBonusGrants(user.Id, t0+30*bonusTestDay, t0+30*bonusTestDay))

		assert.EqualValues(t, 3_000, getBonusGrant(t, grant.Id).DeductedQuota)
		assert.Equal(t, 7_000, getUserQuotaFromDB(t, user.Id))
		cached, err := cacheGetUserBase(user.Id)
		require.NoError(t, err)
		assert.Zero(t, cached.Quota)
	})
}

// Subscription-funded usage neither consumes a wallet bonus nor counts as a
// use of it, even when bonuses expire only when unused.
func TestSubscriptionUsageDoesNotConsumeWalletBonus(t *testing.T) {
	setupBonusTest(t)
	operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = true
	user := createReserveTestUser(t, 0)
	created := common.GetTimestamp() - 31*bonusTestDay
	grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, created)
	subscriptionId := seedTestSubscription(t, user.Id)

	_, err := PreConsumeUserSubscription("bonus-sub-request", user.Id, "gpt-test", 0, 5_000)
	require.NoError(t, err)
	require.NoError(t, PostConsumeUserSubscriptionDelta(subscriptionId, 2_000))
	ledger := getWalletLedger(t, user.Id)
	assert.Zero(t, ledger.WalletSpentQuota)
	assert.Zero(t, ledger.WalletUsedAt)

	now := common.GetTimestamp()
	require.NoError(t, ExpireUserBonusGrants(user.Id, now, now))

	after := getBonusGrant(t, grant.Id)
	assert.Equal(t, BonusStatusExpired, after.Status)
	assert.EqualValues(t, 10_000, after.DeductedQuota)
	assert.Zero(t, getUserQuotaFromDB(t, user.Id))
}

func TestInviteRewardThreshold(t *testing.T) {
	newInvitee := func(t *testing.T, inviterId int) *User {
		t.Helper()
		invitee := &User{Username: "invitee" + common.GetRandomString(6), Password: "invitee-password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, InviterId: inviterId}
		require.NoError(t, invitee.Insert(inviterId))
		return invitee
	}
	pendingReward := func(t *testing.T, inviteeId, inviterId int) InviteReward {
		t.Helper()
		reward := InviteReward{InviteeId: inviteeId, InviterId: inviterId, InviterQuota: 250_000, InviteeQuota: 100_000, Status: InviteRewardStatusPending, CreatedAt: common.GetTimestamp()}
		require.NoError(t, DB.Create(&reward).Error)
		return reward
	}
	rewardStatus := func(t *testing.T, inviteeId int) string {
		t.Helper()
		var reward InviteReward
		require.NoError(t, DB.Where("invitee_id = ?", inviteeId).Take(&reward).Error)
		return reward.Status
	}
	grantCount := func(t *testing.T, userId int, source string) int64 {
		t.Helper()
		var count int64
		require.NoError(t, DB.Model(&BonusGrant{}).Where("user_id = ? AND source = ?", userId, source).Count(&count).Error)
		return count
	}

	t.Run("threshold 0 grants both rewards at registration", func(t *testing.T) {
		setupBonusTest(t)
		inviter := createReserveTestUser(t, 0)

		invitee := newInvitee(t, inviter.Id)

		assert.Equal(t, InviteRewardStatusGranted, rewardStatus(t, invitee.Id))
		assert.Equal(t, 100_000, getUserQuotaFromDB(t, invitee.Id))
		state := getUserAffState(t, inviter.Id)
		assert.Equal(t, 1, state.AffCount)
		assert.Equal(t, 250_000, state.AffQuota)
		assert.Equal(t, 250_000, state.AffHistoryQuota)
		assert.Zero(t, state.Quota)
		var walletGrant, affGrant BonusGrant
		require.NoError(t, DB.Where("user_id = ? AND source = ?", invitee.Id, BonusSourceInvitee).Take(&walletGrant).Error)
		require.NoError(t, DB.Where("user_id = ? AND source = ?", inviter.Id, BonusSourceInviter).Take(&affGrant).Error)
		assert.Equal(t, BonusPoolWallet, walletGrant.Pool)
		assert.Equal(t, BonusPoolAff, affGrant.Pool)
		assert.Equal(t, 30*bonusTestDay, affGrant.ExpireSeconds)
		assert.EqualValues(t, 1, countSystemLogs(t, invitee.Id, "Invitation code bonus {{quota}}, expires at {{expires_at}}"))
		assert.EqualValues(t, 1, countSystemLogs(t, inviter.Id, "Bonus for inviting a user {{quota}}, expires at {{expires_at}} unless transferred"))
	})

	t.Run("a threshold waits for the invitee's cumulative recharge", func(t *testing.T) {
		setupBonusTest(t)
		common.InviteRechargeThresholdQuota = 1_000_000
		inviter := createReserveTestUser(t, 0)
		invitee := newInvitee(t, inviter.Id)
		assert.Equal(t, InviteRewardStatusPending, rewardStatus(t, invitee.Id))
		assert.Zero(t, getUserQuotaFromDB(t, invitee.Id))
		state := getUserAffState(t, inviter.Id)
		assert.Equal(t, 1, state.AffCount, "registrations are still counted at sign-up")
		assert.Zero(t, state.AffQuota)

		topUp := func() {
			seedTestTopUp(t, TopUp{UserId: invitee.Id, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending})
			var stored TopUp
			require.NoError(t, DB.Where("user_id = ? AND status = ?", invitee.Id, common.TopUpStatusPending).Take(&stored).Error)
			require.NoError(t, ManualCompleteTopUp(stored.TradeNo, "127.0.0.1"))
		}
		topUp()
		assert.Equal(t, InviteRewardStatusPending, rewardStatus(t, invitee.Id), "500000 is below the threshold")
		assert.Equal(t, 500_000, getUserQuotaFromDB(t, invitee.Id))

		topUp()
		assert.Equal(t, InviteRewardStatusGranted, rewardStatus(t, invitee.Id))
		assert.Equal(t, 1_100_000, getUserQuotaFromDB(t, invitee.Id))
		state = getUserAffState(t, inviter.Id)
		assert.Equal(t, 1, state.AffCount)
		assert.Equal(t, 250_000, state.AffQuota)
		assert.Equal(t, 250_000, state.AffHistoryQuota)
	})

	t.Run("concurrent grant attempts credit once", func(t *testing.T) {
		setupBonusTest(t)
		inviter := createReserveTestUser(t, 0)
		invitee := createReserveTestUser(t, 0)
		pendingReward(t, invitee.Id, inviter.Id)

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				assert.NoError(t, GrantInviteRewardIfQualified(invitee.Id))
			})
		}
		wg.Wait()

		assert.EqualValues(t, 1, grantCount(t, invitee.Id, BonusSourceInvitee))
		assert.EqualValues(t, 1, grantCount(t, inviter.Id, BonusSourceInviter))
		assert.Equal(t, 100_000, getUserQuotaFromDB(t, invitee.Id))
		assert.Equal(t, 250_000, getUserAffState(t, inviter.Id).AffHistoryQuota)
	})

	t.Run("nothing is granted while compliance is not confirmed", func(t *testing.T) {
		setupBonusTest(t)
		operation_setting.GetPaymentSetting().ComplianceConfirmed = false
		inviter := createReserveTestUser(t, 0)
		invitee := createReserveTestUser(t, 0)
		pendingReward(t, invitee.Id, inviter.Id)

		require.NoError(t, GrantInviteRewardIfQualified(invitee.Id))
		granted, err := RescanPendingInviteRewards()
		require.NoError(t, err)

		assert.Zero(t, granted)
		assert.Equal(t, InviteRewardStatusPending, rewardStatus(t, invitee.Id))
		assert.Zero(t, getUserQuotaFromDB(t, invitee.Id))
	})

	t.Run("a lowered threshold is applied by the rescan", func(t *testing.T) {
		setupBonusTest(t)
		common.InviteRechargeThresholdQuota = 2_000_000
		inviter := createReserveTestUser(t, 0)
		qualified := createReserveTestUser(t, 0)
		below := createReserveTestUser(t, 0)
		pendingReward(t, qualified.Id, inviter.Id)
		pendingReward(t, below.Id, inviter.Id)
		seedTestTopUp(t, TopUp{UserId: qualified.Id, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay})
		granted, err := RescanPendingInviteRewards()
		require.NoError(t, err)
		require.Zero(t, granted)

		common.InviteRechargeThresholdQuota = 500_000
		granted, err = RescanPendingInviteRewards()
		require.NoError(t, err)

		assert.Equal(t, 1, granted)
		assert.Equal(t, InviteRewardStatusGranted, rewardStatus(t, qualified.Id))
		assert.Equal(t, InviteRewardStatusPending, rewardStatus(t, below.Id))
		assert.Equal(t, 100_000, getUserQuotaFromDB(t, qualified.Id))
	})

	for _, removal := range []struct {
		name   string
		remove func(id int) (int64, error)
	}{
		{name: "soft", remove: DeleteUserById},
		{name: "hard", remove: HardDeleteUserById},
	} {
		t.Run("a "+removal.name+"-deleted invitee is no longer a pending invite", func(t *testing.T) {
			setupBonusTest(t)
			common.InviteRechargeThresholdQuota = 500_000
			inviter := createReserveTestUser(t, 0)
			invitee := newInvitee(t, inviter.Id)
			_, err := removal.remove(invitee.Id)
			require.NoError(t, err)
			common.InviteRechargeThresholdQuota = 0
			rewardUpdates := 0
			const callback = "bonus_test:invite_reward_updates"
			require.NoError(t, DB.Callback().Update().After("gorm:update").Register(callback, func(db *gorm.DB) {
				if db.Statement.Table == "invite_rewards" {
					rewardUpdates++
				}
			}))
			t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })

			granted, err := RescanPendingInviteRewards()
			require.NoError(t, err)
			summary, err := GetUserBonusSummary(inviter.Id)
			require.NoError(t, err)

			assert.Zero(t, granted)
			assert.Zero(t, rewardUpdates, "the rescan does not retry a reward that can never be granted")
			assert.Zero(t, summary.PendingInvites.Count)
			assert.Empty(t, summary.PendingInvites.Items)
		})
	}
}

func TestAffTransferAndExpiry(t *testing.T) {
	setupBonusTest(t)
	user := createReserveTestUser(t, 0)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("aff_quota", 500_000).Error)
	created := common.GetTimestamp() - 10*bonusTestDay
	grant := grantTestBonus(t, user.Id, BonusSourceInviter, 1_000_000, 30, created)

	err := (&User{Id: user.Id}).TransferAffQuotaToQuota(100)
	var message *common.Message
	require.ErrorAs(t, err, &message)
	assert.Equal(t, "Minimum transfer quota is {{min}}!", message.Key)
	err = (&User{Id: user.Id}).TransferAffQuotaToQuota(2_000_000)
	require.ErrorAs(t, err, &message)
	assert.Equal(t, "Invitation quota is insufficient!", message.Key)

	before := common.GetTimestamp()
	require.NoError(t, (&User{Id: user.Id}).TransferAffQuotaToQuota(750_000))
	transferred := getBonusGrant(t, grant.Id)
	assert.EqualValues(t, 250_000, transferred.Remaining, "the expiring reward is transferred first")
	assert.GreaterOrEqual(t, transferred.LastUsedAt, before)
	state := getUserAffState(t, user.Id)
	assert.Equal(t, 750_000, state.AffQuota)
	assert.Equal(t, 750_000, state.Quota)
	assert.Zero(t, getWalletLedger(t, user.Id).WalletSpentQuota, "a transfer is not usage")

	deadline := created + 30*bonusTestDay
	require.NoError(t, ExpireUserBonusGrants(user.Id, deadline, deadline))

	expired := getBonusGrant(t, grant.Id)
	assert.Equal(t, BonusStatusExpired, expired.Status)
	assert.EqualValues(t, 250_000, expired.DeductedQuota)
	state = getUserAffState(t, user.Id)
	assert.Equal(t, 500_000, state.AffQuota, "the legacy invitation balance never expires")
	assert.Equal(t, 750_000, state.Quota, "transferred quota never expires")
	assert.EqualValues(t, 1, countSystemLogs(t, user.Id, "Invite reward of {{amount}} expired, {{quota}} untransferred quota removed from the invitation balance"))
}

func TestAffTransferBelowMinimum(t *testing.T) {
	t.Run("the whole balance below the minimum is transferred", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 0)
		grant := grantTestBonus(t, user.Id, BonusSourceInviter, 250_000, 30, common.GetTimestamp())

		require.NoError(t, (&User{Id: user.Id}).TransferAffQuotaToQuota(250_000))

		state := getUserAffState(t, user.Id)
		assert.Zero(t, state.AffQuota)
		assert.Equal(t, 250_000, state.Quota)
		assert.Equal(t, BonusStatusUsed, getBonusGrant(t, grant.Id).Status, "the lone reward is transferred and can no longer expire")
	})

	t.Run("part of a balance below the minimum is rejected", func(t *testing.T) {
		setupBonusTest(t)
		user := createReserveTestUser(t, 0)
		grantTestBonus(t, user.Id, BonusSourceInviter, 250_000, 30, common.GetTimestamp())

		err := (&User{Id: user.Id}).TransferAffQuotaToQuota(100_000)

		var message *common.Message
		require.ErrorAs(t, err, &message)
		assert.Equal(t, "An invitation balance below {{min}} can only be transferred in full ({{balance}})!", message.Key)
		state := getUserAffState(t, user.Id)
		assert.Equal(t, 250_000, state.AffQuota)
		assert.Zero(t, state.Quota)
	})
}

func TestBonusExpiryIdempotent(t *testing.T) {
	setupBonusTest(t)
	created := common.GetTimestamp() - 31*bonusTestDay
	now := common.GetTimestamp()
	sequential := createReserveTestUser(t, 0)
	concurrent := createReserveTestUser(t, 0)
	grantTestBonus(t, sequential.Id, BonusSourceCheckinFree, 10_000, 30, created)
	grantTestBonus(t, concurrent.Id, BonusSourceInvitee, 10_000, 30, created)

	require.NoError(t, ExpireUserBonusGrants(sequential.Id, now, now))
	require.NoError(t, ExpireUserBonusGrants(sequential.Id, now, now))
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			assert.NoError(t, ExpireUserBonusGrants(concurrent.Id, now, now))
		})
	}
	wg.Wait()

	assert.Zero(t, getUserQuotaFromDB(t, sequential.Id))
	assert.Zero(t, getUserQuotaFromDB(t, concurrent.Id))
	assert.EqualValues(t, 1, countSystemLogs(t, sequential.Id, "Check-in reward of {{amount}} expired, {{quota}} unused quota deducted"))
	assert.EqualValues(t, 1, countSystemLogs(t, concurrent.Id, "Invitation bonus of {{amount}} expired, {{quota}} unused quota deducted"))
}

func TestExpireOnlyWhenUnused(t *testing.T) {
	checkinSources := []string{BonusSourceCheckinFree, BonusSourceCheckinPaid}

	t.Run("a wallet use within the period restarts it", func(t *testing.T) {
		setupBonusTest(t)
		operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = true
		user := createReserveTestUser(t, 0)
		created := common.GetTimestamp() - 25*bonusTestDay
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, created)
		require.NoError(t, DecreaseUserQuota(user.Id, 100, false))
		lastUse := getWalletLedger(t, user.Id).WalletUsedAt

		fixedDeadline := created + 30*bonusTestDay
		require.NoError(t, ExpireUserBonusGrants(user.Id, fixedDeadline, fixedDeadline))
		postponed := getBonusGrant(t, grant.Id)
		assert.Equal(t, BonusStatusActive, postponed.Status)
		assert.Equal(t, lastUse+30*bonusTestDay, postponed.CheckAt)
		due, err := ListUsersWithDueBonusGrants(checkinSources, true, fixedDeadline, 0, 10)
		require.NoError(t, err)
		assert.Empty(t, due, "a postponed grant leaves the sweep's candidates")

		require.NoError(t, ExpireUserBonusGrants(user.Id, postponed.CheckAt, postponed.CheckAt))
		expired := getBonusGrant(t, grant.Id)
		assert.Equal(t, BonusStatusExpired, expired.Status)
		assert.EqualValues(t, 9_900, expired.DeductedQuota)
	})

	t.Run("an unused reward expires after its period", func(t *testing.T) {
		setupBonusTest(t)
		operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = true
		user := createReserveTestUser(t, 0)
		created := common.GetTimestamp() - 31*bonusTestDay
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinPaid, 10_000, 30, created)
		due, err := ListUsersWithDueBonusGrants(checkinSources, true, created+30*bonusTestDay, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, []int{user.Id}, due)

		require.NoError(t, ExpireUserBonusGrants(user.Id, created+30*bonusTestDay, created+30*bonusTestDay))

		assert.Equal(t, BonusStatusExpired, getBonusGrant(t, grant.Id).Status)
		assert.Zero(t, getUserQuotaFromDB(t, user.Id))
	})

	t.Run("switching the option off returns to the fixed deadline", func(t *testing.T) {
		setupBonusTest(t)
		setting := operation_setting.GetCheckinSetting()
		setting.ExpireOnlyWhenUnused = true
		user := createReserveTestUser(t, 0)
		created := common.GetTimestamp() - 25*bonusTestDay
		grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, created)
		require.NoError(t, DecreaseUserQuota(user.Id, 100, false))
		fixedDeadline := created + 30*bonusTestDay
		require.NoError(t, ExpireUserBonusGrants(user.Id, fixedDeadline, fixedDeadline))
		require.Equal(t, BonusStatusActive, getBonusGrant(t, grant.Id).Status)

		setting.ExpireOnlyWhenUnused = false
		due, err := ListUsersWithDueBonusGrants(checkinSources, false, fixedDeadline, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, []int{user.Id}, due)
		require.NoError(t, ExpireUserBonusGrants(user.Id, fixedDeadline+60, fixedDeadline))

		assert.Equal(t, BonusStatusExpired, getBonusGrant(t, grant.Id).Status)
	})

	t.Run("a transfer restarts the remaining inviter rewards", func(t *testing.T) {
		setupBonusTest(t)
		common.InviteRewardExpireOnlyWhenUnused = true
		user := createReserveTestUser(t, 0)
		created := common.GetTimestamp() - 25*bonusTestDay
		grant := grantTestBonus(t, user.Id, BonusSourceInviter, 1_000_000, 30, created)
		require.NoError(t, (&User{Id: user.Id}).TransferAffQuotaToQuota(500_000))
		lastUse := getBonusGrant(t, grant.Id).LastUsedAt

		fixedDeadline := created + 30*bonusTestDay
		require.NoError(t, ExpireUserBonusGrants(user.Id, fixedDeadline, fixedDeadline))
		postponed := getBonusGrant(t, grant.Id)
		assert.Equal(t, BonusStatusActive, postponed.Status)
		assert.Equal(t, lastUse+30*bonusTestDay, postponed.CheckAt)

		require.NoError(t, ExpireUserBonusGrants(user.Id, postponed.CheckAt, postponed.CheckAt))
		assert.EqualValues(t, 500_000, getBonusGrant(t, grant.Id).DeductedQuota)
		assert.Zero(t, getUserAffState(t, user.Id).AffQuota)
	})
}

// The self API previews unsettled usage without writing and lists the user's
// pending invite rewards without naming the invitees.
func TestUserBonusSummaryPreviewsUnsettledUsage(t *testing.T) {
	setupBonusTest(t)
	common.InviteRechargeThresholdQuota = 500_000
	operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = true
	user := createReserveTestUser(t, 0)
	invitee := createReserveTestUser(t, 0)
	inviter := createReserveTestUser(t, 0)
	created := common.GetTimestamp() - bonusTestDay
	grant := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, created)
	grantTestBonus(t, user.Id, BonusSourceInviter, 250_000, 30, created)
	require.NoError(t, DecreaseUserQuota(user.Id, 4_000, false))
	require.NoError(t, DB.Create(&InviteReward{InviteeId: invitee.Id, InviterId: user.Id, InviterQuota: 250_000, InviteeQuota: 100_000, Status: InviteRewardStatusPending, CreatedAt: created}).Error)
	inviteeOnly := createReserveTestUser(t, 0)
	require.NoError(t, DB.Create(&InviteReward{InviteeId: inviteeOnly.Id, InviterId: user.Id, InviterQuota: 0, InviteeQuota: 100_000, Status: InviteRewardStatusPending, CreatedAt: created}).Error)
	require.NoError(t, DB.Create(&InviteReward{InviteeId: user.Id, InviterId: inviter.Id, InviterQuota: 250_000, InviteeQuota: 100_000, Status: InviteRewardStatusPending, CreatedAt: created}).Error)
	seedUsedRedemption(t, user.Id, 1_000_000)

	summary, err := GetUserBonusSummary(user.Id)
	require.NoError(t, err)

	assert.EqualValues(t, 1_000_000, summary.RechargedQuota)
	assert.True(t, summary.Checkin.IsPaidUser)
	assert.True(t, summary.Checkin.ExpireOnlyWhenUnused)
	require.Len(t, summary.WalletGrants, 1)
	view := summary.WalletGrants[0]
	assert.Equal(t, BonusSourceCheckinFree, view.Source)
	assert.EqualValues(t, 6_000, view.Remaining)
	assert.Equal(t, getWalletLedger(t, user.Id).WalletUsedAt+30*bonusTestDay, view.ExpiresAt)
	assert.EqualValues(t, 30, view.ExpireDays)
	assert.True(t, view.ExpireOnlyWhenUnused)
	require.Len(t, summary.AffGrants, 1)
	assert.Equal(t, created+30*bonusTestDay, summary.AffGrants[0].ExpiresAt)
	assert.EqualValues(t, 1, summary.PendingInvites.Count, "an invite that pays only the invitee is not the inviter's pending reward")
	require.Len(t, summary.PendingInvites.Items, 1)
	assert.EqualValues(t, 250_000, summary.PendingInvites.Items[0].InviterQuota)
	require.NotNil(t, summary.OwnInviteReward)
	assert.Equal(t, InviteRewardStatusPending, summary.OwnInviteReward.Status)
	assert.Equal(t, 500_000, summary.OwnInviteReward.ThresholdQuota)
	assert.EqualValues(t, 10_000, getBonusGrant(t, grant.Id).Remaining, "the summary writes nothing")
	assert.Zero(t, getWalletLedger(t, user.Id).BonusSpentCursor)
}

// The listed rows are capped, the totals are not.
func TestUserBonusSummaryTotalsCoverUnlistedGrants(t *testing.T) {
	setupBonusTest(t)
	user := createReserveTestUser(t, 0)
	created := common.GetTimestamp() - bonusTestDay
	for range bonusSummaryGrantLimit + 20 {
		grantTestBonus(t, user.Id, BonusSourceCheckinFree, 1_000, 30, created)
	}
	require.NoError(t, DecreaseUserQuota(user.Id, 500, false))

	summary, err := GetUserBonusSummary(user.Id)
	require.NoError(t, err)

	assert.Len(t, summary.WalletGrants, bonusSummaryGrantLimit)
	assert.Equal(t, BonusGrantTotals{Count: bonusSummaryGrantLimit + 20, Remaining: 119_500}, summary.WalletTotals)
	assert.Equal(t, BonusGrantTotals{}, summary.AffTotals)
}

// The rows come in the order rewards are used, which is the fixed deadline
// whatever the switches say, so the first row is the one the next spending
// draws from even when another reward currently expires sooner.
func TestUserBonusSummaryListsRewardsInSpendingOrder(t *testing.T) {
	setupBonusTest(t)
	common.InviteRewardExpireOnlyWhenUnused = true
	user := createReserveTestUser(t, 0)
	now := common.GetTimestamp()
	invitee := grantTestBonus(t, user.Id, BonusSourceInvitee, 10_000, 30, now-20*bonusTestDay)
	checkin := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 10_000, 30, now-19*bonusTestDay)
	require.NoError(t, DecreaseUserQuota(user.Id, 4_000, false))

	summary, err := GetUserBonusSummary(user.Id)
	require.NoError(t, err)

	require.Len(t, summary.WalletGrants, 2)
	first, second := summary.WalletGrants[0], summary.WalletGrants[1]
	assert.Equal(t, invitee.Id, first.Id)
	assert.EqualValues(t, 6_000, first.Remaining, "the spending drew from the first row")
	assert.Equal(t, checkin.Id, second.Id)
	assert.Less(t, second.ExpiresAt, first.ExpiresAt, "the second row currently expires sooner")
}

// The consumption order follows the fixed deadline whatever the switches say,
// so usage made while a switch is off and settled while it is on is
// attributed as it would have been, and flipping the switch back cannot
// expire a reward that the usage really drew from.
func TestSwitchFlipDoesNotReorderConsumption(t *testing.T) {
	now := common.GetTimestamp()
	cases := []struct {
		name string
		// first expires first under the fixed rule, second under the sliding one.
		first, second             func(t *testing.T, userId int) *BonusGrant
		switchOn, switchOff       func()
		firstTick, secondDeadline int64
	}{
		{
			name: "check-in rewards with different periods",
			first: func(t *testing.T, userId int) *BonusGrant {
				return grantTestBonus(t, userId, BonusSourceCheckinPaid, 10_000, 80, now-79*bonusTestDay)
			},
			second: func(t *testing.T, userId int) *BonusGrant {
				return grantTestBonus(t, userId, BonusSourceCheckinFree, 10_000, 35, now-29*bonusTestDay)
			},
			switchOn:       func() { operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = true },
			switchOff:      func() { operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = false },
			firstTick:      now + bonusTestDay,
			secondDeadline: now + 6*bonusTestDay,
		},
		{
			name: "equal periods where only the check-in switch flips",
			first: func(t *testing.T, userId int) *BonusGrant {
				return grantTestBonus(t, userId, BonusSourceCheckinFree, 10_000, 30, now-20*bonusTestDay)
			},
			second: func(t *testing.T, userId int) *BonusGrant {
				return grantTestBonus(t, userId, BonusSourceInvitee, 10_000, 30, now-10*bonusTestDay)
			},
			switchOn:       func() { operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = true },
			switchOff:      func() { operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused = false },
			firstTick:      now + 10*bonusTestDay,
			secondDeadline: now + 20*bonusTestDay,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, 100_000)
			first, second := tc.first(t, user.Id), tc.second(t, user.Id)
			require.NoError(t, DecreaseUserQuota(user.Id, 10_000, false))

			tc.switchOn()
			require.NoError(t, ExpireUserBonusGrants(user.Id, tc.firstTick, tc.firstTick))
			tc.switchOff()
			require.NoError(t, ExpireUserBonusGrants(user.Id, tc.firstTick+60, tc.firstTick+60))
			require.NoError(t, DecreaseUserQuota(user.Id, 10_000, false))
			require.NoError(t, ExpireUserBonusGrants(user.Id, tc.secondDeadline, tc.secondDeadline))

			firstAfter, secondAfter := getBonusGrant(t, first.Id), getBonusGrant(t, second.Id)
			assert.Equal(t, BonusStatusUsed, firstAfter.Status)
			assert.Equal(t, BonusStatusUsed, secondAfter.Status)
			assert.Zero(t, firstAfter.DeductedQuota+secondAfter.DeductedQuota)
			assert.Equal(t, 100_000, getUserQuotaFromDB(t, user.Id), "all usage fell inside valid rewards")
		})
	}
}

// useBonusWriteMode switches between direct writes, batch updates and batch
// updates behind the Redis cache for the rest of the test.
func useBonusWriteMode(t *testing.T, mode string, userId int) {
	t.Helper()
	switch mode {
	case "direct":
	case "batch":
		common.BatchUpdateEnabled = true
	case "batch with redis":
		useUserCacheMiniRedis(t)
		common.BatchUpdateEnabled = true
		_, err := GetUserCache(userId)
		require.NoError(t, err)
	default:
		t.Fatalf("unknown write mode %q", mode)
	}
}

// flushBonusWrites writes batched deltas, as the batch updater would.
func flushBonusWrites() {
	if common.BatchUpdateEnabled {
		batchUpdate()
	}
}

var bonusWriteModes = []string{"direct", "batch", "batch with redis"}

// A refund of a charge that a settlement attributed to paid money stays paid
// balance in every write mode, before or after later spending: in batch mode
// the flush books it by its charge time like a direct refund, so it never
// offsets later bonus spending.
func TestRefundAcrossSettlementKeepsPaidMoneyInEveryMode(t *testing.T) {
	for _, mode := range bonusWriteModes {
		for _, spendFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/spend first %t", mode, spendFirst), func(t *testing.T) {
				setupBonusTest(t)
				user := createReserveTestUser(t, 1_000_000)
				useBonusWriteMode(t, mode, user.Id)
				chargedAt := common.GetTimestamp() - 2
				reserved, err := TryReserveUserQuota(user.Id, 5_000)
				require.NoError(t, err)
				require.True(t, reserved)
				flushBonusWrites()
				_, err = UserCheckin(user.Id)
				require.NoError(t, err)

				if spendFirst {
					require.NoError(t, DecreaseUserQuota(user.Id, 1_000, false))
				}
				require.NoError(t, RefundUserQuota(user.Id, 5_000, chargedAt))
				if !spendFirst {
					require.NoError(t, DecreaseUserQuota(user.Id, 1_000, false))
				}
				flushBonusWrites()
				var grant BonusGrant
				require.NoError(t, DB.Where("user_id = ?", user.Id).Take(&grant).Error)
				require.NoError(t, ExpireUserBonusGrants(user.Id, grant.ExpiresAt, grant.ExpiresAt))

				after := getBonusGrant(t, grant.Id)
				assert.Equal(t, BonusStatusUsed, after.Status, "the real spending used the reward")
				assert.Zero(t, after.DeductedQuota)
				assert.Equal(t, 1_000_000, getUserQuotaFromDB(t, user.Id))
			})
		}
	}
}

// A reservation still in flight when a check-in settles is attributed to the
// user's rewards; when the request fails, its refund gives that back to the
// rewards instead of turning them into permanent balance.
func TestRefundOfInFlightChargeGoesBackToTheReward(t *testing.T) {
	for _, mode := range bonusWriteModes {
		t.Run(mode, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, 0)
			old := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 1_000, 30, common.GetTimestamp()-27*bonusTestDay)
			useBonusWriteMode(t, mode, user.Id)
			chargedAt := common.GetTimestamp() - 1
			reserved, err := TryReserveUserQuota(user.Id, 1_000)
			require.NoError(t, err)
			require.True(t, reserved)
			flushBonusWrites()
			_, err = UserCheckin(user.Id)
			require.NoError(t, err)
			require.Equal(t, BonusStatusUsed, getBonusGrant(t, old.Id).Status, "the check-in settled the reservation")

			require.NoError(t, RefundUserQuota(user.Id, 1_000, chargedAt))
			flushBonusWrites()

			restored := getBonusGrant(t, old.Id)
			assert.Equal(t, BonusStatusActive, restored.Status)
			assert.EqualValues(t, 1_000, restored.Remaining)
			later := common.GetTimestamp() + 40*bonusTestDay
			require.NoError(t, ExpireUserBonusGrants(user.Id, later, later))
			assert.Zero(t, getUserQuotaFromDB(t, user.Id), "nothing was used, so every reward expired")
		})
	}
}

// A request that starts right after a check-in settled (inside the clock
// skew margin) cannot be told apart from one charged just before it, so its
// refund is first offset against the paid usage the check-in settled and only
// the rest lowers the usage counter. The documented approximation: up to
// min(refund, that paid usage) of the new reward is kept (1 and 999 here,
// against an exact 9 and 50,000), and paid money is never deducted.
func TestRefundRightAfterCheckinIsOffsetBySettledPaidUsage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		balance      int
		paidBefore   int
		wantDeducted int64
		wantQuota    int
	}{
		{name: "little paid usage before the check-in", balance: 10, paidBefore: 1, wantDeducted: 998, wantQuota: 10},
		{name: "paid usage above the refund", balance: 100_000, paidBefore: 50_000, wantDeducted: 0, wantQuota: 50_999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, tc.balance)
			require.NoError(t, DecreaseUserQuota(user.Id, tc.paidBefore, false))
			_, err := UserCheckin(user.Id)
			require.NoError(t, err)
			chargedAt := common.GetTimestamp()
			reserved, err := TryReserveUserQuota(user.Id, 1_000)
			require.NoError(t, err)
			require.True(t, reserved)

			require.NoError(t, RefundUserQuota(user.Id, 999, chargedAt))

			var grant BonusGrant
			require.NoError(t, DB.Where("user_id = ?", user.Id).Take(&grant).Error)
			require.NoError(t, ExpireUserBonusGrants(user.Id, grant.ExpiresAt+bonusTestDay, grant.ExpiresAt+bonusTestDay))
			assert.Equal(t, tc.wantDeducted, getBonusGrant(t, grant.Id).DeductedQuota)
			assert.Equal(t, tc.wantQuota, getUserQuotaFromDB(t, user.Id))
		})
	}
}

// Only the last settlement's attribution is remembered: a charge still in
// flight across two settlements (a check-in, then another grant or an expiry
// check) is refunded as plain balance, and the reward it was charged to stays
// used. The documented approximation keeps that reward (1,000 more than an
// exact 100,000 here); paid money is never deducted.
func TestRefundAcrossTwoSettlementsKeepsTheRewardSpent(t *testing.T) {
	for _, mode := range bonusWriteModes {
		t.Run(mode, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, 100_000)
			old := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 1_000, 30, common.GetTimestamp()-27*bonusTestDay)
			useBonusWriteMode(t, mode, user.Id)
			chargedAt := common.GetTimestamp() - 1
			reserved, err := TryReserveUserQuota(user.Id, 1_000)
			require.NoError(t, err)
			require.True(t, reserved)
			flushBonusWrites()
			_, err = UserCheckin(user.Id)
			require.NoError(t, err)
			require.NoError(t, DecreaseUserQuota(user.Id, 1, false))
			flushBonusWrites()
			grantTestBonus(t, user.Id, BonusSourceInvitee, 1_000, 30, common.GetTimestamp())

			require.NoError(t, RefundUserQuota(user.Id, 1_000, chargedAt))
			flushBonusWrites()

			assert.Equal(t, BonusStatusUsed, getBonusGrant(t, old.Id).Status)
			later := common.GetTimestamp() + 40*bonusTestDay
			require.NoError(t, ExpireUserBonusGrants(user.Id, later, later))
			assert.Equal(t, 101_000, getUserQuotaFromDB(t, user.Id))
		})
	}
}

// In batch mode the usage this node queued before a wallet grant is written
// and settled before the grant, so it never consumes the new reward.
func TestWalletGrantSettlesQueuedUsageFirst(t *testing.T) {
	for _, grant := range []struct {
		name        string
		balance     int
		spend       int
		grantReward func(t *testing.T, userId int)
		reward      int64
		wantQuota   int
	}{
		{
			name: "check-in", balance: 100_000, spend: 5_000, reward: 1_000, wantQuota: 95_000,
			grantReward: func(t *testing.T, userId int) {
				_, err := UserCheckin(userId)
				require.NoError(t, err)
			},
		},
		{
			// 200,000 - 150,000 + 500,000 top-up; the 100,000 bonus expires unused.
			name: "invitee bonus at a top-up", balance: 200_000, spend: 150_000, reward: 100_000, wantQuota: 550_000,
			grantReward: func(t *testing.T, userId int) {
				seedTestTopUp(t, TopUp{UserId: userId, Amount: 1, Money: 7, PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending})
				var stored TopUp
				require.NoError(t, DB.Where("user_id = ? AND status = ?", userId, common.TopUpStatusPending).Take(&stored).Error)
				require.NoError(t, ManualCompleteTopUp(stored.TradeNo, "127.0.0.1"))
			},
		},
	} {
		for _, mode := range bonusWriteModes {
			t.Run(grant.name+"/"+mode, func(t *testing.T) {
				setupBonusTest(t)
				common.InviteRechargeThresholdQuota = 500_000
				inviter := createReserveTestUser(t, 0)
				user := createReserveTestUser(t, grant.balance)
				require.NoError(t, DB.Create(&InviteReward{InviteeId: user.Id, InviterId: inviter.Id, InviterQuota: 250_000, InviteeQuota: 100_000, Status: InviteRewardStatusPending, CreatedAt: common.GetTimestamp()}).Error)
				useBonusWriteMode(t, mode, user.Id)
				require.NoError(t, DecreaseUserQuota(user.Id, grant.spend, false))

				grant.grantReward(t, user.Id)
				flushBonusWrites()

				var reward BonusGrant
				require.NoError(t, DB.Where("user_id = ?", user.Id).Take(&reward).Error)
				require.NoError(t, ExpireUserBonusGrants(user.Id, reward.ExpiresAt, reward.ExpiresAt))
				after := getBonusGrant(t, reward.Id)
				assert.Equal(t, BonusStatusExpired, after.Status)
				assert.Equal(t, grant.reward, after.DeductedQuota)
				assert.Equal(t, grant.wantQuota, getUserQuotaFromDB(t, user.Id))
			})
		}
	}
}

// A refund of a charge attributed by an earlier settlement than the last one
// is paid balance: it is never given to the rewards the last settlement
// consumed with real spending.
func TestRefundBeforeTheLastSettlementWindowStaysPaid(t *testing.T) {
	setupBonusTest(t)
	now := common.GetTimestamp()
	user := createReserveTestUser(t, 100_000)
	require.NoError(t, DecreaseUserQuota(user.Id, 5_000, false))
	first := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 1_000, 30, now-900)
	require.NoError(t, DecreaseUserQuota(user.Id, 1_000, false))
	second := grantTestBonus(t, user.Id, BonusSourceCheckinFree, 1_000, 30, now-800)
	require.Equal(t, BonusStatusUsed, getBonusGrant(t, first.Id).Status)

	require.NoError(t, RefundUserQuota(user.Id, 5_000, now-1_000))

	assert.Equal(t, BonusStatusUsed, getBonusGrant(t, first.Id).Status)
	deadline := second.ExpiresAt
	require.NoError(t, ExpireUserBonusGrants(user.Id, deadline, deadline))
	assert.EqualValues(t, 1_000, getBonusGrant(t, second.Id).DeductedQuota)
	assert.Equal(t, 100_000, getUserQuotaFromDB(t, user.Id))
}

// Every users write inside a ledger transaction goes by primary key, never
// through the soft-delete scope: on MySQL a scoped UPDATE locks the
// deleted_at index after the account lock already holds the primary key,
// the reverse of a plain scoped UPDATE of the same user (a relay debit, the
// invite registration count), and the two deadlock.
func TestLedgerTransactionsWriteUsersByPrimaryKey(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, userId int)
	}{
		{name: "invitation balance transfer", run: func(t *testing.T, userId int) {
			require.NoError(t, (&User{Id: userId}).TransferAffQuotaToQuota(750_000))
		}},
		{name: "refund across a settlement", run: func(t *testing.T, userId int) {
			require.NoError(t, RefundUserQuota(userId, 100, common.GetTimestamp()-3600))
		}},
		{name: "expiry sweep", run: func(t *testing.T, userId int) {
			later := common.GetTimestamp() + 31*bonusTestDay
			require.NoError(t, ExpireUserBonusGrants(userId, later, later))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupBonusTest(t)
			user := createReserveTestUser(t, 1_000)
			require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("aff_quota", 500_000).Error)
			require.NoError(t, DecreaseUserQuota(user.Id, 100, false))
			grantTestBonus(t, user.Id, BonusSourceInviter, 500_000, 30, common.GetTimestamp())
			grantTestBonus(t, user.Id, BonusSourceCheckinFree, 1_000, 30, common.GetTimestamp())
			var scoped []string
			const callback = "bonus_test:scoped_user_updates"
			require.NoError(t, DB.Callback().Update().After("gorm:update").Register(callback, func(db *gorm.DB) {
				_, inTransaction := db.Statement.ConnPool.(gorm.TxCommitter)
				if inTransaction && db.Statement.Table == "users" && strings.Contains(db.Statement.SQL.String(), "deleted_at") {
					scoped = append(scoped, db.Statement.SQL.String())
				}
			}))
			t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })

			tc.run(t, user.Id)

			assert.Empty(t, scoped)
		})
	}
}
