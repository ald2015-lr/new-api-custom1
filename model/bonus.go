package model

import (
	"errors"
	"maps"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"gorm.io/gorm"
)

// Expiring bonus ledger. Check-in rewards and the invitee bonus are wallet
// grants, the inviter reward is an aff (invitation balance) grant. A grant row
// exists only for a reward that expires; legacy balances and rewards that
// never expire have no row and never expire.
//
// Wallet grants are consumed first: users.wallet_spent_quota (W) counts the
// net wallet usage and users.bonus_spent_cursor the part of W already
// attributed to grants. Settlement attributes W - cursor to the active wallet
// grants, earliest fixed deadline (expires_at) first, and runs only while the
// user's users row is locked: before a wallet grant is created and in the
// expiry sweep. Aff grants are consumed by the invitation balance transfer.
// The consumption order never depends on the expire-only-when-unused
// switches, so usage settled after a switch changed is attributed exactly as
// it would have been before; the switches only decide when a grant expires.
//
// A settlement remembers what it attributed (users.bonus_settled_from,
// bonus_settled_paid and each grant's settled_take) so that a usage refund of
// a charge it attributed gives the bonus back instead of turning it into paid
// balance (see bookWalletRefundTx). Only the last settlement is remembered,
// and a charge cannot be ordered against it more precisely than the clock
// skew, so a refund of a charge an earlier settlement attributed, or of one
// made right after the last, may leave a bonus counted as used. Both errors
// favour the user and are documented in DEPLOY.md; matching a refund to an
// earlier settlement, or to the unattributed usage first, could take paid
// money.
//
// Locking rule: every mutation of a user's grants happens while that user's
// users row is locked in the same transaction (lockBonusAccount), and no plain
// SELECT runs before that lock (MySQL REPEATABLE READ fixes the read view at
// the first plain read). Grants are read with a plain SELECT, never FOR UPDATE:
// MySQL gap locks on the grant indexes could deadlock two users' inserts.
// Global lock order: checkins / invite_rewards row, then users rows in
// ascending id order, then bonus_grants.
const (
	BonusPoolWallet = "wallet"
	BonusPoolAff    = "aff"

	BonusStatusActive  = "active"
	BonusStatusUsed    = "used"
	BonusStatusExpired = "expired"

	BonusSourceCheckinFree = "checkin_free"
	BonusSourceCheckinPaid = "checkin_paid"
	BonusSourceInvitee     = "invitee"
	BonusSourceInviter     = "inviter"

	BonusRefCheckin = "checkin"
	BonusRefInvite  = "invite"
)

const (
	// bonusRefundSkewSeconds is the clock skew tolerated between the node that
	// charged a request and the node that settled the bonus ledger.
	bonusRefundSkewSeconds = 10
	bonusSecondsPerDay     = 86400
	bonusTimeLayout        = "2006-01-02 15:04"

	bonusSummaryGrantLimit         = 100
	bonusSummaryPendingInviteLimit = 20
)

var (
	errBonusSettleConflict = errors.New("bonus settlement cursor changed concurrently")
	errBonusGrantConflict  = errors.New("bonus grant changed concurrently")
)

type BonusGrant struct {
	Id            int    `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId        int    `json:"-" gorm:"not null;index:idx_bonus_grants_user_status,priority:1"`
	Source        string `json:"source" gorm:"type:varchar(32);not null;uniqueIndex:idx_bonus_grants_ref,priority:3"`
	Pool          string `json:"pool" gorm:"type:varchar(16);not null"`
	Status        string `json:"status" gorm:"type:varchar(16);not null;index:idx_bonus_grants_user_status,priority:2;index:idx_bonus_grants_sweep_fixed,priority:1;index:idx_bonus_grants_sweep_check,priority:1"`
	Amount        int64  `json:"amount" gorm:"type:bigint;not null"`
	Remaining     int64  `json:"remaining" gorm:"type:bigint;not null"` // unspent amount, kept for audit after expiry
	ExpireSeconds int64  `json:"-" gorm:"type:bigint;not null"`         // expiry days snapshot × 86400 (> 0)
	// ExpiresAt is CreatedAt + ExpireSeconds and never changes (fixed rule).
	ExpiresAt int64 `json:"-" gorm:"type:bigint;not null;index:idx_bonus_grants_sweep_fixed,priority:2"`
	// CheckAt is when the sweep next looks at the grant under the sliding rule:
	// it starts at ExpiresAt and only moves forward, never past the deadline.
	CheckAt       int64  `json:"-" gorm:"type:bigint;not null;index:idx_bonus_grants_sweep_check,priority:2"`
	LastUsedAt    int64  `json:"-" gorm:"type:bigint;not null;default:0"` // aff pool only: last transfer
	DeductedQuota int64  `json:"deducted_quota" gorm:"type:bigint;not null;default:0"`
	RefType       string `json:"-" gorm:"type:varchar(16);not null;uniqueIndex:idx_bonus_grants_ref,priority:1"` // checkin | invite
	RefId         int    `json:"-" gorm:"not null;uniqueIndex:idx_bonus_grants_ref,priority:2"`                  // checkins.id | invite_rewards.id
	CreatedAt     int64  `json:"created_at" gorm:"type:bigint;not null"`
	ClosedAt      int64  `json:"closed_at" gorm:"type:bigint;not null;default:0"`
	// SettledTake is what the wallet settlement at SettledAt took from the
	// grant and a later usage refund can still give back to it.
	SettledTake int64 `json:"-" gorm:"type:bigint;not null;default:0"`
	SettledAt   int64 `json:"-" gorm:"type:bigint;not null;default:0"`
}

func (BonusGrant) TableName() string {
	return "bonus_grants"
}

// bonusSliding reports whether grants of source expire only when unused.
func bonusSliding(source string) bool {
	switch source {
	case BonusSourceCheckinFree, BonusSourceCheckinPaid:
		return operation_setting.GetCheckinSetting().ExpireOnlyWhenUnused
	case BonusSourceInvitee, BonusSourceInviter:
		return common.InviteRewardExpireOnlyWhenUnused
	}
	return false
}

// effectiveDeadline is when the grant expires under the current rule. Under
// the sliding rule it is the last use (any wallet usage debit for a wallet
// grant, a transfer for an aff grant) plus the grant's own period, and never
// earlier than the fixed deadline.
func (g *BonusGrant) effectiveDeadline(walletUsedAt int64) int64 {
	if !bonusSliding(g.Source) {
		return g.ExpiresAt
	}
	lastUse := g.LastUsedAt
	if g.Pool == BonusPoolWallet {
		lastUse = walletUsedAt
	}
	return max(g.CreatedAt, lastUse) + g.ExpireSeconds
}

// BillingChargedAt returns the unix second to pass to RefundUserQuota for a
// charge made while serving a request that started at start: it is never later
// than the charge. A zero start is unknown and yields 0, which keeps every
// refund of that charge as paid balance.
func BillingChargedAt(start time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	return start.Unix()
}

// GetUserRechargeQuota returns the user's cumulative recharge in quota:
// successful top-ups valued by the credited-quota rule plus used redemption
// codes (also codes deleted after use). Subscription purchases, balance-paid
// subscriptions, admin adjustments, rewards and refunds never count.
func GetUserRechargeQuota(userId int) (int64, error) {
	totals, err := GetUsersRechargeQuota([]int{userId})
	if err != nil {
		return 0, err
	}
	return totals[userId], nil
}

// GetUsersRechargeQuota is GetUserRechargeQuota for several users at once.
func GetUsersRechargeQuota(userIds []int) (map[int]int64, error) {
	totals := make(map[int]int64, len(userIds))
	if len(userIds) == 0 {
		return totals, nil
	}
	var topUps []TopUp
	if err := DB.Model(&TopUp{}).
		Select("user_id", "amount", "money", "payment_method", "payment_provider").
		Where("user_id IN ? AND status = ? AND amount > 0", userIds, common.TopUpStatusSuccess).
		Find(&topUps).Error; err != nil {
		return nil, err
	}
	for i := range topUps {
		credited, err := topUpCreditedQuota(&topUps[i])
		if err != nil {
			common.SysError(common.LogText("cumulative recharge skips a top-up of user %d: %s", topUps[i].UserId, err.Error()))
			continue
		}
		totals[topUps[i].UserId] = min(totals[topUps[i].UserId]+int64(credited), common.MaxWalletQuota)
	}
	var redeemed []struct {
		UsedUserId int
		Total      int64
	}
	if err := DB.Unscoped().Model(&Redemption{}).
		Select("used_user_id, COALESCE(SUM(quota), 0) AS total").
		Where("used_user_id IN ? AND status = ?", userIds, common.RedemptionCodeStatusUsed).
		Group("used_user_id").
		Scan(&redeemed).Error; err != nil {
		return nil, err
	}
	for _, row := range redeemed {
		totals[row.UsedUserId] = min(totals[row.UsedUserId]+max(row.Total, 0), common.MaxWalletQuota)
	}
	return totals, nil
}

// bonusAccount is the part of a users row the bonus ledger reads.
type bonusAccount struct {
	Id               int
	Quota            int64
	AffQuota         int64
	WalletSpentQuota int64
	WalletUsedAt     int64
	BonusSpentCursor int64
	BonusSettledAt   int64
	BonusSettledFrom int64
	BonusSettledPaid int64
	DeletedAt        gorm.DeletedAt
}

const bonusAccountColumns = "id, quota, aff_quota, wallet_spent_quota, wallet_used_at, bonus_spent_cursor, bonus_settled_at, bonus_settled_from, bonus_settled_paid, deleted_at"

// lockBonusAccount locks the user's row (also a soft-deleted one) for the rest
// of the transaction. On MySQL and PostgreSQL the lock holds back the user's
// relay UPDATEs, so the usage counter read here is exact until commit; SQLite
// transactions are already serialized (BEGIN IMMEDIATE).
func lockBonusAccount(tx *gorm.DB, userId int) (*bonusAccount, error) {
	var acct bonusAccount
	if err := lockForUpdate(tx).Unscoped().Model(&User{}).Select(bonusAccountColumns).Where("id = ?", userId).Take(&acct).Error; err != nil {
		return nil, err
	}
	return &acct, nil
}

// activeBonusGrants reads a user's active grants of pool ("" for both pools),
// ordered by pool, fixed deadline and id: the order in which usage and
// transfers consume them, whatever the expire-only-when-unused switches say.
func activeBonusGrants(db *gorm.DB, userId int, pool string) ([]BonusGrant, error) {
	query := db.Where("user_id = ? AND status = ?", userId, BonusStatusActive)
	if pool != "" {
		query = query.Where("pool = ?", pool)
	}
	var grants []BonusGrant
	if err := query.Order("pool").Order("expires_at").Order("id").Find(&grants).Error; err != nil {
		return nil, err
	}
	return grants, nil
}

// attributeBonusUsage splits amount over grants in their order and returns
// how much each one covers; whatever is left was paid money.
func attributeBonusUsage(grants []BonusGrant, amount int64) []int64 {
	takes := make([]int64, len(grants))
	for i := range grants {
		if amount <= 0 {
			break
		}
		take := min(grants[i].Remaining, amount)
		if take <= 0 {
			continue
		}
		takes[i] = take
		amount -= take
	}
	return takes
}

// consumeBonusGrantTx lowers a grant's remainder; a grant used up is closed.
// settledAt > 0 marks the take as made by the wallet settlement at that time,
// so a refund of the usage it covered can give it back.
func consumeBonusGrantTx(tx *gorm.DB, grant *BonusGrant, take int64, now int64, settledAt int64) error {
	updates := map[string]any{"remaining": gorm.Expr("remaining - ?", take)}
	if take == grant.Remaining {
		updates["status"] = BonusStatusUsed
		updates["closed_at"] = now
	}
	if settledAt > 0 {
		updates["settled_take"] = take
		updates["settled_at"] = settledAt
	}
	result := tx.Model(&BonusGrant{}).
		Where("id = ? AND status = ? AND remaining >= ?", grant.Id, BonusStatusActive, take).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errBonusGrantConflict
	}
	grant.Remaining -= take
	return nil
}

// settleWalletBonusTx attributes the wallet usage not yet attributed to the
// user's active wallet grants. The caller holds lockBonusAccount and passes
// the time read after taking it. The settlement records its window (usage
// charged after the previous settlement), the paid part of what it
// attributed, and each grant's take, for bookWalletRefundTx.
func settleWalletBonusTx(tx *gorm.DB, acct *bonusAccount, now int64) error {
	unattributed := acct.WalletSpentQuota - acct.BonusSpentCursor
	if unattributed == 0 {
		return nil
	}
	// A negative difference (refunds booked ahead of their charges' flush)
	// leaves the grants as they are and records an empty window, so no later
	// refund is given back to grants against it.
	settledFrom, paid := now, int64(0)
	if unattributed > 0 {
		settledFrom = acct.BonusSettledAt
		grants, err := activeBonusGrants(tx, acct.Id, BonusPoolWallet)
		if err != nil {
			return err
		}
		takes := attributeBonusUsage(grants, unattributed)
		paid = unattributed
		for i := range grants {
			if takes[i] == 0 {
				continue
			}
			if err := consumeBonusGrantTx(tx, &grants[i], takes[i], now, now); err != nil {
				return err
			}
			paid -= takes[i]
		}
	}
	result := tx.Unscoped().Model(&User{}).
		Where("id = ? AND bonus_spent_cursor = ?", acct.Id, acct.BonusSpentCursor).
		Updates(map[string]any{
			"bonus_spent_cursor": acct.WalletSpentQuota,
			"bonus_settled_at":   now,
			"bonus_settled_from": settledFrom,
			"bonus_settled_paid": paid,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errBonusSettleConflict
	}
	acct.BonusSpentCursor = acct.WalletSpentQuota
	acct.BonusSettledAt = now
	acct.BonusSettledFrom = settledFrom
	acct.BonusSettledPaid = paid
	return nil
}

// walletRefundBooking is how bookWalletRefundTx books a usage refund: the
// caller credits the whole refund and lowers the two counters by these.
type walletRefundBooking struct {
	spentDecrease int64 // wallet_spent_quota (W)
	paidDecrease  int64 // bonus_settled_paid
}

// bookWalletRefundTx books a usage refund of amount whose charge was applied
// at or after chargedAt (0 when unknown) on the bonus ledger, while the caller
// holds lockBonusAccount. The refund itself is always credited in full.
//
//   - Charged after the last settlement: the charge is not attributed yet, so
//     the refund lowers W and nets exactly.
//   - Charged inside the last settlement's window: that settlement may have
//     attributed the charge, also while the request was still in flight. The
//     refund is matched against the paid part of that settlement first, then
//     given back to the grants it consumed that have not reached their
//     deadline (latest deadline first, reopening a used one); whatever is
//     left lowers W by at most the usage not attributed yet (a charge applied
//     right after the settlement, within the clock skew). Every step keeps
//     the ledger's unspent bonus at or below the real one, so paid money is
//     never deducted.
//   - Anything else (charged before that window, or unknown) is paid balance.
func bookWalletRefundTx(tx *gorm.DB, acct *bonusAccount, amount int64, chargedAt int64, now int64) (walletRefundBooking, error) {
	var booking walletRefundBooking
	if chargedAt <= 0 || amount <= 0 {
		return booking, nil
	}
	chargeFloor := chargedAt - bonusRefundSkewSeconds
	if acct.BonusSettledAt < chargeFloor {
		booking.spentDecrease = amount
		return booking, nil
	}
	if chargeFloor <= acct.BonusSettledFrom {
		return booking, nil
	}
	booking.paidDecrease = min(amount, max(acct.BonusSettledPaid, 0))
	rest := amount - booking.paidDecrease
	if rest > 0 {
		var grants []BonusGrant
		if err := tx.Where("user_id = ? AND pool = ? AND settled_at = ? AND settled_take > 0", acct.Id, BonusPoolWallet, acct.BonusSettledAt).
			Order("expires_at DESC").Order("id DESC").Find(&grants).Error; err != nil {
			return booking, err
		}
		for i := range grants {
			if rest == 0 {
				break
			}
			grant := &grants[i]
			// Only grants still running get it back: one past its deadline would be
			// deducted by the next sweep, while the refunded charge may be one made
			// right after the settlement that will still be attributed later.
			if grant.Status == BonusStatusExpired || grant.effectiveDeadline(acct.WalletUsedAt) <= now {
				continue
			}
			reopen := grant.Status == BonusStatusUsed
			give := min(rest, grant.SettledTake)
			updates := map[string]any{
				"remaining":    gorm.Expr("remaining + ?", give),
				"settled_take": gorm.Expr("settled_take - ?", give),
			}
			if reopen {
				updates["status"] = BonusStatusActive
				updates["closed_at"] = 0
			}
			result := tx.Model(&BonusGrant{}).
				Where("id = ? AND status = ? AND settled_take >= ?", grant.Id, grant.Status, give).
				Updates(updates)
			if result.Error != nil {
				return booking, result.Error
			}
			if result.RowsAffected != 1 {
				return booking, errBonusGrantConflict
			}
			rest -= give
		}
	}
	booking.spentDecrease = min(rest, max(acct.WalletSpentQuota-acct.BonusSpentCursor, 0))
	return booking, nil
}

// bonusGrantSpec describes a reward to credit; ExpireDays 0 never expires.
type bonusGrantSpec struct {
	Source     string
	Amount     int64
	ExpireDays int
	RefType    string
	RefId      int
}

func insertBonusGrantTx(tx *gorm.DB, userId int, pool string, spec bonusGrantSpec, now int64) (*BonusGrant, error) {
	if spec.ExpireDays <= 0 {
		return nil, nil
	}
	seconds := int64(spec.ExpireDays) * bonusSecondsPerDay
	grant := &BonusGrant{
		UserId:        userId,
		Source:        spec.Source,
		Pool:          pool,
		Status:        BonusStatusActive,
		Amount:        spec.Amount,
		Remaining:     spec.Amount,
		ExpireSeconds: seconds,
		ExpiresAt:     now + seconds,
		CheckAt:       now + seconds,
		RefType:       spec.RefType,
		RefId:         spec.RefId,
		CreatedAt:     now,
	}
	if err := tx.Create(grant).Error; err != nil {
		return nil, err
	}
	return grant, nil
}

// grantWalletBonusTx credits a wallet reward under the caller's account lock
// and returns its grant, or nil when it never expires. The wallet usage
// already written is settled first, so it is never attributed to the new
// grant. In batch mode the caller flushes this node's queued usage of the user
// beforehand (flushUserBatchedDeltas); usage still queued on another node, at
// most one batch interval old, reaches the counter later and may be
// attributed to the new grant. The credit is not usage: the wallet usage
// counter stays as it is.
func grantWalletBonusTx(tx *gorm.DB, acct *bonusAccount, spec bonusGrantSpec, now int64) (*BonusGrant, error) {
	if spec.Amount <= 0 {
		return nil, nil
	}
	if spec.ExpireDays > 0 {
		if err := settleWalletBonusTx(tx, acct, now); err != nil {
			return nil, err
		}
	}
	result := tx.Unscoped().Model(&User{}).
		Where("id = ? AND quota <= ?", acct.Id, common.MaxWalletQuota-spec.Amount).
		Update("quota", gorm.Expr("quota + ?", spec.Amount))
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrWalletQuotaLimitExceeded
	}
	acct.Quota += spec.Amount
	return insertBonusGrantTx(tx, acct.Id, BonusPoolWallet, spec, now)
}

// grantAffBonusTx credits an inviter reward to the invitation balance under
// the caller's account lock and returns its grant, or nil when it never expires.
func grantAffBonusTx(tx *gorm.DB, acct *bonusAccount, spec bonusGrantSpec, now int64) (*BonusGrant, error) {
	if spec.Amount <= 0 {
		return nil, nil
	}
	result := tx.Unscoped().Model(&User{}).Where("id = ?", acct.Id).Updates(map[string]any{
		"aff_quota":   gorm.Expr("aff_quota + ?", spec.Amount),
		"aff_history": gorm.Expr("aff_history + ?", spec.Amount),
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	acct.AffQuota += spec.Amount
	return insertBonusGrantTx(tx, acct.Id, BonusPoolAff, spec, now)
}

// consumeAffBonusTx takes a transfer of amount out of the user's expiring
// invite rewards, earliest fixed deadline first; the rest comes from the legacy
// invitation balance, which never expires. Every reward still left is marked
// as used now, which restarts its period under the sliding rule. The caller
// holds lockBonusAccount.
func consumeAffBonusTx(tx *gorm.DB, userId int, amount int64, now int64) error {
	grants, err := activeBonusGrants(tx, userId, BonusPoolAff)
	if err != nil {
		return err
	}
	takes := attributeBonusUsage(grants, amount)
	for i := range grants {
		if takes[i] > 0 {
			if err := consumeBonusGrantTx(tx, &grants[i], takes[i], now, 0); err != nil {
				return err
			}
		}
		// Updated by primary key, so MySQL takes no gap lock on the grant indexes;
		// an unchanged value would count as no affected row on MySQL.
		if grants[i].Remaining > 0 && grants[i].LastUsedAt != now {
			if err := updateActiveBonusGrantTx(tx, grants[i].Id, map[string]any{"last_used_at": now}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListUsersWithDueBonusGrants returns, ascending, up to limit user ids above
// afterUserId that have an active grant of one of sources due by cutoff. Fixed
// grants are due by expires_at, sliding ones by check_at.
func ListUsersWithDueBonusGrants(sources []string, sliding bool, cutoff int64, afterUserId int, limit int) ([]int, error) {
	column := "expires_at"
	if sliding {
		column = "check_at"
	}
	var ids []int
	err := DB.Model(&BonusGrant{}).Distinct("user_id").
		Where("status = ? AND source IN ? AND "+column+" <= ? AND user_id > ?", BonusStatusActive, sources, cutoff, afterUserId).
		Order("user_id").
		Limit(limit).
		Pluck("user_id", &ids).Error
	return ids, err
}

type expiredBonusGrant struct {
	grant    BonusGrant
	deducted int64
}

// ExpireUserBonusGrants settles the user's wallet usage and expires every
// active grant whose effective deadline is not after cutoff. Only the unspent
// remainder is deducted, never more than the balance (nor the cached balance),
// so neither the wallet nor the invitation balance goes below 0 and paid money
// is never touched. Sliding grants that were used get their next check time.
// Running it again, or on two masters, does nothing more.
func ExpireUserBonusGrants(userId int, now int64, cutoff int64) error {
	cachedQuota, cacheHit := int64(0), false
	if cached, err := cacheGetUserBase(userId); err == nil {
		cachedQuota, cacheHit = int64(cached.Quota), true
	}
	var expired []expiredBonusGrant
	var walletDeducted int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		acct, err := lockBonusAccount(tx, userId)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The user was hard-deleted: close the grants without a deduction.
			return tx.Model(&BonusGrant{}).
				Where("user_id = ? AND status = ?", userId, BonusStatusActive).
				Updates(map[string]any{"status": BonusStatusExpired, "deducted_quota": 0, "closed_at": now}).Error
		}
		if err != nil {
			return err
		}
		if err := settleWalletBonusTx(tx, acct, common.GetTimestamp()); err != nil {
			return err
		}
		grants, err := activeBonusGrants(tx, userId, "")
		if err != nil {
			return err
		}
		left := map[string]int64{BonusPoolWallet: max(acct.Quota, 0), BonusPoolAff: max(acct.AffQuota, 0)}
		if cacheHit {
			left[BonusPoolWallet] = min(left[BonusPoolWallet], max(cachedQuota, 0))
		}
		var affDeducted int64
		for i := range grants {
			grant := &grants[i]
			if grant.Remaining <= 0 {
				if err := updateActiveBonusGrantTx(tx, grant.Id, map[string]any{"status": BonusStatusUsed, "closed_at": now}); err != nil {
					return err
				}
				continue
			}
			deadline := grant.effectiveDeadline(acct.WalletUsedAt)
			if deadline > cutoff {
				if bonusSliding(grant.Source) && deadline != grant.CheckAt {
					if err := updateActiveBonusGrantTx(tx, grant.Id, map[string]any{"check_at": deadline}); err != nil {
						return err
					}
				}
				continue
			}
			deducted := min(grant.Remaining, left[grant.Pool])
			left[grant.Pool] -= deducted
			if err := updateActiveBonusGrantTx(tx, grant.Id, map[string]any{"status": BonusStatusExpired, "deducted_quota": deducted, "closed_at": now}); err != nil {
				return err
			}
			if grant.Pool == BonusPoolWallet {
				walletDeducted += deducted
			} else {
				affDeducted += deducted
			}
			expired = append(expired, expiredBonusGrant{grant: *grant, deducted: deducted})
		}
		// The deductions are not usage: the wallet usage counter stays.
		if walletDeducted > 0 {
			if err := tx.Unscoped().Model(&User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota - ?", walletDeducted)).Error; err != nil {
				return err
			}
		}
		if affDeducted > 0 {
			if err := tx.Unscoped().Model(&User{}).Where("id = ?", userId).
				Update("aff_quota", gorm.Expr("aff_quota - ?", affDeducted)).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if walletDeducted > 0 {
		if err := cacheDecrUserQuota(userId, walletDeducted); err != nil {
			common.SysError(common.LogText("failed to sync expired bonus quota of user %d to the cache: %s", userId, err.Error()))
		}
	}
	for _, item := range expired {
		RecordLog(userId, LogTypeSystem, bonusExpiryMessage(&item.grant, item.deducted))
	}
	return nil
}

// updateActiveBonusGrantTx applies updates to a grant that must still be active.
func updateActiveBonusGrantTx(tx *gorm.DB, grantId int, updates map[string]any) error {
	result := tx.Model(&BonusGrant{}).Where("id = ? AND status = ?", grantId, BonusStatusActive).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errBonusGrantConflict
	}
	return nil
}

func bonusExpiryMessage(grant *BonusGrant, deducted int64) *common.Message {
	params := map[string]any{
		"amount": logger.FormatQuota(int(grant.Amount)),
		"quota":  logger.FormatQuota(int(deducted)),
	}
	switch grant.Source {
	case BonusSourceInvitee:
		return common.NewMessage("Invitation bonus of {{amount}} expired, {{quota}} unused quota deducted", params)
	case BonusSourceInviter:
		return common.NewMessage("Invite reward of {{amount}} expired, {{quota}} untransferred quota removed from the invitation balance", params)
	default:
		return common.NewMessage("Check-in reward of {{amount}} expired, {{quota}} unused quota deducted", params)
	}
}

// bonusGrantMessage picks the grant log text for a reward that never expires
// (keys[0]), expires at a fixed time (keys[1]) or expires after a period
// without use (keys[2]). grant is nil when the reward never expires.
func bonusGrantMessage(keys [3]string, quota int64, grant *BonusGrant, extra map[string]any) *common.Message {
	params := map[string]any{"quota": logger.FormatQuota(int(quota))}
	maps.Copy(params, extra)
	if grant == nil {
		return common.NewMessage(keys[0], params)
	}
	params["expires_at"] = time.Unix(grant.ExpiresAt, 0).Format(bonusTimeLayout)
	if !bonusSliding(grant.Source) {
		return common.NewMessage(keys[1], params)
	}
	params["days"] = grant.ExpireSeconds / bonusSecondsPerDay
	return common.NewMessage(keys[2], params)
}

// BonusGrantView is an active grant as the user sees it: Remaining includes
// usage not settled yet and ExpiresAt is the deadline under the current rule.
type BonusGrantView struct {
	Id                   int    `json:"id"`
	Source               string `json:"source"`
	Amount               int64  `json:"amount"`
	Remaining            int64  `json:"remaining"`
	CreatedAt            int64  `json:"created_at"`
	ExpiresAt            int64  `json:"expires_at"`
	ExpireDays           int64  `json:"expire_days"`
	ExpireOnlyWhenUnused bool   `json:"expire_only_when_unused"`
}

type BonusCheckinInfo struct {
	Enabled              bool    `json:"enabled"`
	PaidThresholdQuota   int     `json:"paid_threshold_quota"`
	PaidMultiplier       float64 `json:"paid_multiplier"`
	IsPaidUser           bool    `json:"is_paid_user"`
	FreeExpireDays       int     `json:"free_expire_days"`
	PaidExpireDays       int     `json:"paid_expire_days"`
	ExpireOnlyWhenUnused bool    `json:"expire_only_when_unused"`
}

type BonusInviteInfo struct {
	ThresholdQuota       int  `json:"threshold_quota"`
	ExpireDays           int  `json:"expire_days"`
	ExpireOnlyWhenUnused bool `json:"expire_only_when_unused"`
}

// BonusPendingInvite is an invite of the user whose inviter reward is still
// waiting for the invitee's recharge threshold (or for the payment compliance
// confirmation); it never reveals the invitee.
type BonusPendingInvite struct {
	Id           int   `json:"id"`
	CreatedAt    int64 `json:"created_at"`
	InviterQuota int64 `json:"inviter_quota"`
}

type BonusPendingInvites struct {
	Count int64                `json:"count"`
	Items []BonusPendingInvite `json:"items"`
}

// BonusOwnInviteReward is the user's own invite-code bonus.
type BonusOwnInviteReward struct {
	Status         string `json:"status"`
	InviteeQuota   int64  `json:"invitee_quota"`
	ThresholdQuota int    `json:"threshold_quota"`
}

// BonusGrantTotals covers every active grant of a pool that has a remainder,
// also those beyond the listed rows.
type BonusGrantTotals struct {
	Count     int   `json:"count"`
	Remaining int64 `json:"remaining"`
}

type BonusSummary struct {
	RechargedQuota int64            `json:"recharged_quota"`
	Checkin        BonusCheckinInfo `json:"checkin"`
	Invite         BonusInviteInfo  `json:"invite"`
	// WalletGrants and AffGrants list up to bonusSummaryGrantLimit grants in
	// the order they are used (spent or transferred); the totals cover all.
	WalletGrants    []BonusGrantView      `json:"wallet_grants"`
	WalletTotals    BonusGrantTotals      `json:"wallet_totals"`
	AffGrants       []BonusGrantView      `json:"aff_grants"`
	AffTotals       BonusGrantTotals      `json:"aff_totals"`
	PendingInvites  BonusPendingInvites   `json:"pending_invites"`
	OwnInviteReward *BonusOwnInviteReward `json:"own_invite_reward"`
}

// GetUserBonusSummary returns the user's reward settings, active grants and
// invites. It writes nothing: unsettled wallet usage is attributed in memory,
// exactly as the next settlement would.
func GetUserBonusSummary(userId int) (*BonusSummary, error) {
	var acct bonusAccount
	if err := DB.Unscoped().Model(&User{}).Select(bonusAccountColumns).Where("id = ?", userId).Take(&acct).Error; err != nil {
		return nil, err
	}
	recharged, err := GetUserRechargeQuota(userId)
	if err != nil {
		return nil, err
	}
	grants, err := activeBonusGrants(DB, userId, "")
	if err != nil {
		return nil, err
	}
	var walletGrants, affGrants []BonusGrant
	for _, grant := range grants {
		if grant.Pool == BonusPoolWallet {
			walletGrants = append(walletGrants, grant)
		} else {
			affGrants = append(affGrants, grant)
		}
	}
	if unattributed := acct.WalletSpentQuota - acct.BonusSpentCursor; unattributed > 0 {
		takes := attributeBonusUsage(walletGrants, unattributed)
		for i := range walletGrants {
			walletGrants[i].Remaining -= takes[i]
		}
	}

	setting := operation_setting.GetCheckinSetting()
	walletViews, walletTotals := bonusGrantViews(walletGrants, acct.WalletUsedAt)
	affViews, affTotals := bonusGrantViews(affGrants, acct.WalletUsedAt)
	summary := &BonusSummary{
		RechargedQuota: recharged,
		Checkin: BonusCheckinInfo{
			Enabled:              setting.Enabled,
			PaidThresholdQuota:   setting.PaidThresholdQuota,
			PaidMultiplier:       setting.PaidMultiplier,
			IsPaidUser:           operation_setting.IsPaidCheckinUser(recharged),
			FreeExpireDays:       setting.FreeExpireDays,
			PaidExpireDays:       setting.PaidExpireDays,
			ExpireOnlyWhenUnused: setting.ExpireOnlyWhenUnused,
		},
		Invite: BonusInviteInfo{
			ThresholdQuota:       common.InviteRechargeThresholdQuota,
			ExpireDays:           common.InviteRewardExpireDays,
			ExpireOnlyWhenUnused: common.InviteRewardExpireOnlyWhenUnused,
		},
		WalletGrants:   walletViews,
		WalletTotals:   walletTotals,
		AffGrants:      affViews,
		AffTotals:      affTotals,
		PendingInvites: BonusPendingInvites{Items: []BonusPendingInvite{}},
	}

	// Only invites that will pay the inviter: a pending row may carry just
	// the invitee bonus (the inviter reward was 0 at registration), and a
	// deleted invitee's reward can never be granted.
	pendingQuery := DB.Model(&InviteReward{}).
		Where("inviter_id = ? AND status = ? AND inviter_quota > 0", userId, InviteRewardStatusPending).
		Where(inviteeExistsCondition).
		Session(&gorm.Session{})
	if err := pendingQuery.Count(&summary.PendingInvites.Count).Error; err != nil {
		return nil, err
	}
	var pendingRows []InviteReward
	if err := pendingQuery.Order("id desc").Limit(bonusSummaryPendingInviteLimit).Find(&pendingRows).Error; err != nil {
		return nil, err
	}
	for _, row := range pendingRows {
		summary.PendingInvites.Items = append(summary.PendingInvites.Items, BonusPendingInvite{
			Id:           row.Id,
			CreatedAt:    row.CreatedAt,
			InviterQuota: row.InviterQuota,
		})
	}

	var own InviteReward
	err = DB.Where("invitee_id = ?", userId).Take(&own).Error
	if err == nil {
		summary.OwnInviteReward = &BonusOwnInviteReward{
			Status:         own.Status,
			InviteeQuota:   own.InviteeQuota,
			ThresholdQuota: common.InviteRechargeThresholdQuota,
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return summary, nil
}

// bonusGrantViews lists up to bonusSummaryGrantLimit grants with a remainder
// in the order given (activeBonusGrants: the order they are used, by fixed
// deadline, whatever the expire-only-when-unused switches say) and totals all
// of them.
func bonusGrantViews(grants []BonusGrant, walletUsedAt int64) ([]BonusGrantView, BonusGrantTotals) {
	views := make([]BonusGrantView, 0, min(len(grants), bonusSummaryGrantLimit))
	var totals BonusGrantTotals
	for i := range grants {
		grant := &grants[i]
		if grant.Remaining <= 0 {
			continue
		}
		totals.Count++
		totals.Remaining += grant.Remaining
		if len(views) == bonusSummaryGrantLimit {
			continue
		}
		views = append(views, BonusGrantView{
			Id:                   grant.Id,
			Source:               grant.Source,
			Amount:               grant.Amount,
			Remaining:            grant.Remaining,
			CreatedAt:            grant.CreatedAt,
			ExpiresAt:            grant.effectiveDeadline(walletUsedAt),
			ExpireDays:           grant.ExpireSeconds / bonusSecondsPerDay,
			ExpireOnlyWhenUnused: bonusSliding(grant.Source),
		})
	}
	return views, totals
}
