package operation_setting

import (
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var modelSquareBoolOptionKeys = []string{
	ModelSquareVariantGroupingEnabledOptionKey,
	ModelSquareVersionSpellingFoldOptionKey,
	ModelSquareSnapshotFoldOptionKey,
	ModelSquareShowGroupFilterOptionKey,
	ModelSquareShowVendorFilterOptionKey,
	ModelSquareShowTagFilterOptionKey,
	ModelSquareShowQuotaTypeFilterOptionKey,
	ModelSquareShowEndpointTypeFilterOptionKey,
}

func allModelSquareOptionKeys() []string {
	return slices.Concat(modelSquareBoolOptionKeys, []string{ModelSquareVariantRulesOptionKey, ModelSquareFamilyBasesOptionKey})
}

// restoreModelSquareOptions puts every model square option back as it was
// when the test started.
func restoreModelSquareOptions(t *testing.T) {
	t.Helper()
	previous := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		for _, key := range allModelSquareOptionKeys() {
			LoadModelSquareOption(key, previous[key])
		}
	})
}

func TestModelSquareDisplayDefaults(t *testing.T) {
	display := GetModelSquareDisplay()
	assert.Equal(t, ModelSquareDisplay{
		VariantGroupingEnabled:     true,
		VersionSpellingFoldEnabled: true,
		SnapshotFoldEnabled:        true,
		VariantRules:               defaultModelVariantRules,
		FamilyBases:                []string{},
		// Model tags and endpoint types are hidden until an admin shows them.
		FilterSections: ModelSquareFilterSections{Groups: true, Vendors: true, Tags: false, QuotaTypes: true, EndpointTypes: false},
	}, display)

	// The option map gets every switch with its default, so a site that has
	// no stored row for a new key (an upgrade) uses the default.
	exported := config.GlobalConfig.ExportAllConfigs()
	for key, want := range map[string]string{
		ModelSquareVariantGroupingEnabledOptionKey: "true",
		ModelSquareVersionSpellingFoldOptionKey:    "true",
		ModelSquareSnapshotFoldOptionKey:           "true",
		ModelSquareShowGroupFilterOptionKey:        "true",
		ModelSquareShowVendorFilterOptionKey:       "true",
		ModelSquareShowTagFilterOptionKey:          "false",
		ModelSquareShowQuotaTypeFilterOptionKey:    "true",
		ModelSquareShowEndpointTypeFilterOptionKey: "false",
		ModelSquareFamilyBasesOptionKey:            "[]",
	} {
		assert.Equal(t, want, exported[key], key)
	}
	assert.Len(t, modelSquareBoolOptions, len(modelSquareBoolOptionKeys))
}

func TestValidateModelSquareBoolOptions(t *testing.T) {
	labels := map[string]string{
		ModelSquareVariantGroupingEnabledOptionKey: "是否合并模型变体",
		ModelSquareVersionSpellingFoldOptionKey:    "是否合并版本号写法",
		ModelSquareSnapshotFoldOptionKey:           "是否合并带日期的快照",
		ModelSquareShowGroupFilterOptionKey:        "是否显示分组筛选",
		ModelSquareShowVendorFilterOptionKey:       "是否显示供应商筛选",
		ModelSquareShowTagFilterOptionKey:          "是否显示模型标签筛选",
		ModelSquareShowQuotaTypeFilterOptionKey:    "是否显示定价类型筛选",
		ModelSquareShowEndpointTypeFilterOptionKey: "是否显示端点类型筛选",
	}
	for _, key := range modelSquareBoolOptionKeys {
		t.Run(key, func(t *testing.T) {
			for _, value := range []string{"true", "false", "1", "0"} {
				assert.NoError(t, ValidateModelSquareOption(key, value), value)
			}
			for _, value := range []string{"yes", ""} {
				err := ValidateModelSquareOption(key, value)
				require.Error(t, err, value)
				assert.Equal(t, labels[key]+"必须是 true 或 false", err.Error(), value)
			}
		})
	}
}

func TestLoadModelSquareBoolOptions(t *testing.T) {
	restoreModelSquareOptions(t)
	sections := func() ModelSquareFilterSections { return GetModelSquareDisplay().FilterSections }

	LoadModelSquareOption(ModelSquareVersionSpellingFoldOptionKey, "false")
	LoadModelSquareOption(ModelSquareSnapshotFoldOptionKey, "0")
	LoadModelSquareOption(ModelSquareShowGroupFilterOptionKey, "false")
	LoadModelSquareOption(ModelSquareShowVendorFilterOptionKey, "false")
	LoadModelSquareOption(ModelSquareShowTagFilterOptionKey, "true")
	LoadModelSquareOption(ModelSquareShowQuotaTypeFilterOptionKey, "false")
	LoadModelSquareOption(ModelSquareShowEndpointTypeFilterOptionKey, "1")
	display := GetModelSquareDisplay()
	assert.True(t, display.VariantGroupingEnabled)
	assert.False(t, display.VersionSpellingFoldEnabled)
	assert.False(t, display.SnapshotFoldEnabled)
	assert.Equal(t, ModelSquareFilterSections{Groups: false, Vendors: false, Tags: true, QuotaTypes: false, EndpointTypes: true}, display.FilterSections)
	exported := config.GlobalConfig.ExportAllConfigs()
	assert.Equal(t, "false", exported[ModelSquareVersionSpellingFoldOptionKey])
	assert.Equal(t, "true", exported[ModelSquareShowTagFilterOptionKey])
	assert.Equal(t, "true", exported[ModelSquareShowEndpointTypeFilterOptionKey])

	// An invalid stored value falls back to that key's default.
	for _, key := range modelSquareBoolOptionKeys {
		LoadModelSquareOption(key, "yes")
	}
	display = GetModelSquareDisplay()
	assert.True(t, display.VariantGroupingEnabled)
	assert.True(t, display.VersionSpellingFoldEnabled)
	assert.True(t, display.SnapshotFoldEnabled)
	assert.Equal(t, ModelSquareFilterSections{Groups: true, Vendors: true, Tags: false, QuotaTypes: true, EndpointTypes: false}, sections())

	// Each switch changes only its own field.
	LoadModelSquareOption(ModelSquareVariantGroupingEnabledOptionKey, "false")
	display = GetModelSquareDisplay()
	assert.False(t, display.VariantGroupingEnabled)
	assert.True(t, display.VersionSpellingFoldEnabled)
	assert.True(t, display.SnapshotFoldEnabled)
	assert.Equal(t, ModelSquareFilterSections{Groups: true, Vendors: true, Tags: false, QuotaTypes: true, EndpointTypes: false}, sections())
	assert.Equal(t, defaultModelVariantRules, display.VariantRules)
}

func TestIsModelSquareOptionKey(t *testing.T) {
	for _, key := range allModelSquareOptionKeys() {
		assert.True(t, IsModelSquareOptionKey(key), key)
	}
	for _, key := range []string{"model_square_setting.unknown", "model_square_setting", "show_tag_filter", ""} {
		assert.False(t, IsModelSquareOptionKey(key), key)
	}
}
