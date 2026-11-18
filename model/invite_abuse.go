package model

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The invite abuse rule: an inviter (not deleted, not an administrator, not
// exempt and, while paid inviters are exempt, without a qualifying recharge
// of its own) with at least the threshold of invited users, none of whom has
// a qualifying recharge, is disabled together with its invited users that
// are not deleted, enabled, not administrators and not exempt. Invited users
// are the users whose inviter_id is the inviter, deleted ones included (see
// countedInvitees); aff_count is not a reliable count.
const (
	InviteAbuseKindInviter = "inviter"
	InviteAbuseKindInvitee = "invitee"

	// InviteAbuseSweepInterval is how often the master node sweeps every
	// inviter while the rule is enabled and its settings stay the same.
	InviteAbuseSweepInterval = 10 * time.Minute

	inviteAbuseSweepBudget = 50 * time.Second
	// A sign-up waits for its inviter's check at most this long; accounts it
	// leaves enabled are disabled by the sweep.
	inviteAbuseSignUpBudget = 5 * time.Second
	inviteAbusePreviewItems = 100
	inviteAbuseRevokeReason = "invite_abuse"
	inviteAbuseAuditAction  = "user.invite_abuse_ban"
)

var (
	// inviteAbuseBatchSize is how many candidate inviters one grouped query
	// returns; the sweep and the preview page through them by inviter id.
	inviteAbuseBatchSize = 200
	// inviteAbuseRecheckInterval is how often the rule takes its verdict on an
	// inviter again while it disables the inviter's accounts.
	inviteAbuseRecheckInterval = time.Second
)

// InviteAbuseBan records an account the invite abuse rule disabled. A record
// an administrator released by enabling the account again exempts the
// account from the rule from then on.
type InviteAbuseBan struct {
	Id          int    `json:"id"`
	UserId      int    `json:"user_id" gorm:"not null;uniqueIndex"`
	InviterId   int    `json:"inviter_id" gorm:"not null;index"` // the abusive inviter; its own id for kind inviter
	Kind        string `json:"kind" gorm:"type:varchar(16);not null"`
	InviteCount int    `json:"invite_count" gorm:"not null"` // the inviter's invited users when the account was disabled
	CreatedAt   int64  `json:"created_at" gorm:"type:bigint;not null"`
	ReleasedAt  int64  `json:"released_at" gorm:"type:bigint;not null;default:0"`
	ReleasedBy  int    `json:"released_by" gorm:"not null;default:0"`
}

// InviteAbuseSettings are the thresholds the invite abuse rule runs with.
type InviteAbuseSettings struct {
	// Threshold is the number of invited users (deleted ones included) from
	// which an inviter is checked.
	Threshold int
	// MinRedeemQuota is the redeemed quota from which a user's used
	// redemption codes count as a recharge.
	MinRedeemQuota int64
	// ExemptPaidInviter spares the inviters who have a qualifying recharge
	// themselves.
	ExemptPaidInviter bool
}

// CurrentInviteAbuseSettings returns the rule's settings as this node has
// them.
func CurrentInviteAbuseSettings() InviteAbuseSettings {
	return loadedInviteAbuseOptions().settings()
}

// inviteAbuseOptions are the rule's four options; this node's copy is in
// common.InviteAbuseBan*, the saved one in the options table.
type inviteAbuseOptions struct {
	enabled           bool
	threshold         int
	minRedeemQuota    int
	exemptPaidInviter bool
}

// inviteAbuseOptionKeys are the keys of the rule's options.
var inviteAbuseOptionKeys = []string{
	operation_setting.InviteAbuseBanEnabledKey, operation_setting.InviteAbuseBanThresholdKey,
	operation_setting.InviteAbuseBanMinRedeemQuotaKey, operation_setting.InviteAbuseBanExemptPaidInviterKey,
}

// loadedInviteAbuseOptions returns this node's copy of the options.
func loadedInviteAbuseOptions() inviteAbuseOptions {
	return inviteAbuseOptions{
		enabled:           common.InviteAbuseBanEnabled,
		threshold:         common.InviteAbuseBanThreshold,
		minRedeemQuota:    common.InviteAbuseBanMinRedeemQuota,
		exemptPaidInviter: common.InviteAbuseBanExemptPaidInviter,
	}
}

// set applies the saved value of one of the options.
func (options *inviteAbuseOptions) set(key string, value string) {
	switch key {
	case operation_setting.InviteAbuseBanEnabledKey:
		options.enabled = value == "true"
	case operation_setting.InviteAbuseBanThresholdKey:
		options.threshold, _ = strconv.Atoi(value)
	case operation_setting.InviteAbuseBanMinRedeemQuotaKey:
		options.minRedeemQuota, _ = strconv.Atoi(value)
	case operation_setting.InviteAbuseBanExemptPaidInviterKey:
		options.exemptPaidInviter = value == "true"
	}
}

// settings returns the settings the options give the rule; a threshold
// below 1 counts as 1 and a minimum below 0 as 0.
func (options inviteAbuseOptions) settings() InviteAbuseSettings {
	return InviteAbuseSettings{
		Threshold:         max(options.threshold, 1),
		MinRedeemQuota:    max(int64(options.minRedeemQuota), 0),
		ExemptPaidInviter: options.exemptPaidInviter,
	}
}

// InviteAbusePreview is what the rule would disable with some settings.
type InviteAbusePreview struct {
	Threshold         int                      `json:"threshold"`
	MinRedeemQuota    int64                    `json:"min_redeem_quota"`
	ExemptPaidInviter bool                     `json:"exempt_paid_inviter"`
	AbusiveInviters   int                      `json:"abusive_inviters"`
	InvitersToDisable int                      `json:"inviters_to_disable"`
	InviteesToDisable int64                    `json:"invitees_to_disable"`
	Items             []InviteAbusePreviewItem `json:"items"`
	Truncated         bool                     `json:"truncated"`
}

// InviteAbusePreviewItem is one abusive inviter of a preview.
type InviteAbusePreviewItem struct {
	Id                int    `json:"id"`
	Username          string `json:"username"`
	DisplayName       string `json:"display_name"`
	Status            int    `json:"status"`
	InviteCount       int64  `json:"invite_count"`
	InviteesToDisable int64  `json:"invitees_to_disable"`
}

// inviteAbuseCandidate is an inviter whose invited users reach the threshold
// without a qualifying recharge. Whether the rule applies to the inviter
// itself (deleted, administrator, exempt) is checked separately.
type inviteAbuseCandidate struct {
	InviterId   int
	InviteCount int64
	// InviteesToDisable counts the invited users that are not deleted,
	// enabled, not administrators and not exempt.
	InviteesToDisable int64
}

// countedInvitees selects the invited users that count for their inviter
// (inviter_id). Everything that counts or lists a user's invited users builds
// on it: the users list's invite_count, its sort and its inviter filter, and
// the invite abuse rule's threshold, recharge check and preview. Deleted
// users count too: an invited account deleted afterwards is still somebody
// its inviter invited, so a farm cannot slip under the threshold by deleting
// its invited accounts.
func countedInvitees(db *gorm.DB) *gorm.DB {
	return db.Unscoped().Model(&User{}).Where("inviter_id <> ?", 0)
}

// qualifyingTopUps selects the successful paid orders that count as a
// recharge: online top-ups (amount > 0), subscriptions paid through a payment
// gateway (amount 0, money > 0) and orders an administrator completed. A
// subscription paid from the balance writes no top_ups row.
func qualifyingTopUps() *gorm.DB {
	return DB.Model(&TopUp{}).Where("status = ? AND (amount > 0 OR money > 0)", common.TopUpStatusSuccess)
}

// qualifyingRedemptions groups the used redemption codes by user and keeps
// the users who redeemed at least minQuota in total. Codes deleted after use
// still count.
func qualifyingRedemptions(minQuota int64) *gorm.DB {
	return DB.Unscoped().Model(&Redemption{}).Where("status = ?", common.RedemptionCodeStatusUsed).
		Group("used_user_id").Having("SUM(quota) >= ?", minQuota)
}

// rechargedUserIds returns the users among userIds (a list of ids or a
// subquery selecting them) who have a qualifying recharge: a qualifying
// top-up, or used redemption codes worth at least minRedeemQuota. A limit
// above 0 stops after that many users of each kind, which is enough to tell
// whether any of them recharged. An id may be returned twice.
func rechargedUserIds(userIds any, minRedeemQuota int64, limit int) ([]int, error) {
	var paid, redeemed []int
	topUps := qualifyingTopUps().Distinct("user_id").Where("user_id IN (?)", userIds)
	redemptions := qualifyingRedemptions(minRedeemQuota).Where("used_user_id IN (?)", userIds)
	if limit > 0 {
		topUps, redemptions = topUps.Limit(limit), redemptions.Limit(limit)
	}
	if err := topUps.Pluck("user_id", &paid).Error; err != nil {
		return nil, err
	}
	if limit > 0 && len(paid) >= limit {
		return paid, nil
	}
	if err := redemptions.Pluck("used_user_id", &redeemed).Error; err != nil {
		return nil, err
	}
	return append(paid, redeemed...), nil
}

// inviteAbuseExemptUsers selects the users an administrator enabled again
// after the rule disabled them.
func inviteAbuseExemptUsers() *gorm.DB {
	return DB.Model(&InviteAbuseBan{}).Select("user_id").Where("released_at > ?", 0)
}

// inviteAbuseNotReleased is a condition that holds while none of userIds is
// exempt. It costs one lookup on the unique user_id index per id on every
// database, where PostgreSQL scans all the records for NOT IN (subquery).
func inviteAbuseNotReleased(userIds ...int) clause.Expr {
	released := DB.Model(&InviteAbuseBan{}).Select("1").Where("user_id IN ? AND released_at > ?", userIds, 0)
	return clause.Expr{SQL: "NOT EXISTS (?)", Vars: []any{released}}
}

// listInviteAbuseCandidates returns up to limit candidates with an inviter id
// above afterInviterId, in inviter id order. One grouped scan of the counted
// invited users with the qualifying recharges joined (at most one row per
// user each, so COUNT(*) counts invited users); portable to SQLite, MySQL and
// PostgreSQL.
func listInviteAbuseCandidates(settings InviteAbuseSettings, afterInviterId int, limit int) ([]inviteAbuseCandidate, error) {
	userId := clause.Column{Table: clause.CurrentTable, Name: "id"}
	var candidates []inviteAbuseCandidate
	err := countedInvitees(DB).
		Select("inviter_id, COUNT(*) AS invite_count, "+
			"SUM(CASE WHEN deleted_at IS NULL AND status = ? AND role < ? AND exempt_users.user_id IS NULL THEN 1 ELSE 0 END) AS invitees_to_disable",
			common.UserStatusEnabled, common.RoleAdminUser).
		Joins("LEFT JOIN (?) AS paid_top_ups ON paid_top_ups.user_id = ?", qualifyingTopUps().Distinct("user_id"), userId).
		Joins("LEFT JOIN (?) AS paid_redemptions ON paid_redemptions.used_user_id = ?", qualifyingRedemptions(settings.MinRedeemQuota).Select("used_user_id"), userId).
		Joins("LEFT JOIN (?) AS exempt_users ON exempt_users.user_id = ?", inviteAbuseExemptUsers(), userId).
		Where("inviter_id > ?", afterInviterId).
		Group("inviter_id").
		Having("COUNT(*) >= ? AND COUNT(paid_top_ups.user_id) = 0 AND COUNT(paid_redemptions.used_user_id) = 0", settings.Threshold).
		Order("inviter_id").
		Limit(limit).
		Scan(&candidates).Error
	return candidates, err
}

// loadInviteAbuseInviters returns, by id, the candidates' inviters the rule
// applies to: not deleted, not administrators, not exempt and, while paid
// inviters are exempt, without a qualifying recharge.
func loadInviteAbuseInviters(candidates []inviteAbuseCandidate, settings InviteAbuseSettings) (map[int]User, error) {
	inviters := make(map[int]User, len(candidates))
	if len(candidates) == 0 {
		return inviters, nil
	}
	ids := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.InviterId)
	}
	var users []User
	if err := DB.Select("id", "username", "display_name", "status", "role", "inviter_id").
		Where("id IN ? AND role < ?", ids, common.RoleAdminUser).
		Where("id NOT IN (?)", inviteAbuseExemptUsers()).
		Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		inviters[user.Id] = user
	}
	if !settings.ExemptPaidInviter || len(inviters) == 0 {
		return inviters, nil
	}
	paid, err := rechargedUserIds(ids, settings.MinRedeemQuota, 0)
	if err != nil {
		return nil, err
	}
	for _, id := range paid {
		delete(inviters, id)
	}
	return inviters, nil
}

// abusiveInviter takes the rule's verdict on one inviter with a few indexed
// single-inviter queries: the inviter, its counted invited users, the
// paid-inviter exemption and the invited users' recharges. It returns the
// inviter and its invited users count when the rule calls it abusive with
// settings, and nil otherwise.
func abusiveInviter(inviterId int, settings InviteAbuseSettings) (*User, int64, error) {
	var inviter User
	err := DB.Select("id", "username", "status", "role").
		Where("id = ? AND role < ?", inviterId, common.RoleAdminUser).
		Where(inviteAbuseNotReleased(inviterId)).
		Take(&inviter).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, nil // deleted, an administrator or exempt
	}
	if err != nil {
		return nil, 0, err
	}
	var inviteCount int64
	if err := countedInvitees(DB).Where("inviter_id = ?", inviterId).Count(&inviteCount).Error; err != nil {
		return nil, 0, err
	}
	if inviteCount < int64(settings.Threshold) {
		return nil, 0, nil
	}
	if settings.ExemptPaidInviter {
		paid, err := rechargedUserIds([]int{inviterId}, settings.MinRedeemQuota, 1)
		if err != nil || len(paid) > 0 {
			return nil, 0, err
		}
	}
	// Deleted invited users are included: one who paid before being deleted
	// still clears the inviter.
	invitees := countedInvitees(DB).Select("id").Where("inviter_id = ?", inviterId)
	paid, err := rechargedUserIds(invitees, settings.MinRedeemQuota, 1)
	if err != nil || len(paid) > 0 {
		return nil, 0, err
	}
	return &inviter, inviteCount, nil
}

// enforceInviteAbuseRuleOnInviter applies the rule to one inviter. The
// verdict is taken right before anything is disabled, so the sign-up hook and
// the sweep act on a current one. It returns how many accounts it disabled
// and whether it got through all of them (the deadline or a change of the
// rule stops it).
func enforceInviteAbuseRuleOnInviter(inviterId int, settings InviteAbuseSettings, deadline time.Time) (int, bool, error) {
	inviter, inviteCount, err := abusiveInviter(inviterId, settings)
	if err != nil || inviter == nil {
		return 0, err == nil, err
	}
	return disableAbusiveInviter(*inviter, inviteCount, settings, deadline)
}

// inviteAbuseRuleApplies reports whether the rule is still on with settings.
// A sweep pass and a sign-up check stop as soon as an administrator turns the
// rule off or changes its settings, so nothing more is disabled under
// settings that no longer apply.
func inviteAbuseRuleApplies(settings InviteAbuseSettings) bool {
	options := loadedInviteAbuseOptions()
	return options.enabled && options.settings() == settings
}

// inviteAbuseRuleSaved reports whether the options table still has the rule
// on with settings. A change an administrator saved on another node reaches
// this node only with its next option sync (SYNC_FREQUENCY, 60 s by default),
// later than a pass's time budget, so the rule also confirms the saved
// options while it disables accounts. An option never saved keeps this
// node's value.
func inviteAbuseRuleSaved(settings InviteAbuseSettings) (bool, error) {
	var saved []Option
	// A map condition lets GORM quote the key column for each database.
	if err := DB.Where(map[string]any{"key": inviteAbuseOptionKeys}).Find(&saved).Error; err != nil {
		return false, err
	}
	options := loadedInviteAbuseOptions()
	for _, option := range saved {
		options.set(option.Key, option.Value)
	}
	return options.enabled && options.settings() == settings, nil
}

// disableAbusiveInviter disables an abusive inviter and then its invited
// users that are not deleted, enabled, not administrators and not exempt,
// newest first, until the deadline or until the rule no longer applies with
// settings, here or as saved (inviteAbuseRuleSaved). It confirms the saved
// options before the first account, and then confirms them and takes the
// verdict again at most once per inviteAbuseRecheckInterval: a big farm
// takes a while, and a recharge or a deletion that clears the inviter stops
// it as well. An administrator who releases the inviter stops it at once. It
// returns how many accounts it disabled and whether it got through all of
// them.
func disableAbusiveInviter(inviter User, inviteCount int64, settings InviteAbuseSettings, deadline time.Time) (int, bool, error) {
	var invitees []User
	if err := DB.Select("id", "username").
		Where("inviter_id = ? AND status = ? AND role < ?", inviter.Id, common.UserStatusEnabled, common.RoleAdminUser).
		Where("id NOT IN (?)", inviteAbuseExemptUsers()).
		Order("id DESC").
		Find(&invitees).Error; err != nil {
		return 0, false, err
	}
	type account struct {
		user User
		kind string
	}
	accounts := make([]account, 0, len(invitees)+1)
	if inviter.Status == common.UserStatusEnabled {
		accounts = append(accounts, account{user: inviter, kind: InviteAbuseKindInviter})
	}
	for _, invitee := range invitees {
		accounts = append(accounts, account{user: invitee, kind: InviteAbuseKindInvitee})
	}
	disabled, finished := 0, true
	var err error
	var confirmedAt time.Time
	for _, target := range accounts {
		if time.Now().After(deadline) || !inviteAbuseRuleApplies(settings) {
			finished = false
			break
		}
		if time.Since(confirmedAt) >= inviteAbuseRecheckInterval {
			if !confirmedAt.IsZero() { // the verdict is fresh before the first account
				var current *User
				if current, _, err = abusiveInviter(inviter.Id, settings); err != nil || current == nil {
					finished = err == nil // no longer abusive: done with it
					break
				}
			}
			var saved bool
			if saved, err = inviteAbuseRuleSaved(settings); err != nil || !saved {
				finished = false
				break
			}
			confirmedAt = time.Now()
		}
		var changed bool
		// The exemptions are part of the update that disables: an account an
		// administrator released after the list above was loaded stays
		// enabled, and so does every account left once the inviter is released.
		changed, err = DisableUserBySystem(target.user.Id, inviteAbuseRevokeReason, inviteAbuseNotReleased(target.user.Id, inviter.Id))
		if err != nil {
			finished = false
			break
		}
		if changed {
			recordInviteAbuseBan(target.user, inviter.Id, target.kind, inviteCount)
			disabled++
		}
	}
	if disabled > 0 {
		common.SysLog(common.LogText("invite abuse: inviter %d has %d invited users without a recharge, disabled %d accounts", inviter.Id, inviteCount, disabled))
	}
	return disabled, finished, err
}

// recordInviteAbuseBan stores the ban record of an account the rule just
// disabled and tells the user (user log) and the administrators (audit log)
// why it was disabled.
func recordInviteAbuseBan(user User, inviterId int, kind string, inviteCount int64) {
	ban := InviteAbuseBan{
		UserId:      user.Id,
		InviterId:   inviterId,
		Kind:        kind,
		InviteCount: int(inviteCount),
		CreatedAt:   common.GetTimestamp(),
	}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&ban).Error; err != nil {
		common.SysError(common.LogText("failed to record the invite abuse ban of user %d: %v", user.Id, err))
	}
	message := common.NewMessage("Account disabled automatically: {{count}} invited users and none of them has recharged.", map[string]any{"count": inviteCount})
	if kind == InviteAbuseKindInvitee {
		message = common.NewMessage("Account disabled automatically: your inviter's {{count}} invited users have no recharge.", map[string]any{"count": inviteCount})
	}
	RecordLog(user.Id, LogTypeSystem, message)
	params := map[string]any{
		"id":           user.Id,
		"username":     user.Username,
		"kind":         kind,
		"inviter_id":   inviterId,
		"invite_count": inviteCount,
	}
	content := fmt.Sprintf("Disabled automatically by the invite abuse rule (%s, inviter ID %d, %d invited users without a recharge)", kind, inviterId, inviteCount)
	// No administrator acts here; the system actor is shown with the
	// administrator role so administrators see the entry in the audit log.
	RecordOperationAuditLog(user.Id, common.RoleAdminUser, content, "", inviteAbuseAuditAction, params,
		&AuditAdminInfo{AdminUsername: "system", AuthMethod: "system"}, nil)
}

// applyInviteAbuseRuleAfterSignUp checks the inviter of a user who just signed
// up with an invite code, then reloads the user's status and authentication
// version: a sign-up that makes the inviter abusive leaves the new account
// disabled at once, and the OAuth login that follows refuses it. Errors are
// logged and never fail the sign-up.
func (user *User) applyInviteAbuseRuleAfterSignUp(inviterId int) {
	if inviterId == 0 || !common.InviteAbuseBanEnabled {
		return
	}
	if _, _, err := enforceInviteAbuseRuleOnInviter(inviterId, CurrentInviteAbuseSettings(), time.Now().Add(inviteAbuseSignUpBudget)); err != nil {
		common.SysError(common.LogText("invite abuse check of inviter %d failed: %v", inviterId, err))
	}
	var current User
	if err := DB.Select("status", "auth_version").Where("id = ?", user.Id).Take(&current).Error; err != nil {
		common.SysError(common.LogText("failed to reload the status of new user %d: %v", user.Id, err))
		return
	}
	user.Status, user.AuthVersion = current.Status, current.AuthVersion
}

// ReleaseInviteAbuseBan marks the user's active ban record as released by the
// administrator who is enabling the account again; the user is exempt from
// the rule from then on. It reports whether a record was released.
func ReleaseInviteAbuseBan(userId int, adminId int) (bool, error) {
	result := DB.Model(&InviteAbuseBan{}).
		Where("user_id = ? AND released_at = ?", userId, 0).
		Updates(map[string]any{"released_at": common.GetTimestamp(), "released_by": adminId})
	return result.RowsAffected > 0, result.Error
}

// PreviewInviteAbuse reports what the rule would disable now with settings,
// whether or not the rule is enabled: totals that count every account once,
// and the abusive inviters with the most invited users first (an item's
// numbers are that inviter's own).
func PreviewInviteAbuse(settings InviteAbuseSettings) (*InviteAbusePreview, error) {
	preview := &InviteAbusePreview{Threshold: settings.Threshold, MinRedeemQuota: settings.MinRedeemQuota, ExemptPaidInviter: settings.ExemptPaidInviter}
	items := make([]InviteAbusePreviewItem, 0)
	abusive := make(map[int]bool)
	var enabledInviters []User
	afterInviterId := 0
	for {
		candidates, err := listInviteAbuseCandidates(settings, afterInviterId, inviteAbuseBatchSize)
		if err != nil {
			return nil, err
		}
		inviters, err := loadInviteAbuseInviters(candidates, settings)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			afterInviterId = candidate.InviterId
			inviter, applies := inviters[candidate.InviterId]
			if !applies {
				continue
			}
			preview.AbusiveInviters++
			abusive[inviter.Id] = true
			if inviter.Status == common.UserStatusEnabled {
				preview.InvitersToDisable++
				enabledInviters = append(enabledInviters, inviter)
			}
			preview.InviteesToDisable += candidate.InviteesToDisable
			items = append(items, InviteAbusePreviewItem{
				Id:                inviter.Id,
				Username:          inviter.Username,
				DisplayName:       inviter.DisplayName,
				Status:            inviter.Status,
				InviteCount:       candidate.InviteCount,
				InviteesToDisable: candidate.InviteesToDisable,
			})
		}
		if len(candidates) < inviteAbuseBatchSize {
			break
		}
	}
	// An enabled abusive inviter invited by another abusive inviter is also
	// one of that inviter's invited users to disable; it is counted once, as
	// an inviter.
	for _, inviter := range enabledInviters {
		if abusive[inviter.InviterId] {
			preview.InviteesToDisable--
		}
	}
	slices.SortFunc(items, func(a, b InviteAbusePreviewItem) int {
		if order := cmp.Compare(b.InviteCount, a.InviteCount); order != 0 {
			return order
		}
		return cmp.Compare(b.Id, a.Id)
	})
	if len(items) > inviteAbusePreviewItems {
		items, preview.Truncated = items[:inviteAbusePreviewItems], true
	}
	preview.Items = items
	return preview, nil
}

// inviteAbuseSweepState is where the master node's sweep stands. Only the
// caller holding inviteAbuseSweepRunning reads or writes it.
type inviteAbuseSweepState struct {
	settings   InviteAbuseSettings // the settings of the current pass
	generation int64               // the rule generation the current pass started at
	cursor     int                 // > 0: the current pass continues after this inviter id
	finishedAt time.Time           // when the last complete pass with settings ended
}

var (
	inviteAbuseSweepRunning atomic.Bool
	inviteAbuseSweep        inviteAbuseSweepState

	// inviteAbuseRuleGeneration moves on with every change of the rule that
	// calls for a new pass at once, so the tick cannot miss one, however
	// quickly the rule is turned off and on again.
	inviteAbuseRuleGeneration atomic.Int64
	inviteAbuseSweepWake      = make(chan struct{}, 1)
)

// InviteAbuseSweepWake delivers a value after the rule was turned on or one
// of its settings changed while it is on, so the master node's task loop can
// sweep at once instead of on its next tick.
func InviteAbuseSweepWake() <-chan struct{} {
	return inviteAbuseSweepWake
}

// applyInviteAbuseOption applies one of the rule's options. A real change
// that calls for a new pass — the rule turned on, or the threshold, the
// minimum redeemed quota or the paid-inviter exemption changed while it is
// on — moves the rule generation on and wakes the master node's task loop.
// Applying a value that did not change, as the periodic option sync does,
// changes nothing.
func applyInviteAbuseOption(key string, value string) {
	options := loadedInviteAbuseOptions()
	wasEnabled, previous := options.enabled, options.settings()
	options.set(key, value)
	common.InviteAbuseBanEnabled, common.InviteAbuseBanThreshold = options.enabled, options.threshold
	common.InviteAbuseBanMinRedeemQuota, common.InviteAbuseBanExemptPaidInviter = options.minRedeemQuota, options.exemptPaidInviter
	if !options.enabled || (wasEnabled && options.settings() == previous) {
		return
	}
	inviteAbuseRuleGeneration.Add(1)
	select {
	case inviteAbuseSweepWake <- struct{}{}:
	default: // a wake-up is pending already
	}
}

// RunInviteAbuseSweepIfDue is the master node's tick of the rule, run every
// minute and on InviteAbuseSweepWake. While the rule is enabled it sweeps
// every inviter right after startup, after the rule is turned on, after the
// threshold, the minimum redeemed quota or the paid-inviter exemption
// changed, and otherwise every InviteAbuseSweepInterval. A pass stops at its
// time budget and continues on the next tick; it also stops when the rule is
// turned off or its settings change, and the next tick then starts over with
// the new settings. Several masters are safe: every ban is a conditional
// update.
func RunInviteAbuseSweepIfDue(now time.Time) {
	if !inviteAbuseSweepRunning.CompareAndSwap(false, true) {
		return
	}
	defer inviteAbuseSweepRunning.Store(false)
	if !common.InviteAbuseBanEnabled {
		inviteAbuseSweep = inviteAbuseSweepState{}
		return
	}
	// The generation first: a change after this read starts another pass on
	// the next call.
	generation := inviteAbuseRuleGeneration.Load()
	settings := CurrentInviteAbuseSettings()
	if settings != inviteAbuseSweep.settings || generation != inviteAbuseSweep.generation {
		inviteAbuseSweep = inviteAbuseSweepState{settings: settings, generation: generation}
	}
	if inviteAbuseSweep.cursor == 0 && !inviteAbuseSweep.finishedAt.IsZero() && now.Sub(inviteAbuseSweep.finishedAt) < InviteAbuseSweepInterval {
		return
	}
	deadline := time.Now().Add(inviteAbuseSweepBudget)
	cursor, complete, err := sweepInviteAbuse(settings, inviteAbuseSweep.cursor, deadline)
	inviteAbuseSweep.cursor = cursor
	switch {
	case err != nil:
		common.SysError(common.LogText("invite abuse sweep failed: %v", err))
	case complete:
		inviteAbuseSweep.cursor, inviteAbuseSweep.finishedAt = 0, now
	case time.Now().Before(deadline): // here or, as saved, on another node
		common.SysLog(common.LogText("invite abuse sweep stopped after inviter %d: the rule was turned off or its settings changed", cursor))
	default:
		common.SysLog(common.LogText("invite abuse sweep reached its time budget, continuing after inviter %d on the next tick", cursor))
	}
}

// sweepInviteAbuse applies the rule to the candidates after afterInviterId
// until the deadline or until the rule no longer applies with settings. It
// returns the inviter id the pass continues after and whether the pass
// reached the end.
func sweepInviteAbuse(settings InviteAbuseSettings, afterInviterId int, deadline time.Time) (int, bool, error) {
	for {
		candidates, err := listInviteAbuseCandidates(settings, afterInviterId, inviteAbuseBatchSize)
		if err != nil {
			return afterInviterId, false, err
		}
		inviters, err := loadInviteAbuseInviters(candidates, settings)
		if err != nil {
			return afterInviterId, false, err
		}
		for _, candidate := range candidates {
			inviter, applies := inviters[candidate.InviterId]
			// A disabled inviter whose invited users are all disabled is done.
			if applies && (inviter.Status == common.UserStatusEnabled || candidate.InviteesToDisable > 0) {
				if time.Now().After(deadline) || !inviteAbuseRuleApplies(settings) {
					return afterInviterId, false, nil
				}
				// The batch only pre-selects: a recharge or a release may have
				// come in while the pass worked through earlier inviters, so the
				// verdict is taken again right before anything is disabled.
				_, finished, err := enforceInviteAbuseRuleOnInviter(candidate.InviterId, settings, deadline)
				if err != nil || !finished {
					return afterInviterId, false, err
				}
			}
			afterInviterId = candidate.InviterId
		}
		if len(candidates) < inviteAbuseBatchSize {
			return afterInviterId, true, nil
		}
	}
}
