package model

import (
	"fmt"
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetPricingEndpointTestTables(t *testing.T) {
	t.Helper()
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}, &Model{}, &Vendor{}))
	for _, table := range []string{"abilities", "channels", "models", "vendors"} {
		require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
	}
	InitChannelCache()
	InvalidatePricingCache()
	t.Cleanup(func() {
		for _, table := range []string{"abilities", "channels", "models", "vendors"} {
			require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
		}
		InitChannelCache()
		InvalidatePricingCache()
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
}

func insertPricingEndpointChannel(t *testing.T, channelID int, channelType int, settings dto.ChannelOtherSettings) {
	t.Helper()
	channel := &Channel{
		Id:     channelID,
		Type:   channelType,
		Key:    fmt.Sprintf("key-%d", channelID),
		Status: common.ChannelStatusEnabled,
		Name:   fmt.Sprintf("channel-%d", channelID),
	}
	if settings.AdvancedCustom != nil {
		channel.SetOtherSettings(settings)
	}
	require.NoError(t, DB.Create(channel).Error)
}

func insertPricingEndpointAbility(t *testing.T, channelID int, modelName string) {
	t.Helper()
	require.NoError(t, DB.Create(&Ability{
		Group:     "default",
		Model:     modelName,
		ChannelId: channelID,
		Enabled:   true,
	}).Error)
}

func pricingEndpointAdvancedCustomConfig(routes ...dto.AdvancedCustomRoute) dto.ChannelOtherSettings {
	return dto.ChannelOtherSettings{
		AdvancedCustom: &dto.AdvancedCustomConfig{
			Routes: routes,
		},
	}
}

func pricingEndpointTypesByModel(t *testing.T) map[string][]constant.EndpointType {
	t.Helper()
	InitChannelCache()
	return pricingEndpointTypesFromPricing(GetPricing())
}

func pricingEndpointTypesFromPricing(pricings []Pricing) map[string][]constant.EndpointType {
	byModel := make(map[string][]constant.EndpointType)
	for _, pricing := range pricings {
		byModel[pricing.ModelName] = pricing.SupportedEndpointTypes
	}
	return byModel
}

func TestPricingAdvancedCustomUsesConfiguredEndpointTypes(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 101, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 101, "gemini-2.5-flash")
	insertPricingEndpointAbility(t, 101, "gpt-4o")

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, byModel["gemini-2.5-flash"])
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
	}, byModel["gpt-4o"])
}

func TestPricingModelMetadataEndpointsMergeWithAdvancedCustomInference(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 103, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 103, "gemini-2.5-flash")
	require.NoError(t, DB.Create(&Model{
		ModelName: "gemini-2.5-flash",
		Endpoints: `{
			"openai": "/v1/chat/completions"
		}`,
		Status:   1,
		NameRule: NameRuleExact,
	}).Error)

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeOpenAI,
	}, byModel["gemini-2.5-flash"])
}

func TestPricingModelMetadataEndpointsCanProvideEndpointWithoutChannelInference(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 104, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 104, "metadata-only-model")
	require.NoError(t, DB.Create(&Model{
		ModelName: "metadata-only-model",
		Endpoints: `{
			"openai": "/v1/chat/completions"
		}`,
		Status:   1,
		NameRule: NameRuleExact,
	}).Error)

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["metadata-only-model"])
}

func TestPricingAdvancedCustomMissingConfigFallsBackToChannelType(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 102, constant.ChannelTypeAdvancedCustom, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 102, "gpt-4o")

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["gpt-4o"])
}

func TestPricingNativeChannelEndpointTypesUnchanged(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 201, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	insertPricingEndpointChannel(t, 202, constant.ChannelTypeGemini, dto.ChannelOtherSettings{})
	insertPricingEndpointChannel(t, 203, constant.ChannelTypeAnthropic, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 201, "gpt-4o")
	insertPricingEndpointAbility(t, 202, "gemini-2.5-flash")
	insertPricingEndpointAbility(t, 203, "claude-3-5-sonnet")

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["gpt-4o"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeGemini, constant.EndpointTypeOpenAI}, byModel["gemini-2.5-flash"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI}, byModel["claude-3-5-sonnet"])
}

func TestInitChannelCacheInvalidatesPricingCache(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 301, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
	))
	insertPricingEndpointAbility(t, 301, "gemini-3.5-flash")
	InitChannelCache()

	initial := pricingEndpointTypesByModel(t)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, initial["gemini-3.5-flash"])

	var channel Channel
	require.NoError(t, DB.First(&channel, "id = ?", 301).Error)
	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 301).Update("settings", channel.OtherSettings).Error)
	InitChannelCache()

	updated := pricingEndpointTypesByModel(t)
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, updated["gemini-3.5-flash"])
}

func TestInitChannelCacheInvalidatesStartupPricingBuiltBeforeChannelCache(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 302, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 302, "gemini-3.5-flash")

	staleByModel := pricingEndpointTypesFromPricing(GetPricing())
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, staleByModel["gemini-3.5-flash"])

	InitChannelCache()

	rebuiltByModel := pricingEndpointTypesFromPricing(GetPricing())
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, rebuiltByModel["gemini-3.5-flash"])
}

func TestCacheUpdateChannelSyncsAdvancedCustomConfig(t *testing.T) {
	resetPricingEndpointTestTables(t)

	channel := &Channel{
		Id:     401,
		Type:   constant.ChannelTypeAdvancedCustom,
		Key:    "key-401",
		Status: common.ChannelStatusEnabled,
		Name:   "channel-401",
	}
	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(dto.AdvancedCustomRoute{
		IncomingPath: "/v1/responses",
		UpstreamPath: "/v1beta/models/{model}:generateContent",
		Converter:    "openai_responses_to_gemini_generate_content",
	}))
	CacheUpdateChannel(channel)

	require.NotNil(t, channel2advancedCustomConfig[401])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}, channel2advancedCustomConfig[401].SupportedEndpointTypesForModel("gemini-3.5-flash"))

	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
	}))
	CacheUpdateChannel(channel)

	require.NotNil(t, channel2advancedCustomConfig[401])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, channel2advancedCustomConfig[401].SupportedEndpointTypesForModel("gemini-3.5-flash"))

	channel.Type = constant.ChannelTypeOpenAI
	CacheUpdateChannel(channel)

	assert.Nil(t, channel2advancedCustomConfig[401])
}

func TestPricingListsDuplicateVendorNamesOnce(t *testing.T) {
	resetPricingEndpointTestTables(t)

	// Rows left behind by releases that created vendors while reading pricing.
	for _, vendor := range []Vendor{
		{Id: 3, Name: "Anthropic", Status: 1},
		{Id: 5, Name: "Anthropic", Icon: "Claude.Color", Description: "Claude", Status: 1},
		{Id: 7, Name: " anthropic", Status: 1},
		{Id: 9, Name: "OpenAI", Icon: "OpenAI", Status: 1},
	} {
		require.NoError(t, DB.Create(&vendor).Error)
	}
	insertPricingEndpointChannel(t, 105, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	for _, name := range []string{"claude-saved", "claude-inferred", "gpt-4o"} {
		insertPricingEndpointAbility(t, 105, name)
	}
	require.NoError(t, DB.Create(&Model{ModelName: "claude-saved", VendorID: 7, Status: 1, NameRule: NameRuleExact}).Error)
	InitChannelCache()

	vendorByModel := make(map[string]int)
	for _, pricing := range GetPricing() {
		vendorByModel[pricing.ModelName] = pricing.VendorID
	}

	assert.Equal(t, []PricingVendor{
		{ID: 3, Name: "Anthropic", Icon: "Claude.Color", Description: "Claude"},
		{ID: 9, Name: "OpenAI", Icon: "OpenAI"},
	}, GetVendors())
	assert.Equal(t, map[string]int{"claude-saved": 3, "claude-inferred": 3, "gpt-4o": 9}, vendorByModel)
}

func TestPricingListsNovelAIModelsUnderNovelAI(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 106, constant.ChannelTypeNovelAI, dto.ChannelOtherSettings{})
	for _, name := range []string{"nai-diffusion-5-full", "nai-diffusion-4-5-full", "anime-v4"} {
		insertPricingEndpointAbility(t, 106, name)
	}
	insertPricingEndpointChannel(t, 107, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 107, "gpt-4o")
	InitChannelCache()
	vendorByModel := func() map[string]int {
		vendors := make(map[string]int)
		for _, pricing := range GetPricing() {
			vendors[pricing.ModelName] = pricing.VendorID
		}
		return vendors
	}

	// No vendor is saved: the built-in NovelAI vendor carries its icon, and the
	// alias without a name rule follows its NovelAI channel.
	assert.Equal(t, []PricingVendor{
		{ID: -1012, Name: "OpenAI", Icon: "OpenAI"},
		{ID: -1024, Name: "NovelAI", Icon: "NovelAI"},
	}, GetVendors())
	assert.Equal(t, map[string]int{"nai-diffusion-5-full": -1024, "nai-diffusion-4-5-full": -1024, "anime-v4": -1024, "gpt-4o": -1012}, vendorByModel())

	// A saved vendor of the same name replaces the built-in one.
	require.NoError(t, DB.Create(&Vendor{Id: 4, Name: "novelai", Icon: "NovelAI", Status: 1}).Error)
	InvalidatePricingCache()
	assert.Equal(t, map[string]int{"nai-diffusion-5-full": 4, "nai-diffusion-4-5-full": 4, "anime-v4": 4, "gpt-4o": -1012}, vendorByModel())
	assert.Equal(t, []PricingVendor{
		{ID: 4, Name: "novelai", Icon: "NovelAI"},
		{ID: -1012, Name: "OpenAI", Icon: "OpenAI"},
	}, GetVendors())
}

func TestDefaultVendorMappingUsesSingleVendorChannelsInAnyAbilityOrder(t *testing.T) {
	ability := func(name string, channelType int) AbilityWithChannel {
		return AbilityWithChannel{Ability: Ability{Model: name}, ChannelType: channelType}
	}
	abilities := []AbilityWithChannel{
		ability("anime-v4", constant.ChannelTypeOpenAI),
		ability("anime-v4", constant.ChannelTypeNovelAI),
		ability("nai-diffusion-4-5-full", constant.ChannelTypeOpenAI),
		ability("gpt-4o", constant.ChannelTypeNovelAI),
		ability("saved-alias", constant.ChannelTypeNovelAI),
		ability("tts-1", constant.ChannelTypeNovelAI),
		ability("tts-1-hd", constant.ChannelTypeOpenAI),
	}
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reversed_%t", reversed), func(t *testing.T) {
			ordered := slices.Clone(abilities)
			if reversed {
				slices.Reverse(ordered)
			}
			// A saved record keeps its vendor; one saved without a vendor
			// takes the vendor its name names, never the channel type's.
			metaMap := map[string]*Model{
				"saved-alias": {ModelName: "saved-alias", Status: 1},
				"tts-1":       {ModelName: "tts-1", Status: 1},
				"tts-1-hd":    {ModelName: "tts-1-hd", VendorID: 5, Status: 1},
			}
			initDefaultVendorMapping(metaMap, map[int]*Vendor{}, ordered)

			vendors := make(map[string]int, len(metaMap))
			for name, meta := range metaMap {
				vendors[name] = meta.VendorID
			}
			// Name rules come before the channel type, and name NovelAI models
			// that an OpenAI-compatible channel serves.
			assert.Equal(t, map[string]int{"anime-v4": -1024, "nai-diffusion-4-5-full": -1024, "gpt-4o": -1012, "saved-alias": 0, "tts-1": -1012, "tts-1-hd": 5}, vendors)
		})
	}
}

// resolveModelMetadata shares one record among all the names a prefix,
// suffix or contains record matches, so a vendor inferred for one of them
// goes on that name's own copy and the record itself stays as saved.
func TestDefaultVendorMappingInfersVendorOnACopyOfASharedRecord(t *testing.T) {
	shared := &Model{ModelName: "tts-", Description: "Speech", Status: 1, NameRule: NameRulePrefix}
	metaMap := map[string]*Model{"tts-1-1106": shared, "tts-1-hd": shared, "tts-custom-voice": shared}
	initDefaultVendorMapping(metaMap, map[int]*Vendor{}, []AbilityWithChannel{
		{Ability: Ability{Model: "tts-1-1106"}, ChannelType: constant.ChannelTypeOpenAI},
		{Ability: Ability{Model: "tts-1-hd"}, ChannelType: constant.ChannelTypeOpenAI},
		{Ability: Ability{Model: "tts-1-hd"}, ChannelType: constant.ChannelTypeNovelAI},
		{Ability: Ability{Model: "tts-custom-voice"}, ChannelType: constant.ChannelTypeNovelAI},
	})

	assert.Equal(t, Model{ModelName: "tts-", Description: "Speech", Status: 1, NameRule: NameRulePrefix}, *shared, "the shared record is not changed")
	for _, name := range []string{"tts-1-1106", "tts-1-hd"} {
		require.NotSame(t, shared, metaMap[name], name)
		assert.Equal(t, Model{ModelName: "tts-", Description: "Speech", VendorID: -1012, Status: 1, NameRule: NameRulePrefix}, *metaMap[name], name)
	}
	// No rule names a vendor: the name keeps the record as saved, and a
	// record never takes the vendor of the NovelAI channel serving it.
	assert.Same(t, shared, metaMap["tts-custom-voice"])
}

func TestPricingInfersOpenAIForSpeechAndEmbeddings(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 108, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	for _, name := range []string{"tts-1", "tts-1-1106", "[按次]tts-1", "text-embedding-ada-002"} {
		insertPricingEndpointAbility(t, 108, name)
	}
	insertPricingEndpointChannel(t, 109, constant.ChannelTypeNovelAI, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 109, "gpt-sovits")
	InitChannelCache()
	vendorByModel := func() map[string]int {
		vendors := make(map[string]int)
		for _, pricing := range GetPricing() {
			vendors[pricing.ModelName] = pricing.VendorID
		}
		return vendors
	}

	// gpt-sovits hits the stop row, so it lists under its NovelAI channel.
	assert.Equal(t, map[string]int{"tts-1": -1012, "tts-1-1106": -1012, "[按次]tts-1": -1012, "text-embedding-ada-002": -1012, "gpt-sovits": -1024}, vendorByModel())
	assert.Equal(t, []PricingVendor{
		{ID: -1012, Name: "OpenAI", Icon: "OpenAI"},
		{ID: -1024, Name: "NovelAI", Icon: "NovelAI"},
	}, GetVendors())

	// A record with another vendor keeps it.
	require.NoError(t, DB.Create(&Vendor{Id: 4, Name: "Acme Speech", Status: 1}).Error)
	require.NoError(t, DB.Create(&Model{ModelName: "tts-1", VendorID: 4, Status: 1, NameRule: NameRuleExact}).Error)
	InvalidatePricingCache()
	assert.Equal(t, map[string]int{"tts-1": 4, "tts-1-1106": -1012, "[按次]tts-1": -1012, "text-embedding-ada-002": -1012, "gpt-sovits": -1024}, vendorByModel())
}

// The official metadata sync saves Alibaba's models under "Alibaba"; models
// inferred by name list under that saved vendor instead of a second "阿里巴巴".
func TestPricingListsSyncedCompanyOnce(t *testing.T) {
	resetPricingEndpointTestTables(t)

	require.NoError(t, DB.Create(&Vendor{Id: 7, Name: "Alibaba", Icon: "Qwen.Color", Status: 1}).Error)
	require.NoError(t, DB.Create(&Model{ModelName: "qwen-plus", VendorID: 7, Status: 1, NameRule: NameRuleExact}).Error)
	insertPricingEndpointChannel(t, 111, constant.ChannelTypeAli, dto.ChannelOtherSettings{})
	for _, name := range []string{"qwen-plus", "qwen-plus-latest", "qwen-plus-2025-07-28"} {
		insertPricingEndpointAbility(t, 111, name)
	}
	InitChannelCache()

	vendorByModel := make(map[string]int)
	for _, pricing := range GetPricing() {
		vendorByModel[pricing.ModelName] = pricing.VendorID
	}
	assert.Equal(t, map[string]int{"qwen-plus": 7, "qwen-plus-latest": 7, "qwen-plus-2025-07-28": 7}, vendorByModel)
	assert.Equal(t, []PricingVendor{{ID: 7, Name: "Alibaba", Icon: "Qwen.Color"}}, GetVendors())
}

// A record saved without a vendor (made only to add a description, say)
// takes the vendor its name names; a saved vendor always wins.
func TestPricingInfersVendorOfRecordsWithoutVendor(t *testing.T) {
	resetPricingEndpointTestTables(t)

	require.NoError(t, DB.Create(&Vendor{Id: 4, Name: "Acme Speech", Status: 1}).Error)
	for _, record := range []Model{
		{ModelName: "tts-1", Description: "Text to speech", Status: 1, NameRule: NameRuleExact},
		{ModelName: "tts-", Description: "Speech", Status: 1, NameRule: NameRulePrefix},
		{ModelName: "tts-1-hd", Description: "HD speech", VendorID: 4, Status: 1, NameRule: NameRuleExact},
	} {
		require.NoError(t, DB.Create(&record).Error)
	}
	insertPricingEndpointChannel(t, 110, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	for _, name := range []string{"tts-1", "tts-1-1106", "tts-1-hd", "tts-1-hd-1106", "tts-custom-voice"} {
		insertPricingEndpointAbility(t, 110, name)
	}
	InitChannelCache()

	vendors, descriptions := make(map[string]int), make(map[string]string)
	for _, pricing := range GetPricing() {
		vendors[pricing.ModelName] = pricing.VendorID
		descriptions[pricing.ModelName] = pricing.Description
	}
	// The prefix record "tts-" stands for tts-1-1106, tts-1-hd-1106 and
	// tts-custom-voice; no rule names a vendor for the last one.
	assert.Equal(t, map[string]int{"tts-1": -1012, "tts-1-1106": -1012, "tts-1-hd": 4, "tts-1-hd-1106": -1012, "tts-custom-voice": 0}, vendors)
	assert.Equal(t, map[string]string{"tts-1": "Text to speech", "tts-1-1106": "Speech", "tts-1-hd": "HD speech", "tts-1-hd-1106": "Speech", "tts-custom-voice": "Speech"}, descriptions)
	assert.Equal(t, []PricingVendor{
		{ID: 4, Name: "Acme Speech"},
		{ID: -1012, Name: "OpenAI", Icon: "OpenAI"},
	}, GetVendors())
}
