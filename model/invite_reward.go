package model

import (
	"errors"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	InviteRewardStatusPending = "pending"
	InviteRewardStatusGranted = "granted"

	inviteRewardRescanBatchSize = 500
)

var (
	errInviteRewardAlreadyGranted = errors.New("invite reward already granted")
	errInviteRewardInviteeGone    = errors.New("invitee no longer exists")
)

// inviteeExistsCondition keeps the invite_rewards rows whose invitee account
// still exists: a deleted invitee's rewards can never be granted (deleted
// users are never restored), so they are neither shown nor retried.
const inviteeExistsCondition = "EXISTS (SELECT 1 FROM users WHERE users.id = invite_rewards.invitee_id AND users.deleted_at IS NULL)"

// InviteReward records the two invite rewards of one registration. Both are
// snapshotted at registration and granted together, once, when the invitee's
// cumulative recharge first reaches InviteRechargeThresholdQuota (at once when
// it is 0). A pending reward has no expiry clock: the clock starts at grant.
type InviteReward struct {
	Id           int    `json:"id" gorm:"primaryKey;autoIncrement"`
	InviteeId    int    `json:"-" gorm:"not null;uniqueIndex:idx_invite_rewards_invitee"`
	InviterId    int    `json:"-" gorm:"not null;index:idx_invite_rewards_inviter_status,priority:1"`
	InviterQuota int64  `json:"inviter_quota" gorm:"type:bigint;not null"`
	InviteeQuota int64  `json:"invitee_quota" gorm:"type:bigint;not null"`
	Status       string `json:"status" gorm:"type:varchar(16);not null;index:idx_invite_rewards_inviter_status,priority:2;index:idx_invite_rewards_status"`
	CreatedAt    int64  `json:"created_at" gorm:"type:bigint;not null"`
	GrantedAt    int64  `json:"granted_at" gorm:"type:bigint;not null;default:0"`
}

func (InviteReward) TableName() string {
	return "invite_rewards"
}

// HandleInviteRegistration records the invite rewards of a new user signed up
// with inviterId's code and grants them when no recharge threshold applies.
// The inviter's registration count still moves at sign-up, as before.
func HandleInviteRegistration(inviteeId int, inviterId int) {
	inviterQuota, inviteeQuota := int64(common.QuotaForInviter), int64(common.QuotaForInvitee)
	if inviterQuota <= 0 && inviteeQuota <= 0 {
		return
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&InviteReward{
			InviteeId:    inviteeId,
			InviterId:    inviterId,
			InviterQuota: max(inviterQuota, 0),
			InviteeQuota: max(inviteeQuota, 0),
			Status:       InviteRewardStatusPending,
			CreatedAt:    common.GetTimestamp(),
		})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if inviterQuota <= 0 {
			return nil
		}
		return tx.Model(&User{}).Where("id = ?", inviterId).Update("aff_count", gorm.Expr("aff_count + ?", 1)).Error
	})
	if err != nil {
		common.SysError(common.LogText("failed to record the invite reward of user %d: %s", inviteeId, err.Error()))
		return
	}
	if err := GrantInviteRewardIfQualified(inviteeId); err != nil {
		common.SysError(common.LogText("failed to grant the invite reward of user %d: %s", inviteeId, err.Error()))
	}
}

// GrantInviteRewardIfQualified grants the invitee's pending invite rewards when
// the cumulative recharge has reached the current threshold. It runs after
// every committed recharge, after the invitee's check-in and in the sweep's
// rescan; the status compare-and-swap grants each reward exactly once.
func GrantInviteRewardIfQualified(inviteeId int) error {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return nil
	}
	var reward InviteReward
	err := DB.Where("invitee_id = ? AND status = ?", inviteeId, InviteRewardStatusPending).Take(&reward).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if threshold := int64(common.InviteRechargeThresholdQuota); threshold > 0 {
		recharged, err := GetUserRechargeQuota(inviteeId)
		if err != nil {
			return err
		}
		if recharged < threshold {
			return nil
		}
	}
	_, err = grantPendingInviteReward(&reward)
	return err
}

// RescanPendingInviteRewards grants every pending invite reward whose invitee
// has reached the current threshold, for a lowered threshold, re-enabled
// compliance or a recharge whose hook did not run. It returns how many were
// granted.
func RescanPendingInviteRewards() (int, error) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return 0, nil
	}
	threshold := int64(common.InviteRechargeThresholdQuota)
	granted := 0
	lastId := 0
	for {
		var rewards []InviteReward
		if err := DB.Where("status = ? AND id > ?", InviteRewardStatusPending, lastId).
			Where(inviteeExistsCondition).
			Order("id").Limit(inviteRewardRescanBatchSize).Find(&rewards).Error; err != nil {
			return granted, err
		}
		if len(rewards) == 0 {
			return granted, nil
		}
		lastId = rewards[len(rewards)-1].Id
		recharged := map[int]int64{}
		if threshold > 0 {
			inviteeIds := make([]int, 0, len(rewards))
			for _, reward := range rewards {
				inviteeIds = append(inviteeIds, reward.InviteeId)
			}
			var err error
			if recharged, err = GetUsersRechargeQuota(inviteeIds); err != nil {
				return granted, err
			}
		}
		for i := range rewards {
			if threshold > 0 && recharged[rewards[i].InviteeId] < threshold {
				continue
			}
			ok, err := grantPendingInviteReward(&rewards[i])
			if err != nil {
				common.SysError(common.LogText("failed to grant the invite reward of user %d: %s", rewards[i].InviteeId, err.Error()))
				continue
			}
			if ok {
				granted++
			}
		}
		if len(rewards) < inviteRewardRescanBatchSize {
			return granted, nil
		}
	}
}

// grantPendingInviteReward credits both rewards of a pending record. The
// invitee bonus goes to the wallet, the inviter reward to the invitation
// balance; each gets an expiring grant unless InviteRewardExpireDays is 0. A
// record granted concurrently, or whose invitee is gone, is left alone and
// reported as not granted.
func grantPendingInviteReward(reward *InviteReward) (bool, error) {
	days := common.InviteRewardExpireDays
	if days > 0 && reward.InviteeQuota > 0 {
		// The invitee's usage this node still holds in the batch is settled
		// before the bonus exists, as in a check-in.
		flushUserBatchedDeltas(reward.InviteeId)
	}
	var inviteeGrant, inviterGrant *BonusGrant
	var inviteeGranted, inviterGranted bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&InviteReward{}).
			Where("id = ? AND status = ?", reward.Id, InviteRewardStatusPending).
			Updates(map[string]any{"status": InviteRewardStatusGranted, "granted_at": common.GetTimestamp()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errInviteRewardAlreadyGranted
		}
		userIds := []int{reward.InviteeId, reward.InviterId}
		slices.Sort(userIds)
		accounts := make(map[int]*bonusAccount, 2)
		for _, id := range slices.Compact(userIds) {
			acct, err := lockBonusAccount(tx, id)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if !acct.DeletedAt.Valid {
				accounts[id] = acct
			}
		}
		invitee := accounts[reward.InviteeId]
		if invitee == nil {
			return errInviteRewardInviteeGone
		}
		now := common.GetTimestamp()
		var err error
		if reward.InviteeQuota > 0 {
			spec := bonusGrantSpec{Source: BonusSourceInvitee, Amount: reward.InviteeQuota, ExpireDays: days, RefType: BonusRefInvite, RefId: reward.Id}
			if inviteeGrant, err = grantWalletBonusTx(tx, invitee, spec, now); err != nil {
				return err
			}
			inviteeGranted = true
		}
		if inviter := accounts[reward.InviterId]; reward.InviterQuota > 0 && inviter != nil {
			spec := bonusGrantSpec{Source: BonusSourceInviter, Amount: reward.InviterQuota, ExpireDays: days, RefType: BonusRefInvite, RefId: reward.Id}
			if inviterGrant, err = grantAffBonusTx(tx, inviter, spec, now); err != nil {
				return err
			}
			inviterGranted = true
		}
		return nil
	})
	if errors.Is(err, errInviteRewardAlreadyGranted) || errors.Is(err, errInviteRewardInviteeGone) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if inviteeGranted {
		if err := cacheIncrUserQuota(reward.InviteeId, reward.InviteeQuota); err != nil {
			common.SysError(common.LogText("failed to sync the invitation bonus of user %d to the cache: %s", reward.InviteeId, err.Error()))
		}
		RecordLog(reward.InviteeId, LogTypeSystem, bonusGrantMessage([3]string{
			"Invitation code bonus {{quota}}",
			"Invitation code bonus {{quota}}, expires at {{expires_at}}",
			"Invitation code bonus {{quota}}, expires if unused for {{days}} days (currently {{expires_at}})",
		}, reward.InviteeQuota, inviteeGrant, nil))
	}
	if inviterGranted {
		RecordLog(reward.InviterId, LogTypeSystem, bonusGrantMessage([3]string{
			"Bonus for inviting a user {{quota}}",
			"Bonus for inviting a user {{quota}}, expires at {{expires_at}} unless transferred",
			"Bonus for inviting a user {{quota}}, expires if not transferred for {{days}} days (currently {{expires_at}})",
		}, reward.InviterQuota, inviterGrant, nil))
	}
	return true, nil
}
