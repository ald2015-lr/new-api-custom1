package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoreModelSquareDisplayOptions puts the model square options back as they
// were when the test started.
func restoreModelSquareDisplayOptions(t *testing.T) {
	t.Helper()
	previous := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		for key, value := range previous {
			if operation_setting.IsModelSquareOptionKey(key) {
				operation_setting.LoadModelSquareOption(key, value)
			}
		}
	})
}

func TestUpdateModelSquareOptionsSavesAllOrNothing(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	restoreModelSquareDisplayOptions(t)
	previousOptions := common.OptionMap
	t.Cleanup(func() { common.OptionMap = previousOptions })
	common.OptionMap = map[string]string{}
	put := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/option/model_square", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", 9999)
		c.Set("role", common.RoleRootUser)
		c.Set("username", "root-operator")
		UpdateModelSquareOptions(c)
		return recorder
	}
	stored := func() map[string]string {
		var options []model.Option
		require.NoError(t, db.Find(&options).Error)
		values := make(map[string]string, len(options))
		for _, option := range options {
			values[option.Key] = option.Value
		}
		return values
	}

	recorder := put(`{"options":{"model_square_setting.show_tag_filter":true,"model_square_setting.snapshot_fold_enabled":false,"model_square_setting.family_bases":"[\"claude-opus-4-5\"]"}}`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	saved := map[string]string{
		"model_square_setting.show_tag_filter":       "true",
		"model_square_setting.snapshot_fold_enabled": "false",
		"model_square_setting.family_bases":          `["claude-opus-4-5"]`,
	}
	assert.Equal(t, saved, stored())
	display := operation_setting.GetModelSquareDisplay()
	assert.True(t, display.VariantGroupingEnabled)
	assert.True(t, display.VersionSpellingFoldEnabled)
	assert.False(t, display.SnapshotFoldEnabled)
	assert.Equal(t, []string{"claude-opus-4-5"}, display.FamilyBases)
	assert.Equal(t, operation_setting.ModelSquareFilterSections{Groups: true, Vendors: true, Tags: true, QuotaTypes: true, EndpointTypes: false}, display.FilterSections)
	for key, value := range saved {
		assert.Equal(t, value, common.OptionMap[key], key)
	}

	// Like PUT /api/option/: one row per saved option, with its name and
	// never its value.
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Order("id").Find(&audits).Error)
	params := make([]string, 0, len(audits))
	for _, audit := range audits {
		assert.Equal(t, "option.update", audit.Action)
		assert.Equal(t, 9999, audit.UserId)
		require.NotNil(t, audit.Other.Op)
		encoded, err := common.Marshal(audit.Other.Op.Params)
		require.NoError(t, err)
		params = append(params, string(encoded))
		row, err := common.Marshal(audit)
		require.NoError(t, err)
		assert.NotContains(t, string(row), "claude-opus-4-5")
	}
	assert.Equal(t, []string{
		`{"key":"model_square_setting.family_bases"}`,
		`{"key":"model_square_setting.show_tag_filter"}`,
		`{"key":"model_square_setting.snapshot_fold_enabled"}`,
	}, params)

	for _, tc := range []struct {
		name, body, message string
		status              int
	}{
		{"a switch value the server refuses", `{"options":{"model_square_setting.show_vendor_filter":"yes","model_square_setting.show_tag_filter":false}}`, "是否显示供应商筛选必须是 true 或 false", http.StatusOK},
		{"base models the server refuses", `{"options":{"model_square_setting.family_bases":"[\"gemini 4\"]","model_square_setting.show_tag_filter":false}}`, "第 1 个基础模型不能包含空白字符", http.StatusOK},
		{"an option that is not the model square's", `{"options":{"model_square_setting.show_tag_filter":false,"QuotaForInviter":1000}}`, "Invalid parameters", http.StatusBadRequest},
		{"an unknown model square option", `{"options":{"model_square_setting.show_tag_filter":false,"model_square_setting.unknown":true}}`, "Invalid parameters", http.StatusBadRequest},
		{"no option", `{"options":{}}`, "Invalid parameters", http.StatusBadRequest},
		{"not JSON", `{"options":`, "Invalid parameters", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := put(tc.body)
			assert.Equal(t, tc.status, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			if tc.status == http.StatusBadRequest {
				assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"message_key":%q`, tc.message))
			} else {
				assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"message":%q`, tc.message))
			}
			assert.Equal(t, saved, stored(), "nothing is saved")
			assert.Equal(t, display, operation_setting.GetModelSquareDisplay(), "nothing is applied")
			var count int64
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&count).Error)
			assert.Equal(t, int64(3), count, "nothing is audited")
		})
	}
}
