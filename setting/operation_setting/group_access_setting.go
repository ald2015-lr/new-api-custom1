package operation_setting

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// ---------------------------------------------------------------------------
// Recharge-gated groups (root-admin configurable)
// DB keys: group_access_setting.rules, group_access_setting.count_redemption
//
// A rule gates one group: a user may use it only when the cumulative top-up
// (in top-up units) reaches min_topup, or when the user id is whitelisted.
// min_topup == 0 means whitelist only. Exemptions (admins, the user's own
// group) are applied by the service layer.
// ---------------------------------------------------------------------------

const (
	GroupAccessRulesOptionKey           = "group_access_setting.rules"
	GroupAccessCountRedemptionOptionKey = "group_access_setting.count_redemption"

	groupAccessMaxMinTopup = 1e9
)

// GroupAccessSetting is registered with the config manager so the options are
// exported to the option map. Rules stays a JSON string so GET /api/option
// returns it verbatim; reads go through the compiled snapshot below.
type GroupAccessSetting struct {
	Rules           string `json:"rules"`
	CountRedemption bool   `json:"count_redemption"`
}

// GroupAccessUser is a whitelist entry. Enforcement uses Id only; Username is
// an informational snapshot refreshed whenever the rules are saved.
type GroupAccessUser struct {
	Id       int    `json:"id"`
	Username string `json:"username"`
}

type GroupAccessRule struct {
	Group    string            `json:"group"`
	MinTopup float64           `json:"min_topup"`
	Users    []GroupAccessUser `json:"users"`
}

// GroupAccessRequirement is the public part of a rule. Whitelists are never
// exposed through it.
type GroupAccessRequirement struct {
	MinTopup      float64 `json:"min_topup"`
	WhitelistOnly bool    `json:"whitelist_only"`
}

type groupAccessGate struct {
	requirement GroupAccessRequirement
	users       map[int]struct{}
}

// groupAccessSnapshot is replaced atomically so request paths read a
// consistent rule set without locking.
type groupAccessSnapshot struct {
	rules string
	gates map[string]groupAccessGate
}

var groupAccessSetting = GroupAccessSetting{
	Rules:           "[]",
	CountRedemption: true,
}

var (
	groupAccessCurrent         atomic.Pointer[groupAccessSnapshot]
	groupAccessCountRedemption atomic.Bool
)

func init() {
	config.GlobalConfig.Register("group_access_setting", &groupAccessSetting)
	groupAccessCurrent.Store(&groupAccessSnapshot{rules: groupAccessSetting.Rules, gates: map[string]groupAccessGate{}})
	groupAccessCountRedemption.Store(groupAccessSetting.CountRedemption)
}

// ParseGroupAccessRules decodes and validates a rules array. With
// requireUserIds every whitelist entry must carry a positive id (the stored
// form); otherwise an entry may carry only a username that is still to be
// resolved. Blank input is an empty rule list.
func ParseGroupAccessRules(value string, requireUserIds bool) ([]GroupAccessRule, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return []GroupAccessRule{}, nil
	}
	if common.GetJsonType(common.RawMessage(value)) != "array" {
		return nil, fmt.Errorf("分组充值门槛规则必须是 JSON 数组")
	}
	var rules []GroupAccessRule
	if err := common.UnmarshalJsonStr(value, &rules); err != nil {
		return nil, fmt.Errorf("解析分组充值门槛规则失败: %w", err)
	}
	seenGroups := make(map[string]struct{}, len(rules))
	for i := range rules {
		rule := &rules[i]
		rule.Group = strings.TrimSpace(rule.Group)
		if rule.Group == "" {
			return nil, fmt.Errorf("第 %d 条分组充值门槛规则的分组不能为空", i+1)
		}
		if rule.Group == "auto" {
			return nil, fmt.Errorf("auto 分组不能设置充值门槛")
		}
		if _, ok := seenGroups[rule.Group]; ok {
			return nil, fmt.Errorf("分组 %s 的充值门槛规则重复", rule.Group)
		}
		seenGroups[rule.Group] = struct{}{}
		if math.IsNaN(rule.MinTopup) || math.IsInf(rule.MinTopup, 0) || rule.MinTopup < 0 || rule.MinTopup > groupAccessMaxMinTopup {
			return nil, fmt.Errorf("分组 %s 的累计充值门槛必须是 0 到 %d 之间的数字", rule.Group, int64(groupAccessMaxMinTopup))
		}
		if rule.MinTopup == 0 {
			rule.MinTopup = 0 // normalize -0
		}
		for j := range rule.Users {
			user := &rule.Users[j]
			user.Username = strings.TrimSpace(user.Username)
			if user.Id < 0 || (user.Id == 0 && requireUserIds) {
				return nil, fmt.Errorf("分组 %s 的白名单用户 ID 必须是正整数", rule.Group)
			}
			if user.Id == 0 && user.Username == "" {
				return nil, fmt.Errorf("分组 %s 的白名单用户必须填写用户 ID 或用户名", rule.Group)
			}
		}
		if rule.Users == nil {
			rule.Users = []GroupAccessUser{}
		}
	}
	return rules, nil
}

// EncodeGroupAccessRules returns the normalized stored form: whitelist users
// sorted by id with duplicate ids removed. Every user must already carry an id.
func EncodeGroupAccessRules(rules []GroupAccessRule) (string, error) {
	normalized := make([]GroupAccessRule, 0, len(rules))
	for _, rule := range rules {
		users := make([]GroupAccessUser, 0, len(rule.Users))
		for _, user := range rule.Users {
			if user.Id <= 0 {
				return "", fmt.Errorf("分组 %s 的白名单用户 ID 无效", rule.Group)
			}
			users = append(users, user)
		}
		slices.SortStableFunc(users, func(a, b GroupAccessUser) int { return cmp.Compare(a.Id, b.Id) })
		users = slices.CompactFunc(users, func(a, b GroupAccessUser) bool { return a.Id == b.Id })
		rule.Users = users
		normalized = append(normalized, rule)
	}
	encoded, err := common.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func IsGroupAccessOptionKey(key string) bool {
	return key == GroupAccessRulesOptionKey || key == GroupAccessCountRedemptionOptionKey
}

// ValidateGroupAccessOption validates a value in its stored form.
func ValidateGroupAccessOption(key string, value string) error {
	switch key {
	case GroupAccessRulesOptionKey:
		_, err := ParseGroupAccessRules(value, true)
		return err
	case GroupAccessCountRedemptionOptionKey:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("是否计入兑换码必须是 true 或 false")
		}
	}
	return nil
}

// LoadGroupAccessOption applies a stored value. An invalid value is logged and
// leaves the previous configuration in effect; it is never partially applied.
func LoadGroupAccessOption(key string, value string) {
	switch key {
	case GroupAccessRulesOptionKey:
		rules, err := ParseGroupAccessRules(value, true)
		if err != nil {
			common.SysError("加载分组充值门槛规则失败，继续使用上一次的有效配置: " + err.Error())
			return
		}
		gates := make(map[string]groupAccessGate, len(rules))
		for _, rule := range rules {
			users := make(map[int]struct{}, len(rule.Users))
			for _, user := range rule.Users {
				users[user.Id] = struct{}{}
			}
			gates[rule.Group] = groupAccessGate{
				requirement: GroupAccessRequirement{MinTopup: rule.MinTopup, WhitelistOnly: rule.MinTopup == 0},
				users:       users,
			}
		}
		groupAccessSetting.Rules = value
		groupAccessCurrent.Store(&groupAccessSnapshot{rules: value, gates: gates})
	case GroupAccessCountRedemptionOptionKey:
		countRedemption, err := strconv.ParseBool(value)
		if err != nil {
			common.SysError("加载分组充值门槛的兑换码计入设置失败，继续使用上一次的有效配置: " + err.Error())
			return
		}
		groupAccessSetting.CountRedemption = countRedemption
		groupAccessCountRedemption.Store(countRedemption)
	}
}

// GetGroupAccessGate reports whether group is gated, its public requirement,
// and whether userId is on its whitelist.
func GetGroupAccessGate(group string, userId int) (requirement GroupAccessRequirement, whitelisted bool, gated bool) {
	gate, gated := groupAccessCurrent.Load().gates[group]
	if !gated {
		return GroupAccessRequirement{}, false, false
	}
	_, whitelisted = gate.users[userId]
	return gate.requirement, whitelisted, true
}

// GetGroupAccessRequirements returns the public requirement of every gated group.
func GetGroupAccessRequirements() map[string]GroupAccessRequirement {
	gates := groupAccessCurrent.Load().gates
	requirements := make(map[string]GroupAccessRequirement, len(gates))
	for group, gate := range gates {
		requirements[group] = gate.requirement
	}
	return requirements
}

// GroupAccessRulesJSON returns the stored form of the rules currently in effect.
func GroupAccessRulesJSON() string {
	return groupAccessCurrent.Load().rules
}

// GroupAccessCountsRedemption reports whether used redemption codes count
// toward the cumulative top-up.
func GroupAccessCountsRedemption() bool {
	return groupAccessCountRedemption.Load()
}
