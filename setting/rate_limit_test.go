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
