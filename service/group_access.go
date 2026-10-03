package service

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// GroupAccessSubject is the requester checked against recharge-gated groups.
type GroupAccessSubject struct {
	UserId int
	Role   int
	Group  string
}

// GroupAccessSubjectFromContext returns the subject stored by TokenAuth, or
// falls back to the dashboard identity ("id" and "role" set by UserAuth) with
// userGroup as the user's own group.
func GroupAccessSubjectFromContext(c *gin.Context, userGroup string) GroupAccessSubject {
	if subject, ok := common.GetContextKeyType[GroupAccessSubject](c, constant.ContextKeyGroupAccessSubject); ok {
		return subject
	}
	return GroupAccessSubject{UserId: c.GetInt("id"), Role: c.GetInt("role"), Group: userGroup}
}

// GroupAccessDenial explains why a gated group is unavailable to a requester.
type GroupAccessDenial struct {
	Group         string
	MinTopup      float64
	CurrentTopup  decimal.Decimal
	WhitelistOnly bool
}

// Message returns the localized denial shown to the requester.
func (denial *GroupAccessDenial) Message(c *gin.Context) string {
	if denial.WhitelistOnly {
		return i18n.T(c, i18n.MsgGroupAccessWhitelistOnly, map[string]any{"Group": denial.Group})
	}
	return i18n.T(c, i18n.MsgGroupAccessTopupRequired, map[string]any{
		"Group":    denial.Group,
		"Required": decimal.NewFromFloat(denial.MinTopup).Round(2).String(),
		"Current":  denial.CurrentTopup.Truncate(2).String(),
	})
}

type groupAccessTopupMemo struct {
	userId          int
	countRedemption bool
	total           decimal.Decimal
}

// requestUserTopupTotal memoizes the cumulative top-up in the request context
// so repeated group checks (for example Auto group filtering) share one lookup.
func requestUserTopupTotal(c *gin.Context, userId int) (decimal.Decimal, error) {
	countRedemption := operation_setting.GroupAccessCountsRedemption()
	memo, ok := common.GetContextKeyType[groupAccessTopupMemo](c, constant.ContextKeyGroupAccessTopupTotal)
	if ok && memo.userId == userId && memo.countRedemption == countRedemption {
		return memo.total, nil
	}
	total, err := model.GetUserTopupTotal(userId, countRedemption)
	if err != nil {
		return decimal.Zero, err
	}
	common.SetContextKey(c, constant.ContextKeyGroupAccessTopupTotal, groupAccessTopupMemo{
		userId: userId, countRedemption: countRedemption, total: total,
	})
	return total, nil
}

// CheckGroupAccess returns nil when subject may use group. Ungated groups,
// administrators and the user's own group are always allowed; otherwise the
// whitelist or the cumulative top-up threshold decides. The gate only narrows
// access and never replaces the usable-group checks. When the cumulative
// top-up cannot be loaded the check fails closed: the denial is returned
// together with the error.
func CheckGroupAccess(c *gin.Context, subject GroupAccessSubject, group string) (*GroupAccessDenial, error) {
	requirement, whitelisted, gated := operation_setting.GetGroupAccessGate(group, subject.UserId)
	if !gated || subject.Role >= common.RoleAdminUser || group == subject.Group || whitelisted {
		return nil, nil
	}
	denial := &GroupAccessDenial{Group: group, MinTopup: requirement.MinTopup, WhitelistOnly: requirement.WhitelistOnly}
	if requirement.WhitelistOnly {
		return denial, nil
	}
	total, err := requestUserTopupTotal(c, subject.UserId)
	if err != nil {
		return denial, err
	}
	if total.GreaterThanOrEqual(decimal.NewFromFloat(requirement.MinTopup)) {
		return nil, nil
	}
	denial.CurrentTopup = total
	return denial, nil
}

// FilterGroupsByAccess keeps, in order, the groups subject may use.
func FilterGroupsByAccess(c *gin.Context, subject GroupAccessSubject, groups []string) []string {
	filtered := make([]string, 0, len(groups))
	for _, group := range groups {
		denial, err := CheckGroupAccess(c, subject, group)
		if err != nil {
			common.SysError(fmt.Sprintf("failed to check access to group %s for user %d: %v", group, subject.UserId, err))
		}
		if denial == nil {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

// LockedGroup describes a usable group the requester cannot use yet because
// of its top-up gate. Whitelists are never exposed.
type LockedGroup struct {
	Desc          string  `json:"desc"`
	Ratio         float64 `json:"ratio"`
	MinTopup      float64 `json:"min_topup"`
	CurrentTopup  float64 `json:"current_topup"`
	WhitelistOnly bool    `json:"whitelist_only"`
}

// GetLockedUsableGroups returns the selectable groups among usableGroups
// (group -> description) that subject cannot use. The cumulative top-up is
// only loaded when at least one group is locked.
func GetLockedUsableGroups(c *gin.Context, subject GroupAccessSubject, usableGroups map[string]string) map[string]LockedGroup {
	locked := make(map[string]LockedGroup)
	for group, desc := range usableGroups {
		if !ratio_setting.ContainsGroupRatio(group) {
			continue
		}
		denial, err := CheckGroupAccess(c, subject, group)
		if err != nil {
			common.SysError(fmt.Sprintf("failed to check access to group %s for user %d: %v", group, subject.UserId, err))
		}
		if denial == nil {
			continue
		}
		locked[group] = LockedGroup{
			Desc:          desc,
			Ratio:         GetUserGroupRatio(subject.Group, group),
			MinTopup:      denial.MinTopup,
			WhitelistOnly: denial.WhitelistOnly,
		}
	}
	if len(locked) == 0 {
		return locked
	}
	total, err := requestUserTopupTotal(c, subject.UserId)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to load cumulative top-up for user %d: %v", subject.UserId, err))
	}
	current := total.Truncate(2).InexactFloat64()
	for group, info := range locked {
		info.CurrentTopup = current
		locked[group] = info
	}
	return locked
}

// CheckPinnedChannelAccess denies a request pinned to channel (a follow-up on an
// earlier task) when every group the channel serves is recharge-gated and
// unavailable to the requester, so such follow-ups cannot bypass the gate. A
// channel that serves at least one group the requester may use is allowed.
func CheckPinnedChannelAccess(c *gin.Context, channel *model.Channel) (*GroupAccessDenial, error) {
	subject := GroupAccessSubjectFromContext(c, common.GetContextKeyString(c, constant.ContextKeyUserGroup))
	var firstDenial *GroupAccessDenial
	for _, group := range channel.GetGroups() {
		denial, err := CheckGroupAccess(c, subject, group)
		if err != nil {
			return denial, err
		}
		if denial == nil {
			return nil, nil
		}
		if firstDenial == nil {
			firstDenial = denial
		}
	}
	return firstDenial, nil
}

// PinnedChannelAccessMessage returns the localized reason a pinned follow-up is
// refused, or "" when it may proceed. A failed check is refused (fail closed).
func PinnedChannelAccessMessage(c *gin.Context, channel *model.Channel) string {
	denial, err := CheckPinnedChannelAccess(c, channel)
	if denial == nil {
		return ""
	}
	if err != nil {
		common.SysError(fmt.Sprintf("failed to check group access for channel %d: %v", channel.Id, err))
		return i18n.T(c, i18n.MsgDatabaseError)
	}
	return denial.Message(c)
}
