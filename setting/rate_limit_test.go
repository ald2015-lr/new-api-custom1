package setting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckModelRequestRateLimitModelAcceptsRulesWithinBounds(t *testing.T) {
	for _, value := range []string{
		`{}`,
		`{"[按次]claude-opus-4-6": {"duration_minutes": 1, "total_count": 0, "success_count": 0}}`,
		`{"gpt-4o": {"duration_minutes": 1440, "total_count": 100000000, "success_count": 100000000}}`,
		fmt.Sprintf(`{%q: {"duration_minutes": 5, "total_count": 10, "success_count": 5}}`, strings.Repeat("模", 255)),
		`{"gpt-4o": {"duration_minutes": 1, "total_count": 1, "match": "exact", "shared": false}}`,
		`{"[按次]": {"duration_minutes": 1, "total_count": 8, "success_count": 8, "match": "prefix"}}`,
		`{"[": {"duration_minutes": 1, "total_count": 8, "match": "prefix", "shared": true}}`,
		`{"-thinking": {"duration_minutes": 1, "total_count": 8, "match": "suffix", "shared": true}}`,
		// "*" is a literal character that wildcard-normalized names contain.
		`{"gpt-4-gizmo-*": {"duration_minutes": 1, "total_count": 1}}`,
		`{"-thinking-*": {"duration_minutes": 1, "total_count": 1, "match": "suffix"}}`,
	} {
		assert.NoError(t, CheckModelRequestRateLimitModel(value), value)
	}
}

func TestCheckModelRequestRateLimitModelRejectsInvalidRules(t *testing.T) {
	tooMany := make(map[string]ModelRequestRateLimitRule, maxModelRateLimitRules+1)
	for i := range maxModelRateLimitRules + 1 {
		tooMany[fmt.Sprintf("model-%d", i)] = ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 1}
	}
	tooManyJSON, err := common.Marshal(tooMany)
	require.NoError(t, err)

	const (
		notObject = "Model rate limits must be a JSON object that maps model names to rules"
		badName   = "Model names in model rate limits must have 1 to {{max}} characters and no leading or trailing spaces: \"{{model}}\""
		badWindow = "The time window of the rate limit for model {{model}} must be 1 to {{max}} minutes"
		badCount  = "The request counts of the rate limit for model {{model}} must be 0 to {{max}}"
		badMatch  = "The match of the rate limit for model {{model}} must be exact, prefix or suffix"
		badShared = "The rate limit for model {{model}} can count matching models together only with prefix or suffix match"
		badPrefix = "The prefix {{model}} of a model rate limit must not end with *: a prefix rule already matches every model whose name starts with it"
		badSuffix = "The suffix {{model}} of a model rate limit must not start with *: a suffix rule already matches every model whose name ends with it"
	)
	for _, tc := range []struct {
		name, value, key string
	}{
		{"malformed JSON", `{"gpt-4o":`, notObject},
		{"JSON null", `null`, notObject},
		{"JSON array", `[]`, notObject},
		{"rule is not an object", `{"gpt-4o": [1, 2]}`, notObject},
		{"fractional count", `{"gpt-4o": {"duration_minutes": 1, "total_count": 1.5}}`, notObject},
		{"too many models", string(tooManyJSON), "Model rate limits can list at most {{max}} models"},
		{"empty model name", `{"": {"duration_minutes": 1, "total_count": 1}}`, badName},
		{"blank model name", `{"  ": {"duration_minutes": 1, "total_count": 1}}`, badName},
		{"padded model name", `{" gpt-4o": {"duration_minutes": 1, "total_count": 1}}`, badName},
		{"model name too long", fmt.Sprintf(`{%q: {"duration_minutes": 1, "total_count": 1}}`, strings.Repeat("模", 256)), badName},
		{"missing window", `{"gpt-4o": {"total_count": 1}}`, badWindow},
		{"window above one day", `{"gpt-4o": {"duration_minutes": 1441, "total_count": 1}}`, badWindow},
		{"negative total count", `{"gpt-4o": {"duration_minutes": 1, "total_count": -1}}`, badCount},
		{"negative success count", `{"gpt-4o": {"duration_minutes": 1, "success_count": -1}}`, badCount},
		{"total count too large", `{"gpt-4o": {"duration_minutes": 1, "total_count": 100000001}}`, badCount},
		{"success count too large", `{"gpt-4o": {"duration_minutes": 1, "success_count": 100000001}}`, badCount},
		{"unknown match", `{"gpt-": {"duration_minutes": 1, "total_count": 1, "match": "contains"}}`, badMatch},
		{"match in another case", `{"gpt-": {"duration_minutes": 1, "total_count": 1, "match": "Prefix"}}`, badMatch},
		{"match is not a string", `{"gpt-": {"duration_minutes": 1, "total_count": 1, "match": 1}}`, notObject},
		{"shared without match", `{"gpt-4o": {"duration_minutes": 1, "total_count": 1, "shared": true}}`, badShared},
		{"shared exact rule", `{"gpt-4o": {"duration_minutes": 1, "total_count": 1, "match": "exact", "shared": true}}`, badShared},
		{"shared is not a boolean", `{"gpt-": {"duration_minutes": 1, "total_count": 1, "match": "prefix", "shared": "yes"}}`, notObject},
		// The way the settings table shows a pattern rule, which as a key
		// would match no model.
		{"prefix ending with *", `{"[按次]*": {"duration_minutes": 1, "total_count": 8, "match": "prefix"}}`, badPrefix},
		{"suffix starting with *", `{"*-thinking": {"duration_minutes": 1, "total_count": 8, "match": "suffix", "shared": true}}`, badSuffix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckModelRequestRateLimitModel(tc.value)
			var message *common.Message
			require.ErrorAs(t, err, &message)
			assert.Equal(t, tc.key, message.Key)
		})
	}
}

// A stored value that fails validation is reported and leaves the rules in
// effect unchanged, so loading options at startup cannot clear them.
func TestUpdateModelRequestRateLimitModelKeepsRulesOnInvalidValue(t *testing.T) {
	previous := ModelRequestRateLimitModel2JSONString()
	t.Cleanup(func() { require.NoError(t, UpdateModelRequestRateLimitModelByJSONString(previous)) })
	require.NoError(t, UpdateModelRequestRateLimitModelByJSONString(`{"gpt-4o": {"duration_minutes": 2, "total_count": 3, "success_count": 1}}`))

	assert.Error(t, UpdateModelRequestRateLimitModelByJSONString(`{"gpt-4o": {"duration_minutes": 0, "total_count": 3}}`))
	assert.Error(t, UpdateModelRequestRateLimitModelByJSONString(`not json`))

	name, rule, found := FindModelRequestRateLimitRule("gpt-4o")
	require.True(t, found)
	assert.Equal(t, "gpt-4o", name)
	assert.Equal(t, ModelRequestRateLimitRule{DurationMinutes: 2, TotalCount: 3, SuccessCount: 1}, rule)
	assert.JSONEq(t, `{"gpt-4o": {"duration_minutes": 2, "total_count": 3, "success_count": 1}}`, ModelRequestRateLimitModel2JSONString())
}

// A request is limited by the exact rule of the first of its names that has
// one, then by every prefix or suffix rule matching its requested name, that
// name without its @ modifiers, or its routing name, in key order. Only exact
// rules are found by name.
func TestFindModelRequestRateLimitRulesReturnsTheExactRuleThenMatchingPatterns(t *testing.T) {
	previous := ModelRequestRateLimitModel2JSONString()
	t.Cleanup(func() { require.NoError(t, UpdateModelRequestRateLimitModelByJSONString(previous)) })
	const rules = `{
		"gpt-4o": {"duration_minutes": 1, "total_count": 3, "success_count": 0},
		"gpt-": {"duration_minutes": 2, "total_count": 1, "success_count": 0, "match": "prefix", "shared": true},
		"-4o": {"duration_minutes": 3, "total_count": 1, "success_count": 0, "match": "suffix"},
		"[按次]": {"duration_minutes": 4, "total_count": 8, "success_count": 8, "match": "prefix"},
		"-thinking": {"duration_minutes": 5, "total_count": 1, "success_count": 0, "match": "suffix"},
		"@effort:high": {"duration_minutes": 6, "total_count": 1, "success_count": 0, "match": "suffix", "shared": true},
		"opus-4-6": {"duration_minutes": 7, "total_count": 1, "success_count": 0, "match": "suffix"}
	}`
	require.NoError(t, UpdateModelRequestRateLimitModelByJSONString(rules))

	exact := ModelRequestRateLimitMatch{Key: "gpt-4o", Rule: ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 3}}
	suffix := ModelRequestRateLimitMatch{Key: "-4o", Rule: ModelRequestRateLimitRule{DurationMinutes: 3, TotalCount: 1, Match: ModelRateLimitMatchSuffix}}
	prefix := ModelRequestRateLimitMatch{Key: "gpt-", Rule: ModelRequestRateLimitRule{DurationMinutes: 2, TotalCount: 1, Match: ModelRateLimitMatchPrefix, Shared: true}}
	tag := ModelRequestRateLimitMatch{Key: "[按次]", Rule: ModelRequestRateLimitRule{DurationMinutes: 4, TotalCount: 8, SuccessCount: 8, Match: ModelRateLimitMatchPrefix}}
	thinking := ModelRequestRateLimitMatch{Key: "-thinking", Rule: ModelRequestRateLimitRule{DurationMinutes: 5, TotalCount: 1, Match: ModelRateLimitMatchSuffix}}
	effort := ModelRequestRateLimitMatch{Key: "@effort:high", Rule: ModelRequestRateLimitRule{DurationMinutes: 6, TotalCount: 1, Match: ModelRateLimitMatchSuffix, Shared: true}}
	opus := ModelRequestRateLimitMatch{Key: "opus-4-6", Rule: ModelRequestRateLimitRule{DurationMinutes: 7, TotalCount: 1, Match: ModelRateLimitMatchSuffix}}
	for _, tc := range []struct {
		name                                     string
		requested, formatted, unmodified, routed string
		want                                     []ModelRequestRateLimitMatch
	}{
		{"exact rule and both patterns", "gpt-4o", "gpt-4o", "gpt-4o", "gpt-4o", []ModelRequestRateLimitMatch{exact, suffix, prefix}},
		{"@ modifier variant", "gpt-4o@temperature:1", "gpt-4o@temperature:1", "gpt-4o", "gpt-4o", []ModelRequestRateLimitMatch{exact, suffix, prefix}},
		{"prefix only", "gpt-5", "gpt-5", "gpt-5", "gpt-5", []ModelRequestRateLimitMatch{prefix}},
		// Only the requested name ends with the @ modifier.
		{"suffix of the requested name only", "gpt-5@effort:high", "gpt-5@effort:high", "gpt-5", "gpt-5", []ModelRequestRateLimitMatch{effort, prefix}},
		// The routing name claude-opus-4-6 drops -thinking, and only it ends
		// with opus-4-6.
		{"legacy suffix the routing name drops", "claude-opus-4-6-thinking", "claude-opus-4-6-thinking", "claude-opus-4-6-thinking", "claude-opus-4-6", []ModelRequestRateLimitMatch{thinking, opus}},
		// Only the name without its @ modifiers ends with -thinking.
		{"legacy suffix before @ modifiers", "claude-opus-4-6-thinking@effort:high", "claude-opus-4-6-thinking@effort:high", "claude-opus-4-6-thinking", "claude-opus-4-6", []ModelRequestRateLimitMatch{thinking, effort, opus}},
		{"line tag", "[按次]gpt-5@effort:low", "[按次]gpt-5@effort:low", "[按次]gpt-5", "[按次]gpt-5", []ModelRequestRateLimitMatch{tag}},
		{"no rule", "claude-sonnet-4-5", "claude-sonnet-4-5", "claude-sonnet-4-5", "claude-sonnet-4-5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, FindModelRequestRateLimitRules(tc.requested, tc.formatted, tc.unmodified, tc.routed))
		})
	}

	_, _, found := FindModelRequestRateLimitRule("gpt-", "[按次]")
	assert.False(t, found, "a prefix rule is not an exact rule of its key")
	// Exact rules export without the new fields, as they were saved before.
	assert.JSONEq(t, rules, ModelRequestRateLimitModel2JSONString())
}
