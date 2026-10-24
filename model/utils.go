package model

import (
	"errors"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeChannelUsedQuota
	BatchUpdateTypeRequestCount
	// BatchUpdateTypeWalletSpent holds the wallet usage debits (W) per user and
	// BatchUpdateTypeWalletUse their number; BatchUpdateTypeWalletRefund holds
	// the usage refunds with a known charge time and BatchUpdateTypeWalletRefundAt
	// the earliest of those charge times (a minimum, not a sum). They are only
	// written together with the matching quota delta, by addWalletUsageDebitRecord
	// and addWalletRefundRecord.
	BatchUpdateTypeWalletSpent
	BatchUpdateTypeWalletUse
	BatchUpdateTypeWalletRefund
	BatchUpdateTypeWalletRefundAt
	BatchUpdateTypeCount // if you add a new type, you need to add a new map and a new lock
)

var batchUpdateStores []map[int]int
var batchUpdateLocks []sync.Mutex

func init() {
	for range BatchUpdateTypeCount {
		batchUpdateStores = append(batchUpdateStores, make(map[int]int))
		batchUpdateLocks = append(batchUpdateLocks, sync.Mutex{})
	}
}

func InitBatchUpdater() {
	gopool.Go(func() {
		for {
			time.Sleep(time.Duration(common.BatchUpdateInterval) * time.Second)
			batchUpdate()
		}
	})
}

func addNewRecord(type_ int, id int, value int) {
	batchUpdateLocks[type_].Lock()
	defer batchUpdateLocks[type_].Unlock()
	addLocked(type_, id, value)
}

// addWalletUsageDebitRecord queues a wallet usage debit so that the quota
// delta and its usage delta always land in the same flush. Locks are taken in
// ascending store order, the same order batchUpdate uses.
func addWalletUsageDebitRecord(id int, quota int) {
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	batchUpdateLocks[BatchUpdateTypeWalletSpent].Lock()
	batchUpdateLocks[BatchUpdateTypeWalletUse].Lock()
	defer batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	defer batchUpdateLocks[BatchUpdateTypeWalletSpent].Unlock()
	defer batchUpdateLocks[BatchUpdateTypeWalletUse].Unlock()
	addLocked(BatchUpdateTypeUserQuota, id, -quota)
	addLocked(BatchUpdateTypeWalletSpent, id, quota)
	addLocked(BatchUpdateTypeWalletUse, id, 1)
}

// addWalletRefundRecord queues a usage refund of a charge applied at or after
// chargedAt (> 0). The flush books all of a user's refunds by the earliest
// charge time, which can only make more of them paid balance.
func addWalletRefundRecord(id int, quota int, chargedAt int64) {
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	batchUpdateLocks[BatchUpdateTypeWalletRefund].Lock()
	batchUpdateLocks[BatchUpdateTypeWalletRefundAt].Lock()
	defer batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	defer batchUpdateLocks[BatchUpdateTypeWalletRefund].Unlock()
	defer batchUpdateLocks[BatchUpdateTypeWalletRefundAt].Unlock()
	addLocked(BatchUpdateTypeUserQuota, id, quota)
	addLocked(BatchUpdateTypeWalletRefund, id, quota)
	earliest := batchUpdateStores[BatchUpdateTypeWalletRefundAt]
	if at, ok := earliest[id]; !ok || int(chargedAt) < at {
		earliest[id] = int(chargedAt)
	}
}

// addLocked adds value to a store entry with overflow saturation. The caller
// holds the store's lock.
func addLocked(type_ int, id int, value int) {
	old, ok := batchUpdateStores[type_][id]
	if !ok {
		batchUpdateStores[type_][id] = value
		return
	}

	sum := old + value
	if (value > 0 && sum < old) || (value < 0 && sum > old) {
		common.SysError(common.LogText("batch update overflow: type=%d id=%d old=%d value=%d", type_, id, old, value))
		if value > 0 {
			sum = math.MaxInt
		} else {
			sum = math.MinInt
		}
	}
	batchUpdateStores[type_][id] = sum
}

func batchUpdate() {
	// check if there's any data to update
	hasData := false
	for i := range BatchUpdateTypeCount {
		batchUpdateLocks[i].Lock()
		if len(batchUpdateStores[i]) > 0 {
			hasData = true
			batchUpdateLocks[i].Unlock()
			break
		}
		batchUpdateLocks[i].Unlock()
	}

	if !hasData {
		return
	}

	common.SysLog(common.LogText("batch update started"))
	// Swap every store under all locks (ascending order) so a wallet quota
	// delta and its usage delta can never be split across two flushes.
	stores := make([]map[int]int, BatchUpdateTypeCount)
	for i := range BatchUpdateTypeCount {
		batchUpdateLocks[i].Lock()
	}
	for i := range BatchUpdateTypeCount {
		stores[i] = batchUpdateStores[i]
		batchUpdateStores[i] = make(map[int]int)
	}
	for i := BatchUpdateTypeCount - 1; i >= 0; i-- {
		batchUpdateLocks[i].Unlock()
	}

	for i, store := range stores {
		if i == BatchUpdateTypeUserQuota || i == BatchUpdateTypeUsedQuota || i == BatchUpdateTypeRequestCount ||
			i == BatchUpdateTypeWalletSpent || i == BatchUpdateTypeWalletUse ||
			i == BatchUpdateTypeWalletRefund || i == BatchUpdateTypeWalletRefundAt {
			continue
		}
		for key, value := range store {
			switch i {
			case BatchUpdateTypeTokenQuota:
				err := increaseTokenQuota(key, value)
				if err != nil {
					common.SysLog(common.LogText("failed to batch update token quota: %s", err.Error()))
				}
			case BatchUpdateTypeChannelUsedQuota:
				updateChannelUsedQuota(key, value)
			}
		}
	}

	userQuotaStore := stores[BatchUpdateTypeUserQuota]
	usedQuotaStore := stores[BatchUpdateTypeUsedQuota]
	requestCountStore := stores[BatchUpdateTypeRequestCount]
	walletSpentStore := stores[BatchUpdateTypeWalletSpent]
	walletUseStore := stores[BatchUpdateTypeWalletUse]
	walletRefundStore := stores[BatchUpdateTypeWalletRefund]
	walletRefundAtStore := stores[BatchUpdateTypeWalletRefundAt]

	userIDs := make(map[int]struct{}, len(userQuotaStore)+len(usedQuotaStore)+len(requestCountStore))
	for _, store := range []map[int]int{userQuotaStore, usedQuotaStore, requestCountStore, walletSpentStore, walletUseStore, walletRefundStore} {
		for key := range store {
			userIDs[key] = struct{}{}
		}
	}
	for key := range userIDs {
		updateUserQuotaUsedQuotaAndRequestCount(key, batchedUserDeltas{
			quota:        userQuotaStore[key],
			usedQuota:    usedQuotaStore[key],
			requestCount: requestCountStore[key],
			walletSpent:  walletSpentStore[key],
			walletUses:   walletUseStore[key],
			refund:       walletRefundStore[key],
			refundAt:     walletRefundAtStore[key],
		})
	}
	common.SysLog(common.LogText("batch update finished"))
}

// batchedUserStores are the stores keyed by user id, in ascending lock order.
var batchedUserStores = []int{
	BatchUpdateTypeUserQuota, BatchUpdateTypeUsedQuota, BatchUpdateTypeRequestCount,
	BatchUpdateTypeWalletSpent, BatchUpdateTypeWalletUse, BatchUpdateTypeWalletRefund, BatchUpdateTypeWalletRefundAt,
}

// flushUserBatchedDeltas writes this process's batched deltas of one user now,
// exactly as the next batch flush would. A wallet grant calls it before its
// transaction, so wallet usage this node queued before the grant is settled
// before the grant exists. The caller must not hold a transaction.
func flushUserBatchedDeltas(userId int) {
	if !common.BatchUpdateEnabled {
		return
	}
	values := make(map[int]int, len(batchedUserStores))
	for _, type_ := range batchedUserStores {
		batchUpdateLocks[type_].Lock()
	}
	for _, type_ := range batchedUserStores {
		values[type_] = batchUpdateStores[type_][userId]
		delete(batchUpdateStores[type_], userId)
	}
	for _, type_ := range slices.Backward(batchedUserStores) {
		batchUpdateLocks[type_].Unlock()
	}
	updateUserQuotaUsedQuotaAndRequestCount(userId, batchedUserDeltas{
		quota:        values[BatchUpdateTypeUserQuota],
		usedQuota:    values[BatchUpdateTypeUsedQuota],
		requestCount: values[BatchUpdateTypeRequestCount],
		walletSpent:  values[BatchUpdateTypeWalletSpent],
		walletUses:   values[BatchUpdateTypeWalletUse],
		refund:       values[BatchUpdateTypeWalletRefund],
		refundAt:     values[BatchUpdateTypeWalletRefundAt],
	})
}

func RecordExist(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func shouldUpdateRedis(fromDB bool, err error) bool {
	return common.RedisEnabled && fromDB && err == nil
}
