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

// The rules of ModelRequestRateLimitModel as a request looks them up: the
// exact rules by key, and the prefix and suffix rules sorted by key. They are
// replaced together with ModelRequestRateLimitModel.
var modelRequestRateLimitExact = map[string]ModelRequestRateLimitRule{}
var modelRequestRateLimitPatterns []ModelRequestRateLimitMatch

// How the key of a per-model rule matches model names. A rule without a match
// is an exact rule, so rules saved before match modes existed keep their
// meaning.
const (
	ModelRateLimitMatchExact  = "exact"
	ModelRateLimitMatchPrefix = "prefix"
	ModelRateLimitMatchSuffix = "suffix"
)

// ModelRequestRateLimitRule limits the requests each user sends to the models
// its key matches. An exact rule matches the name a request sends, or else
// that name's base model; a prefix or suffix rule matches every name that
// starts or ends with its key (see middleware.PerModelRequestRateLimit). Every
// rule that matches a request applies, in addition to the global or group
// limit, so the strictest rejects first. A zero count does not limit.
type ModelRequestRateLimitRule struct {
	DurationMinutes int `json:"duration_minutes"`
	TotalCount      int `json:"total_count"`
	SuccessCount    int `json:"success_count"`
	// Match is ModelRateLimitMatchExact (also when empty),
	// ModelRateLimitMatchPrefix or ModelRateLimitMatchSuffix.
	Match string `json:"match,omitempty"`
	// Shared counts the requests to all models a prefix or suffix rule
	// matches together. Otherwise each model is counted on its own, as if it
	// had an exact rule. Exact rules leave it false.
	Shared bool `json:"shared,omitempty"`
}

// IsPattern reports whether the rule is a prefix or suffix rule.
func (r ModelRequestRateLimitRule) IsPattern() bool {
	return r.Match == ModelRateLimitMatchPrefix || r.Match == ModelRateLimitMatchSuffix
}

// ModelRequestRateLimitMatch is a rule that applies to a request, with the key
// it is saved under.
type ModelRequestRateLimitMatch struct {
	Key  string
	Rule ModelRequestRateLimitRule
}

// matches reports whether the prefix or suffix rule m matches model name.
func (m ModelRequestRateLimitMatch) matches(name string) bool {
	if m.Rule.Match == ModelRateLimitMatchPrefix {
		return strings.HasPrefix(name, m.Key)
	}
	return strings.HasSuffix(name, m.Key)
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
	exact := make(map[string]ModelRequestRateLimitRule, len(rules))
	var patterns []ModelRequestRateLimitMatch
	for key, rule := range rules {
		if rule.IsPattern() {
			patterns = append(patterns, ModelRequestRateLimitMatch{Key: key, Rule: rule})
		} else {
			exact[key] = rule
		}
	}
	// Keys are unique, so this order is the same on every load.
	slices.SortFunc(patterns, func(a, b ModelRequestRateLimitMatch) int { return strings.Compare(a.Key, b.Key) })

	ModelRequestRateLimitMutex.Lock()
	defer ModelRequestRateLimitMutex.Unlock()

	ModelRequestRateLimitModel = rules
	modelRequestRateLimitExact = exact
	modelRequestRateLimitPatterns = patterns
	return nil
}

// FindModelRequestRateLimitRule returns the exact rule of the first of names
// that has one, and that name.
func FindModelRequestRateLimitRule(names ...string) (string, ModelRequestRateLimitRule, bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	return findExactModelRequestRateLimitRule(names...)
}

// FindModelRequestRateLimitRules returns every rule that applies to a request
// for model requested, whose wildcard-normalized name is formatted, whose name
// without its @ modifiers is unmodified, and whose routing name is routing:
// first the exact rule of the first of requested, formatted and routing that
// has one, then, by key, each prefix or suffix rule that matches requested,
// unmodified or routing. The routing name drops a legacy suffix such as the
// -thinking of claude-opus-4-6-thinking, so unmodified is what a -thinking
// rule matches when the name also has @ modifiers.
func FindModelRequestRateLimitRules(requested, formatted, unmodified, routing string) []ModelRequestRateLimitMatch {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	var matches []ModelRequestRateLimitMatch
	if key, rule, found := findExactModelRequestRateLimitRule(requested, formatted, routing); found {
		matches = append(matches, ModelRequestRateLimitMatch{Key: key, Rule: rule})
	}
	for _, pattern := range modelRequestRateLimitPatterns {
		if pattern.matches(requested) || pattern.matches(unmodified) || pattern.matches(routing) {
			matches = append(matches, pattern)
		}
	}
	return matches
}

// findExactModelRequestRateLimitRule is FindModelRequestRateLimitRule for a
// caller that holds ModelRequestRateLimitMutex.
func findExactModelRequestRateLimitRule(names ...string) (string, ModelRequestRateLimitRule, bool) {
	for _, name := range names {
		if rule, found := modelRequestRateLimitExact[name]; found {
			return name, rule, true
		}
	}
	return "", ModelRequestRateLimitRule{}, false
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
		switch rule.Match {
		case "", ModelRateLimitMatchExact:
			// Only a prefix or suffix rule matches several models to count
			// together, so the saved value has one reading.
			if rule.Shared {
				return nil, common.NewMessage("The rate limit for model {{model}} can count matching models together only with prefix or suffix match", map[string]any{"model": model})
			}
		// The match mode is what makes a key a wildcard, and "*" in a key is a
		// literal character, so "[按次]*", the way the settings table shows a
		// prefix rule, would match no model. "*" elsewhere in a key can match:
		// wildcard-normalized names such as gpt-4-gizmo-* end with it.
		case ModelRateLimitMatchPrefix:
			if strings.HasSuffix(model, "*") {
				return nil, common.NewMessage("The prefix {{model}} of a model rate limit must not end with *: a prefix rule already matches every model whose name starts with it", map[string]any{"model": model})
			}
		case ModelRateLimitMatchSuffix:
			if strings.HasPrefix(model, "*") {
				return nil, common.NewMessage("The suffix {{model}} of a model rate limit must not start with *: a suffix rule already matches every model whose name ends with it", map[string]any{"model": model})
			}
		default:
			return nil, common.NewMessage("The match of the rate limit for model {{model}} must be exact, prefix or suffix", map[string]any{"model": model})
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
