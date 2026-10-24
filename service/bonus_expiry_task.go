package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	bonusExpiryTickInterval = 1 * time.Minute
	bonusExpiryTimeBudget   = 50 * time.Second
	bonusExpiryUserBatch    = 200
	// bonusExpiryGraceSeconds keeps the sweep behind the clock so a wallet use
	// right before a deadline is always written before the grant is judged.
	bonusExpiryGraceSeconds = 60
)

var (
	bonusExpiryOnce    sync.Once
	bonusExpiryRunning atomic.Bool

	// The invite settings the last successful rescan ran with. Only the tick
	// holding bonusExpiryRunning reads or writes them.
	inviteRescanDone       bool
	inviteRescanThreshold  int
	inviteRescanCompliance bool
)

// StartBonusExpiryTask expires check-in and invite rewards on the master node
// once a minute and grants pending invite rewards after the invite threshold
// or the payment compliance changes. Several masters are safe: every change
// is guarded by the user's row lock and status compare-and-swap.
func StartBonusExpiryTask() {
	bonusExpiryOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("bonus expiry task started: tick=%s", bonusExpiryTickInterval))
			ticker := time.NewTicker(bonusExpiryTickInterval)
			defer ticker.Stop()

			runBonusExpiryOnce()
			for range ticker.C {
				runBonusExpiryOnce()
			}
		})
	})
}

func runBonusExpiryOnce() {
	if !bonusExpiryRunning.CompareAndSwap(false, true) {
		return
	}
	defer bonusExpiryRunning.Store(false)

	ctx := context.Background()
	budgetEnd := time.Now().Add(bonusExpiryTimeBudget)
	now := common.GetTimestamp()
	grace := int64(bonusExpiryGraceSeconds)
	if common.BatchUpdateEnabled {
		// A batched debit reaches the database up to one flush interval late.
		grace += 3 * int64(common.BatchUpdateInterval)
	}
	cutoff := now - grace
	groups := []struct {
		sources []string
		sliding bool
	}{
		{sources: []string{model.BonusSourceCheckinFree, model.BonusSourceCheckinPaid}, sliding: operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused},
		{sources: []string{model.BonusSourceInvitee, model.BonusSourceInviter}, sliding: common.InviteRewardExpireOnlyWhenUnused},
	}
	processed := 0
	for _, group := range groups {
		afterUserId := 0
		for {
			userIds, err := model.ListUsersWithDueBonusGrants(group.sources, group.sliding, cutoff, afterUserId, bonusExpiryUserBatch)
			if err != nil {
				logger.LogWarn(ctx, common.LogText("bonus expiry task failed to list due rewards: %v", err))
				break
			}
			for _, userId := range userIds {
				if time.Now().After(budgetEnd) {
					// The rest is picked up by the next tick.
					logger.LogInfo(ctx, common.LogText("bonus expiry task processed %d users, continuing next tick", processed))
					return
				}
				if err := model.ExpireUserBonusGrants(userId, now, cutoff); err != nil {
					logger.LogWarn(ctx, common.LogText("failed to expire the rewards of user %d: %v", userId, err))
				}
				processed++
			}
			if len(userIds) < bonusExpiryUserBatch {
				break
			}
			afterUserId = userIds[len(userIds)-1]
		}
	}
	if processed > 0 {
		logger.LogInfo(ctx, common.LogText("bonus expiry task processed %d users", processed))
	}
	rescanPendingInviteRewardsOnChange(ctx)
}

// rescanPendingInviteRewardsOnChange grants pending invite rewards on the first
// tick and whenever the invite threshold or the payment compliance changed, so
// a lowered threshold applies within a minute without a new recharge.
func rescanPendingInviteRewardsOnChange(ctx context.Context) {
	threshold := common.InviteRechargeThresholdQuota
	compliance := operation_setting.IsPaymentComplianceConfirmed()
	if inviteRescanDone && threshold == inviteRescanThreshold && compliance == inviteRescanCompliance {
		return
	}
	granted, err := model.RescanPendingInviteRewards()
	if err != nil {
		logger.LogWarn(ctx, common.LogText("failed to rescan pending invite rewards: %v", err))
		return
	}
	inviteRescanDone, inviteRescanThreshold, inviteRescanCompliance = true, threshold, compliance
	if granted > 0 {
		logger.LogInfo(ctx, common.LogText("granted %d pending invite rewards", granted))
	}
}
