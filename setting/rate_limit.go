package setting

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// maxRateLimitDurationSeconds is the largest window the count cap is computed
// against (24h). Token-bucket capacity is count*duration; this keeps that
// product inside int64 when the window is at most a day.
const maxRateLimitDurationSeconds = 24 * 60 * 60

// maxModelRequestRateLimitCount is math.MaxInt64 / maxRateLimitDurationSeconds.
// It is the largest count that cannot overflow int64(count)*duration for a
// window of at most 24 hours.
const maxModelRequestRateLimitCount int64 = math.MaxInt64 / maxRateLimitDurationSeconds

// Bounds of the per-model rules. A rule's window is at most the 24 hours the
// count cap is computed for, and its counts have the bounds of the global count
// inputs on the settings page.
const (
	maxModelRateLimitRuleMinutes     = maxRateLimitDurationSeconds / 60
	maxModelRateLimitRuleCount       = 100_000_000
	maxModelRateLimitRules           = 1000
	maxModelRateLimitModelNameLength = 255
)

var ModelRequestRateLimitEnabled = false
var ModelRequestRateLimitDurationMinutes = 1
var ModelRequestRateLimitCount = 0
var ModelRequestRateLimitSuccessCount = 1000
var ModelRequestRateLimitGroup = map[string][2]int{}
var ModelRequestRateLimitModel = map[string]ModelRequestRateLimitRule{}
var ModelRequestRateLimitMutex sync.RWMutex

// ModelRequestRateLimitRule limits the requests each user sends to one model,
// keyed by the exact model name the client sends. It applies in addition to
// the global or group limit, so the stricter of the two rejects first. A zero
// count does not limit.
type ModelRequestRateLimitRule struct {
	DurationMinutes int `json:"duration_minutes"`
	TotalCount      int `json:"total_count"`
	SuccessCount    int `json:"success_count"`
}

func ModelRequestRateLimitGroup2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := common.Marshal(ModelRequestRateLimitGroup)
	if err != nil {
		common.SysLog(common.LogText("error marshalling model ratio: %s", err.Error()))
	}
	return string(jsonBytes)
}

func UpdateModelRequestRateLimitGroupByJSONString(jsonStr string) error {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	ModelRequestRateLimitGroup = make(map[string][2]int)
	return common.Unmarshal([]byte(jsonStr), &ModelRequestRateLimitGroup)
}

func GetGroupRateLimit(group string) (totalCount, successCount int, found bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	if ModelRequestRateLimitGroup == nil {
		return 0, 0, false
	}

	limits, found := ModelRequestRateLimitGroup[group]
	if !found {
		return 0, 0, false
	}
	return limits[0], limits[1], true
}

func CheckModelRequestRateLimitGroup(jsonStr string) error {
	checkModelRequestRateLimitGroup := make(map[string][2]int)
	err := common.Unmarshal([]byte(jsonStr), &checkModelRequestRateLimitGroup)
	if err != nil {
		return err
	}
	for group, limits := range checkModelRequestRateLimitGroup {
		if limits[0] < 0 || limits[1] < 1 {
			return fmt.Errorf("group %s has negative rate limit values: [%d, %d]", group, limits[0], limits[1])
		}
		if int64(limits[0]) > maxModelRequestRateLimitCount || int64(limits[1]) > maxModelRequestRateLimitCount {
			return fmt.Errorf("group %s [%d, %d] exceeds max rate limit %d", group, limits[0], limits[1], maxModelRequestRateLimitCount)
		}
	}

	return nil
}

func ModelRequestRateLimitModel2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := common.Marshal(ModelRequestRateLimitModel)
	if err != nil {
		common.SysLog(common.LogText("error marshalling model request rate limits: %s", err.Error()))
	}
	return string(jsonBytes)
}

// UpdateModelRequestRateLimitModelByJSONString replaces the per-model rules.
// An invalid value keeps the current rules, so a bad stored option is logged
// at startup instead of clearing or half-applying them.
func UpdateModelRequestRateLimitModelByJSONString(jsonStr string) error {
	rules, err := parseModelRequestRateLimitModel(jsonStr)
	if err != nil {
		return err
	}
	ModelRequestRateLimitMutex.Lock()
	defer ModelRequestRateLimitMutex.Unlock()

	ModelRequestRateLimitModel = rules
	return nil
}

func GetModelRequestRateLimitRule(model string) (ModelRequestRateLimitRule, bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	rule, found := ModelRequestRateLimitModel[model]
	return rule, found
}

func CheckModelRequestRateLimitModel(jsonStr string) error {
	_, err := parseModelRequestRateLimitModel(jsonStr)
	return err
}

func parseModelRequestRateLimitModel(jsonStr string) (map[string]ModelRequestRateLimitRule, error) {
	var rules map[string]ModelRequestRateLimitRule
	if err := common.UnmarshalJsonStr(jsonStr, &rules); err != nil || rules == nil {
		return nil, common.NewMessage("Model rate limits must be a JSON object that maps model names to rules")
	}
	if len(rules) > maxModelRateLimitRules {
		return nil, common.NewMessage("Model rate limits can list at most {{max}} models", map[string]any{"max": maxModelRateLimitRules})
	}
	// Sorted, so the first invalid model reported is the same on every call.
	for _, model := range slices.Sorted(maps.Keys(rules)) {
		rule := rules[model]
		if model == "" || model != strings.TrimSpace(model) || utf8.RuneCountInString(model) > maxModelRateLimitModelNameLength {
			return nil, common.NewMessage("Model names in model rate limits must have 1 to {{max}} characters and no leading or trailing spaces: \"{{model}}\"", map[string]any{"max": maxModelRateLimitModelNameLength, "model": model})
		}
		if rule.DurationMinutes < 1 || rule.DurationMinutes > maxModelRateLimitRuleMinutes {
			return nil, common.NewMessage("The time window of the rate limit for model {{model}} must be 1 to {{max}} minutes", map[string]any{"max": maxModelRateLimitRuleMinutes, "model": model})
		}
		if rule.TotalCount < 0 || rule.TotalCount > maxModelRateLimitRuleCount || rule.SuccessCount < 0 || rule.SuccessCount > maxModelRateLimitRuleCount {
			return nil, common.NewMessage("The request counts of the rate limit for model {{model}} must be 0 to {{max}}", map[string]any{"max": maxModelRateLimitRuleCount, "model": model})
		}
	}
	return rules, nil
}
