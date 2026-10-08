package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOptionValueRejectsInvalidMaxTokenAutoGroups(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue("MaxTokenAutoGroups", value))
		})
	}
	require.NoError(t, validateOptionValue("MaxTokenAutoGroups", "999999"))
}

func TestValidateOptionValueRejectsPingIntervalOutsideRange(t *testing.T) {
	const key = "general_setting.ping_interval_seconds"
	for _, value := range []string{"", "0", "-1", "3601", "1.5", "ten"} {
		t.Run(value, func(t *testing.T) {
			assert.Error(t, validateOptionValue(key, value))
		})
	}
	for _, value := range []string{"1", "10", "3600"} {
		require.NoError(t, validateOptionValue(key, value))
	}
}

func TestValidateOptionValueGroupAccessRules(t *testing.T) {
	const key = "group_access_setting.rules"
	for name, value := range map[string]string{
		"not an array":     `{"group":"svip"}`,
		"empty group":      `[{"group":" ","min_topup":1}]`,
		"auto group":       `[{"group":"auto","min_topup":1}]`,
		"duplicate group":  `[{"group":"svip","min_topup":1},{"group":"svip","min_topup":2}]`,
		"negative minimum": `[{"group":"svip","min_topup":-1}]`,
		"minimum too high": `[{"group":"svip","min_topup":1000000001}]`,
		"unresolved user":  `[{"group":"svip","min_topup":1,"users":[{"username":"alice"}]}]`,
		"zero user id":     `[{"group":"svip","min_topup":1,"users":[{"id":0,"username":"alice"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateOptionValue(key, value))
		})
	}
	require.NoError(t, validateOptionValue(key, `[{"group":"svip","min_topup":0,"users":[{"id":3,"username":"alice"}]}]`))
	require.NoError(t, validateOptionValue(key, `[]`))
	assert.Error(t, validateOptionValue("group_access_setting.count_redemption", "yes"))
	require.NoError(t, validateOptionValue("group_access_setting.count_redemption", "false"))
}
