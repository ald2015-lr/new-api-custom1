package model

import (
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createReserveTestUser(t *testing.T, quota int) User {
	t.Helper()
	user := User{
		Username:    "reserve-user-" + common.GetRandomString(6),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       quota,
		AffCode:     "reserve-aff-" + common.GetRandomString(8),
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func createReserveTestToken(t *testing.T, remainQuota int) Token {
	t.Helper()
	token := Token{
		UserId:      1,
		Key:         "reserve-token-" + common.GetRandomString(8),
		Name:        "reserve-test",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: remainQuota,
	}
	require.NoError(t, token.Insert())
	return token
}

func getUserQuotaFromDB(t *testing.T, id int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").First(&user, id).Error)
	return user.Quota
}

func getTokenFromDB(t *testing.T, id int) Token {
	t.Helper()
	var token Token
	require.NoError(t, DB.First(&token, id).Error)
	return token
}

func resetBatchUpdateTestState(t *testing.T) {
	t.Helper()
	oldBatchEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	for i := range BatchUpdateTypeCount {
		batchUpdateLocks[i].Lock()
		batchUpdateStores[i] = make(map[int]int)
		batchUpdateLocks[i].Unlock()
	}
	t.Cleanup(func() {
		common.BatchUpdateEnabled = oldBatchEnabled
		for i := range BatchUpdateTypeCount {
			batchUpdateLocks[i].Lock()
			batchUpdateStores[i] = make(map[int]int)
			batchUpdateLocks[i].Unlock()
		}
	})
}

func TestTryReserveQuotaWithoutRedis(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)

	user := createReserveTestUser(t, 100)
	reserved, err := TryReserveUserQuota(user.Id, 60)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 41)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	token := createReserveTestToken(t, 80)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 25, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reloaded := getTokenFromDB(t, token.Id)
	assert.Equal(t, 55, reloaded.RemainQuota)
	assert.Equal(t, 25, reloaded.UsedQuota)

	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 56, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 55, getTokenFromDB(t, token.Id).RemainQuota)
}

func TestRedisBatchReserveNeverFallsBackToStaleDatabaseBalance(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 10)
	reserved, err := TryReserveUserQuota(user.Id, 8)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 10, getUserQuotaFromDB(t, user.Id), "batch delta is not flushed yet")

	reserved, err = TryReserveUserQuota(user.Id, 3)
	require.NoError(t, err)
	assert.False(t, reserved, "stale DB balance must not authorize a second spend")
	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, cachedUser.Quota)

	token := createReserveTestToken(t, 9)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 3, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 9, getTokenFromDB(t, token.Id).RemainQuota)

	batchUpdate()
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id))
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 2, reloadedToken.RemainQuota)
	assert.Equal(t, 7, reloadedToken.UsedQuota)
}

func TestBatchUpdateAccumulatesTwoMaximumRequestCharges(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, common.MaxQuota*2+100)
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))

	batchUpdate()
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))
}

func TestBatchUpdateAccumulatorSaturatesOverflow(t *testing.T) {
	resetBatchUpdateTestState(t)

	addNewRecord(BatchUpdateTypeUserQuota, 1, math.MaxInt)
	addNewRecord(BatchUpdateTypeUserQuota, 1, 1)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	assert.Equal(t, math.MaxInt, batchUpdateStores[BatchUpdateTypeUserQuota][1])
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()

	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	batchUpdateStores[BatchUpdateTypeUserQuota] = make(map[int]int)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	addNewRecord(BatchUpdateTypeUserQuota, 1, math.MinInt)
	addNewRecord(BatchUpdateTypeUserQuota, 1, -1)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	assert.Equal(t, math.MinInt, batchUpdateStores[BatchUpdateTypeUserQuota][1])
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
}

func TestReserveFallsBackToDatabaseWhenRedisIsUnavailable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 20)
	require.NoError(t, populateUserCache(user))
	server.Close()

	// Redis 故障时降级为数据库条件更新：服务保持可用且不会超扣。
	reserved, err := TryReserveUserQuota(user.Id, 5)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 16)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))
}

func TestSynchronousReserveCompensatesCacheWhenPersistenceFails(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 10)
	require.NoError(t, populateUserCache(user))
	require.NoError(t, DB.Delete(&user).Error)

	reserved, err := TryReserveUserQuota(user.Id, 6)
	assert.False(t, reserved)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cached, cacheErr := cacheGetUserBase(user.Id)
	require.NoError(t, cacheErr)
	assert.Equal(t, 10, cached.Quota)

	token := createReserveTestToken(t, 12)
	_, err = GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	require.NoError(t, DB.Delete(&token).Error)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	assert.False(t, reserved)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cachedToken, cacheErr := cacheGetTokenByKey(token.Key)
	require.NoError(t, cacheErr)
	assert.Equal(t, 12, cachedToken.RemainQuota)
	assert.Zero(t, cachedToken.UsedQuota)
}

func TestTokenCacheInitPreservesLiveQuotaAndFenceBlocksStaleSnapshot(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	token := createReserveTestToken(t, 100)
	loaded, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	stale := *loaded

	result, err := cacheApplyTokenQuotaDelta(token.Id, token.Key, -70)
	require.NoError(t, err)
	require.Equal(t, cacheQuotaOK, result)

	// 已存在的哈希只刷新 TTL：数据库快照不得覆盖已被原子预扣的余额。
	code, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Equal(t, 2, code)
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 30, cached.RemainQuota)

	// 变更期间：fence 删除缓存并拦截并发读者手中的过期快照。
	require.NoError(t, invalidateTokenCacheForMutation(token.Key))
	code, err = cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code, "the pre-mutation snapshot must not be published while fenced")
	_, err = cacheGetTokenByKey(token.Key)
	assert.Error(t, err)

	// fence 过期后可重新从数据库水合。
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	fresh, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 100, fresh.RemainQuota)
	cached, err = cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 100, cached.RemainQuota)
}

type walletLedger struct {
	Quota            int
	WalletSpentQuota int64
	WalletUsedAt     int64
	BonusSpentCursor int64
	BonusSettledAt   int64
}

func getWalletLedger(t *testing.T, id int) walletLedger {
	t.Helper()
	var ledger walletLedger
	require.NoError(t, DB.Model(&User{}).
		Select("quota, wallet_spent_quota, wallet_used_at, bonus_spent_cursor, bonus_settled_at").
		Where("id = ?", id).Take(&ledger).Error)
	return ledger
}

// Every wallet usage debit raises the usage counter and the last-use time in
// the statement that debits the wallet, whichever reservation path runs.
func TestWalletUsageDebitsRecordUsage(t *testing.T) {
	cases := []struct {
		name  string
		debit func(t *testing.T, userId int)
	}{
		{name: "direct debit", debit: func(t *testing.T, userId int) {
			require.NoError(t, DecreaseUserQuota(userId, 300, false))
		}},
		{name: "reservation without redis", debit: func(t *testing.T, userId int) {
			reserved, err := TryReserveUserQuota(userId, 300)
			require.NoError(t, err)
			require.True(t, reserved)
		}},
		{name: "reservation through the redis cache", debit: func(t *testing.T, userId int) {
			useUserCacheMiniRedis(t)
			_, err := GetUserCache(userId)
			require.NoError(t, err)
			reserved, err := TryReserveUserQuota(userId, 300)
			require.NoError(t, err)
			require.True(t, reserved)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			resetBatchUpdateTestState(t)
			user := createReserveTestUser(t, 1000)
			before := common.GetTimestamp()

			tc.debit(t, user.Id)

			ledger := getWalletLedger(t, user.Id)
			assert.Equal(t, 700, ledger.Quota)
			assert.EqualValues(t, 300, ledger.WalletSpentQuota)
			assert.GreaterOrEqual(t, ledger.WalletUsedAt, before)
		})
	}
}

// A usage refund lowers the usage counter only when its charge came after the
// last bonus settlement; otherwise it is paid balance. Refunds are never a use.
func TestRefundUserQuotaClassifiesByChargeTime(t *testing.T) {
	now := common.GetTimestamp()
	cases := []struct {
		name      string
		chargedAt int64
		wantSpent int64
	}{
		{name: "charge after the last settlement", chargedAt: now, wantSpent: 700},
		{name: "charge before the last settlement", chargedAt: now - 3600, wantSpent: 1000},
		{name: "charge time unknown", chargedAt: 0, wantSpent: 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			resetBatchUpdateTestState(t)
			user := createReserveTestUser(t, 0)
			require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]any{
				"wallet_spent_quota": 1000, "bonus_spent_cursor": 1000, "bonus_settled_at": now - 60,
			}).Error)

			require.NoError(t, RefundUserQuota(user.Id, 300, tc.chargedAt))

			ledger := getWalletLedger(t, user.Id)
			assert.Equal(t, 300, ledger.Quota)
			assert.Equal(t, tc.wantSpent, ledger.WalletSpentQuota)
			assert.Zero(t, ledger.WalletUsedAt)
		})
	}
}

func TestRefundUserQuotaErrorsMatchIncreaseUserQuota(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	user := createReserveTestUser(t, common.MaxWalletQuota-100)
	now := common.GetTimestamp()

	assert.ErrorIs(t, IncreaseUserQuota(user.Id, 200, true), ErrWalletQuotaLimitExceeded)
	assert.ErrorIs(t, RefundUserQuota(user.Id, 200, now), ErrWalletQuotaLimitExceeded)
	assert.ErrorIs(t, IncreaseUserQuota(user.Id+1000, 200, true), gorm.ErrRecordNotFound)
	assert.ErrorIs(t, RefundUserQuota(user.Id+1000, 200, now), gorm.ErrRecordNotFound)
	assert.Equal(t, common.MaxWalletQuota-100, getUserQuotaFromDB(t, user.Id))
}

// In batch mode a wallet debit and its usage delta land in the same flush; a
// flush with a debit is a use even when a refund nets it to zero, a
// refund-only flush is not.
func TestBatchFlushMovesWalletUsageWithQuota(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true
	now := common.GetTimestamp()

	netted := createReserveTestUser(t, 1000)
	require.NoError(t, DecreaseUserQuota(netted.Id, 300, false))
	require.NoError(t, RefundUserQuota(netted.Id, 300, now))
	partial := createReserveTestUser(t, 1000)
	require.NoError(t, DecreaseUserQuota(partial.Id, 200, false))
	require.NoError(t, RefundUserQuota(partial.Id, 50, now))
	refundOnly := createReserveTestUser(t, 1000)
	require.NoError(t, RefundUserQuota(refundOnly.Id, 100, 0))
	require.Equal(t, walletLedger{Quota: 1000}, getWalletLedger(t, partial.Id), "nothing is written before the flush")

	batchUpdate()

	ledger := getWalletLedger(t, netted.Id)
	assert.Equal(t, 1000, ledger.Quota)
	assert.Zero(t, ledger.WalletSpentQuota)
	assert.GreaterOrEqual(t, ledger.WalletUsedAt, now)
	ledger = getWalletLedger(t, partial.Id)
	assert.Equal(t, 850, ledger.Quota)
	assert.EqualValues(t, 150, ledger.WalletSpentQuota)
	assert.GreaterOrEqual(t, ledger.WalletUsedAt, now)
	ledger = getWalletLedger(t, refundOnly.Id)
	assert.Equal(t, 1100, ledger.Quota)
	assert.Zero(t, ledger.WalletSpentQuota)
	assert.Zero(t, ledger.WalletUsedAt)
}
