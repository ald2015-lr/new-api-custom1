package operation_setting

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// ---------------------------------------------------------------------------
// Free groups (root-admin configurable)
// DB key: group_billing_setting.free_groups (JSON array of group names)
//
// Requests whose using group is free do not charge the user's wallet,
// subscription or token quota. Channel usage, channel quota limits and usage
// logs still record the real cost. Legacy Midjourney billing is unaffected.
// ---------------------------------------------------------------------------

const FreeGroupsOptionKey = "group_billing_setting.free_groups"

// GroupBillingSetting is registered with the config manager so the option is
// exported to the option map; FreeGroups stays a JSON string so GET /api/option
// returns it verbatim. Reads go through the compiled set below.
type GroupBillingSetting struct {
	FreeGroups string `json:"free_groups"`
}

var groupBillingSetting = GroupBillingSetting{FreeGroups: "[]"}

var freeGroupSet atomic.Pointer[map[string]struct{}]

func init() {
	config.GlobalConfig.Register("group_billing_setting", &groupBillingSetting)
	empty := map[string]struct{}{}
	freeGroupSet.Store(&empty)
}

// ParseFreeGroups decodes and validates a free-group list; it touches no global state.
func ParseFreeGroups(value string) ([]string, error) {
	groups := []string{}
	if strings.TrimSpace(value) == "" {
		return groups, nil
	}
	if err := common.UnmarshalJsonStr(value, &groups); err != nil {
		return nil, fmt.Errorf("免费分组必须是分组名组成的 JSON 数组: %w", err)
	}
	for i, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			return nil, fmt.Errorf("免费分组名不能为空")
		}
		if group == "auto" {
			return nil, fmt.Errorf("auto 分组不能设为免费分组")
		}
		groups[i] = group
	}
	slices.Sort(groups)
	return slices.Compact(groups), nil
}

func IsGroupBillingOptionKey(key string) bool {
	return key == FreeGroupsOptionKey
}

// ValidateGroupBillingOption validates a value before it is stored.
func ValidateGroupBillingOption(_ string, value string) error {
	_, err := ParseFreeGroups(value)
	return err
}

// LoadGroupBillingOption applies a stored value. An invalid value is logged
// and leaves the previous list in effect.
func LoadGroupBillingOption(_ string, value string) {
	groups, err := ParseFreeGroups(value)
	if err != nil {
		common.SysError("加载免费分组失败，继续使用上一次的有效配置: " + err.Error())
		return
	}
	set := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		set[group] = struct{}{}
	}
	groupBillingSetting.FreeGroups = value
	freeGroupSet.Store(&set)
}

// IsFreeGroup reports whether requests in group do not charge the user.
func IsFreeGroup(group string) bool {
	if group == "" {
		return false
	}
	_, free := (*freeGroupSet.Load())[group]
	return free
}

// GetFreeGroups returns the free groups, sorted.
func GetFreeGroups() []string {
	set := *freeGroupSet.Load()
	groups := make([]string, 0, len(set))
	for group := range set {
		groups = append(groups, group)
	}
	slices.Sort(groups)
	return groups
}
