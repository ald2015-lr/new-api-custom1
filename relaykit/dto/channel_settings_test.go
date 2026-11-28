package dto

import (
	"encoding/json"
	"fmt"
	stdregexp "regexp"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/regexp"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdvancedCustomValidateResponsesToChatConverterPath(t *testing.T) {
	valid := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
			},
		},
	}
	require.NoError(t, valid.Validate())

	validGemini := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
			},
		},
	}
	require.NoError(t, validGemini.Validate())

	validClaude := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/messages",
				Converter:    advancedCustomConverterOpenAIResponsesToClaudeMessages,
			},
		},
	}
	require.NoError(t, validClaude.Validate())
	assert.Equal(t, []types.EndpointType{types.EndpointTypeOpenAIResponse}, validClaude.SupportedEndpointTypesForModel("claude-test"))

	tests := []struct {
		name         string
		incomingPath string
	}{
		{name: "chat completions", incomingPath: "/v1/chat/completions"},
		{name: "responses compact", incomingPath: "/v1/responses/compact"},
		{name: "messages", incomingPath: "/v1/messages"},
	}

	for _, converter := range []string{advancedCustomConverterOpenAIResponsesToOpenAIChat, advancedCustomConverterOpenAIResponsesToClaudeMessages} {
		for _, tt := range tests {
			t.Run(converter+"/"+tt.name, func(t *testing.T) {
				config := &AdvancedCustomConfig{
					Routes: []AdvancedCustomRoute{
						{
							IncomingPath: tt.incomingPath,
							UpstreamPath: "/v1/chat/completions",
							Converter:    converter,
						},
					},
				}
				err := config.Validate()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "converter does not match incoming_path")
			})
		}
	}
}

func TestAdvancedCustomValidateModelListRouteConstraints(t *testing.T) {
	valid := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: AdvancedCustomModelListPath,
				UpstreamPath: "https://upstream.example/custom/models",
				Converter:    advancedCustomConverterNone,
			},
		},
	}
	require.NoError(t, valid.Validate())

	tests := []struct {
		name   string
		routes []AdvancedCustomRoute
		want   string
	}{
		{
			name: "model matching rules",
			routes: []AdvancedCustomRoute{
				{
					IncomingPath: AdvancedCustomModelListPath,
					UpstreamPath: "/v1/models",
					Models:       []string{"gpt-4o"},
				},
			},
			want: "models must be empty",
		},
		{
			name: "converter",
			routes: []AdvancedCustomRoute{
				{
					IncomingPath: AdvancedCustomModelListPath,
					UpstreamPath: "/v1/models",
					Converter:    advancedCustomConverterOpenAIChatToOpenAIResponses,
				},
			},
			want: "converter must be none",
		},
		{
			name: "model placeholder",
			routes: []AdvancedCustomRoute{
				{
					IncomingPath: AdvancedCustomModelListPath,
					UpstreamPath: "/v1/models/{model}",
				},
			},
			want: "upstream_path must not contain {model}",
		},
		{
			name: "duplicate routes",
			routes: []AdvancedCustomRoute{
				{IncomingPath: AdvancedCustomModelListPath, UpstreamPath: "/v1/models"},
				{IncomingPath: AdvancedCustomModelListPath, UpstreamPath: "/provider/models"},
			},
			want: "duplicates the /v1/models route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&AdvancedCustomConfig{Routes: tt.routes}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestAdvancedCustomModelListRouteRequiresExactIncomingPath(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/{model}",
				UpstreamPath: "/generic/{model}",
			},
			{
				IncomingPath: AdvancedCustomModelListPath,
				UpstreamPath: "/provider/models",
			},
		},
	}
	require.NoError(t, config.Validate())

	route, ok := config.ModelListRoute()
	require.True(t, ok)
	assert.Equal(t, "/provider/models", route.UpstreamPath)
}

func TestAdvancedCustomValidateBalanceRouteConstraints(t *testing.T) {
	valid := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{{
			IncomingPath: AdvancedCustomBalancePath,
			UpstreamPath: "/provider/balance",
			Converter:    advancedCustomConverterNone,
		}},
	}
	require.NoError(t, valid.Validate())

	route, ok := valid.BalanceRoute()
	require.True(t, ok)
	assert.Equal(t, "/provider/balance", route.UpstreamPath)

	tests := []struct {
		name   string
		routes []AdvancedCustomRoute
		want   string
	}{
		{
			name: "model matching rules",
			routes: []AdvancedCustomRoute{{
				IncomingPath: AdvancedCustomBalancePath,
				UpstreamPath: "/provider/balance",
				Models:       []string{"gpt-4o"},
			}},
			want: "models must be empty",
		},
		{
			name: "converter",
			routes: []AdvancedCustomRoute{{
				IncomingPath: AdvancedCustomBalancePath,
				UpstreamPath: "/provider/balance",
				Converter:    advancedCustomConverterOpenAIChatToOpenAIResponses,
			}},
			want: "converter must be none",
		},
		{
			name: "model placeholder",
			routes: []AdvancedCustomRoute{{
				IncomingPath: AdvancedCustomBalancePath,
				UpstreamPath: "/provider/{model}/balance",
			}},
			want: "upstream_path must not contain {model}",
		},
		{
			name: "duplicate routes",
			routes: []AdvancedCustomRoute{
				{IncomingPath: AdvancedCustomBalancePath, UpstreamPath: "/provider/balance"},
				{IncomingPath: AdvancedCustomBalancePath, UpstreamPath: "/provider/credits"},
			},
			want: "duplicates the /v1/dashboard/billing/credit_grants route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&AdvancedCustomConfig{Routes: tt.routes}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestAdvancedCustomValidateDuplicateIncomingPathWithDisjointModels(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
				Models:       []string{"gpt-4o"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"gemini-2.5-flash"},
			},
		},
	}

	require.NoError(t, config.Validate())
}

func TestAdvancedCustomValidateDuplicateIncomingPathRejectsOverlappingModels(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
				Models:       []string{"shared-model"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"shared-model"},
			},
		},
	}

	err := config.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "models overlaps")
}

func TestAdvancedCustomValidateDuplicateIncomingPathRejectsMultipleCatchAllRoutes(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
			},
		},
	}

	err := config.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catch-all already exists")
}

func TestAdvancedCustomValidateDuplicateIncomingPathRequiresCatchAllLast(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"gemini-2.5-flash"},
			},
		},
	}

	err := config.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catch-all route must be last")
}

func TestAdvancedCustomMatchPathForModel(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"gemini-2.5-flash"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
				Models:       []string{"gpt-4o"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/responses",
				Converter:    advancedCustomConverterNone,
			},
		},
	}
	require.NoError(t, config.Validate())

	geminiRoute, ok := config.MatchPathForModel("/v1/responses", "gemini-2.5-flash")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterOpenAIResponsesToGemini, geminiRoute.Converter)

	chatRoute, ok := config.MatchPathForModel("/v1/responses", "gpt-4o")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterOpenAIResponsesToOpenAIChat, chatRoute.Converter)

	fallbackRoute, ok := config.MatchPathForModel("/v1/responses", "unknown-model")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterNone, fallbackRoute.Converter)
}

func TestAdvancedCustomMatchPathForModelRegexRules(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"re:^gemini-"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
				Models:       []string{"re:(?i)^OAI-"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/responses",
				Converter:    advancedCustomConverterNone,
			},
		},
	}
	require.NoError(t, config.Validate())

	geminiRoute, ok := config.MatchPathForModel("/v1/responses", "gemini-2.5-flash")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterOpenAIResponsesToGemini, geminiRoute.Converter)

	chatRoute, ok := config.MatchPathForModel("/v1/responses", "oai-test")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterOpenAIResponsesToOpenAIChat, chatRoute.Converter)

	fallbackRoute, ok := config.MatchPathForModel("/v1/responses", "gpt-4o")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterNone, fallbackRoute.Converter)
}

func TestAdvancedCustomRouteModelRegexRulesAreCachedCompiled(t *testing.T) {
	require.True(t, matchAdvancedCustomRouteModelRule("re:^cache-probe-", "cache-probe-model"))

	cached, ok := advancedCustomModelRegexCache.Load("^cache-probe-")
	require.True(t, ok)
	require.NotNil(t, cached)
	_, isRegexp := cached.(*regexp.Regexp)
	require.True(t, isRegexp)

	// Invalid patterns never match and are cached as nil so they are not recompiled.
	require.False(t, matchAdvancedCustomRouteModelRule("re:(", "anything"))
	cached, ok = advancedCustomModelRegexCache.Load("(")
	require.True(t, ok)
	re, _ := cached.(*regexp.Regexp)
	require.Nil(t, re)

	// Cached entries keep matching correctly on subsequent calls.
	require.True(t, matchAdvancedCustomRouteModelRule("re:^cache-probe-", "cache-probe-other"))
	require.False(t, matchAdvancedCustomRouteModelRule("re:^cache-probe-", "other-model"))
}

func TestAdvancedCustomMatchPathForModelExactRuleDoesNotMatchPrefix(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"gemini"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/responses",
				Converter:    advancedCustomConverterNone,
			},
		},
	}
	require.NoError(t, config.Validate())

	fallbackRoute, ok := config.MatchPathForModel("/v1/responses", "gemini-2.5-flash")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterNone, fallbackRoute.Converter)
}

func TestAdvancedCustomValidateDuplicateIncomingPathRejectsInvalidRegexModels(t *testing.T) {
	tests := []struct {
		name   string
		models []string
		want   string
	}{
		{name: "empty regex", models: []string{"re:"}, want: "regex is empty"},
		{name: "invalid regex", models: []string{"re:["}, want: "regex is invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AdvancedCustomConfig{
				Routes: []AdvancedCustomRoute{
					{
						IncomingPath: "/v1/responses",
						UpstreamPath: "/v1beta/models/{model}:generateContent",
						Converter:    advancedCustomConverterOpenAIResponsesToGemini,
						Models:       tt.models,
					},
				},
			}

			err := config.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestAdvancedCustomValidateDuplicateIncomingPathRejectsDuplicateRegexModels(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"re:^gemini-"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
				Models:       []string{"re:^gemini-"},
			},
		},
	}

	err := config.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "models overlaps")
}

func TestAdvancedCustomMatchPathForModelUsesFirstMatchingRegexRoute(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"re:^gemini-"},
			},
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterOpenAIResponsesToOpenAIChat,
				Models:       []string{"gemini-2.5-flash"},
			},
		},
	}
	require.NoError(t, config.Validate())

	route, ok := config.MatchPathForModel("/v1/responses", "gemini-2.5-flash")
	require.True(t, ok)
	assert.Equal(t, advancedCustomConverterOpenAIResponsesToGemini, route.Converter)
}

func TestAdvancedCustomSupportedEndpointTypesForModel(t *testing.T) {
	config := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/responses",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Converter:    advancedCustomConverterOpenAIResponsesToGemini,
				Models:       []string{"re:^gemini-"},
			},
			{
				IncomingPath: "/v1beta/models/{model}:generateContent",
				UpstreamPath: "/v1beta/models/{model}:generateContent",
				Models:       []string{"re:^gemini-"},
			},
			{
				IncomingPath: "/v1beta/models/{model}:streamGenerateContent",
				UpstreamPath: "/v1beta/models/{model}:streamGenerateContent",
				Models:       []string{"re:^gemini-"},
			},
			{
				IncomingPath: "/v1/chat/completions",
				UpstreamPath: "/v1/chat/completions",
				Models:       []string{"gpt-4o"},
			},
			{
				IncomingPath: "/v1/messages",
				UpstreamPath: "/v1/messages",
			},
			{
				IncomingPath: "/custom/endpoint",
				UpstreamPath: "/custom/endpoint",
			},
		},
	}
	require.NoError(t, config.Validate())

	assert.Equal(t, []types.EndpointType{
		types.EndpointTypeOpenAIResponse,
		types.EndpointTypeGemini,
		types.EndpointTypeAnthropic,
	}, config.SupportedEndpointTypesForModel("gemini-2.5-flash"))
	assert.Equal(t, []types.EndpointType{
		types.EndpointTypeOpenAI,
		types.EndpointTypeAnthropic,
	}, config.SupportedEndpointTypesForModel("gpt-4o"))
	assert.Equal(t, []types.EndpointType{
		types.EndpointTypeAnthropic,
	}, config.SupportedEndpointTypesForModel("other-model"))
}

func TestAdvancedCustomValidateAlphaSearchConverterPath(t *testing.T) {
	valid := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath: "/v1/alpha/search",
				UpstreamPath: "/v1/alpha/search",
				Converter:    advancedCustomConverterNone,
			},
		},
	}
	require.NoError(t, valid.Validate())
	assert.Equal(t, []types.EndpointType{
		types.EndpointTypeOpenAIAlphaSearch,
	}, valid.SupportedEndpointTypesForModel("gpt-5.1"))

	nonNoneConverters := []string{
		advancedCustomConverterClaudeMessagesToOpenAIChat,
		advancedCustomConverterOpenAIChatToClaudeMessages,
		advancedCustomConverterOpenAIChatToOpenAIResponses,
		advancedCustomConverterOpenAIResponsesToOpenAIChat,
		advancedCustomConverterOpenAIResponsesToGemini,
		advancedCustomConverterOpenAIResponsesToClaudeMessages,
		advancedCustomConverterGeminiContentToOpenAIChat,
		advancedCustomConverterOpenAIChatToGeminiContent,
	}
	for _, converter := range nonNoneConverters {
		t.Run(converter, func(t *testing.T) {
			config := &AdvancedCustomConfig{
				Routes: []AdvancedCustomRoute{
					{
						IncomingPath: "/v1/alpha/search",
						UpstreamPath: "/v1/alpha/search",
						Converter:    converter,
					},
				},
			}
			err := config.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "converter does not match incoming_path")
		})
	}
}

func TestAdvancedCustomValidateRoutePassThroughBody(t *testing.T) {
	valid := &AdvancedCustomConfig{
		Routes: []AdvancedCustomRoute{
			{
				IncomingPath:           "/v1/chat/completions",
				UpstreamPath:           "/v1/chat/completions",
				PassThroughBodyEnabled: true,
			},
			{
				IncomingPath:           "/v1/rerank",
				UpstreamPath:           "/v1/rerank",
				Converter:              AdvancedCustomConverterSGLangRerank,
				PassThroughBodyEnabled: true,
			},
			{
				IncomingPath: "/v1/messages",
				UpstreamPath: "/v1/chat/completions",
				Converter:    advancedCustomConverterClaudeMessagesToOpenAIChat,
			},
		},
	}
	require.NoError(t, valid.Validate())

	route, ok := valid.MatchPathForModel("/v1/chat/completions", "gpt-4o")
	require.True(t, ok)
	assert.True(t, route.PassThroughBodyEnabled)
	route, ok = valid.MatchPathForModel("/v1/messages", "gpt-4o")
	require.True(t, ok)
	assert.False(t, route.PassThroughBodyEnabled)

	encoded, err := json.Marshal(valid.Routes[2])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "pass_through_body_enabled")

	tests := []struct {
		name  string
		route AdvancedCustomRoute
		want  string
	}{
		{
			name: "converter route",
			route: AdvancedCustomRoute{
				IncomingPath:           "/v1/messages",
				UpstreamPath:           "/v1/chat/completions",
				Converter:              advancedCustomConverterClaudeMessagesToOpenAIChat,
				PassThroughBodyEnabled: true,
			},
			want: "pass_through_body_enabled requires converter none",
		},
		{
			name: "model list route",
			route: AdvancedCustomRoute{
				IncomingPath:           AdvancedCustomModelListPath,
				UpstreamPath:           "/v1/models",
				PassThroughBodyEnabled: true,
			},
			want: "pass_through_body_enabled must be false for /v1/models",
		},
		{
			name: "balance route",
			route: AdvancedCustomRoute{
				IncomingPath:           AdvancedCustomBalancePath,
				UpstreamPath:           "/provider/balance",
				PassThroughBodyEnabled: true,
			},
			want: "pass_through_body_enabled must be false for /v1/dashboard/billing/credit_grants",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&AdvancedCustomConfig{Routes: []AdvancedCustomRoute{tt.route}}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestChannelSettingsHTTPTransportJSONRoundTrip(t *testing.T) {
	legacy := `{"proxy":"http://127.0.0.1:8080","force_format":true}`
	var settings ChannelSettings
	require.NoError(t, json.Unmarshal([]byte(legacy), &settings))
	assert.Equal(t, "http://127.0.0.1:8080", settings.Proxy)
	assert.True(t, settings.ForceFormat)
	assert.Empty(t, settings.HTTPProtocol)
	assert.Zero(t, settings.HTTP2ConnectionShards)

	encoded, err := json.Marshal(settings)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "http_protocol")
	assert.NotContains(t, string(encoded), "http2_connection_shards")
	assert.NotContains(t, string(encoded), "tls_insecure_skip_verify")

	explicit := ChannelSettings{
		Proxy:                 "socks5://127.0.0.1:1080",
		HTTPProtocol:          HTTPProtocolHTTP1,
		HTTP2ConnectionShards: 1,
		TLSInsecureSkipVerify: true,
	}
	encoded, err = json.Marshal(explicit)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"http_protocol":"http1"`)
	assert.Contains(t, string(encoded), `"tls_insecure_skip_verify":true`)

	var decoded ChannelSettings
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, explicit.HTTPProtocol, decoded.HTTPProtocol)
	assert.Equal(t, 1, decoded.HTTP2ConnectionShards)
	assert.True(t, decoded.TLSInsecureSkipVerify)

	sharded := ChannelSettings{HTTP2ConnectionShards: 4}
	encoded, err = json.Marshal(sharded)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"http2_connection_shards":4`)
	assert.NotContains(t, string(encoded), "http_protocol")
}

func TestChannelSettingsValidateHTTPTransport(t *testing.T) {
	require.NoError(t, (&ChannelSettings{}).ValidateHTTPTransport())
	require.NoError(t, (&ChannelSettings{HTTPProtocol: "AUTO"}).ValidateHTTPTransport())
	require.NoError(t, (&ChannelSettings{HTTPProtocol: "http1"}).ValidateHTTPTransport())
	require.NoError(t, (&ChannelSettings{HTTP2ConnectionShards: 8}).ValidateHTTPTransport())

	err := (&ChannelSettings{HTTPProtocol: "http2"}).ValidateHTTPTransport()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http_protocol")

	err = (&ChannelSettings{HTTP2ConnectionShards: -1}).ValidateHTTPTransport()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http2_connection_shards")

	err = (&ChannelSettings{HTTP2ConnectionShards: 9}).ValidateHTTPTransport()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http2_connection_shards")

	err = (&ChannelSettings{HTTPProtocol: "http1", HTTP2ConnectionShards: 2}).ValidateHTTPTransport()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http2_connection_shards")
}

func TestChannelOtherSettingsValidateToolLossPolicy(t *testing.T) {
	require.NoError(t, (*ChannelOtherSettings)(nil).ValidateToolLossPolicy())
	require.NoError(t, (&ChannelOtherSettings{}).ValidateToolLossPolicy())
	require.NoError(t, (&ChannelOtherSettings{ToolLossPolicy: "allow"}).ValidateToolLossPolicy())
	require.NoError(t, (&ChannelOtherSettings{ToolLossPolicy: "safe"}).ValidateToolLossPolicy())
	require.NoError(t, (&ChannelOtherSettings{ToolLossPolicy: "strict"}).ValidateToolLossPolicy())

	err := (&ChannelOtherSettings{ToolLossPolicy: "drop"}).ValidateToolLossPolicy()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool_loss_policy")
}

// A NovelAI channel's queue limit per key is stored as 0 to 100 or not at
// all, and a stored value outside that range still acts within it: unset or
// negative is the default of 10, above 100 is 100.
func TestChannelOtherSettingsNovelAIQueueLimit(t *testing.T) {
	queueLimit := func(value int) *int { return &value }
	for _, tc := range []struct {
		name      string
		limit     *int
		valid     bool
		effective int
	}{
		{"unset", nil, true, 10},
		{"no waiting", queueLimit(0), true, 0},
		{"some waiting", queueLimit(7), true, 7},
		{"the maximum", queueLimit(100), true, 100},
		{"negative", queueLimit(-1), false, 10},
		{"far negative", queueLimit(-3), false, 10},
		{"above the maximum", queueLimit(101), false, 100},
		{"far above the maximum", queueLimit(5000), false, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &ChannelOtherSettings{NovelAIQueueLimit: tc.limit}
			if tc.valid {
				assert.NoError(t, settings.ValidateNovelAIQueueLimit())
			} else {
				assert.EqualError(t, settings.ValidateNovelAIQueueLimit(), "novelai_queue_limit must be between 0 and 100")
			}
			assert.Equal(t, tc.effective, settings.EffectiveNovelAIQueueLimit())
		})
	}
	assert.NoError(t, (*ChannelOtherSettings)(nil).ValidateNovelAIQueueLimit())
	assert.Equal(t, 10, (*ChannelOtherSettings)(nil).EffectiveNovelAIQueueLimit())
}

// A channel that hides upstream balance errors answers with its own message
// and status: blank or unset are the defaults, a stored value outside the
// allowed range still acts within it, and saving rejects a message over 500
// characters once trimmed (Chinese counts per character) or a status outside
// 400 to 599.
func TestChannelOtherSettingsUpstreamQuotaError(t *testing.T) {
	long := strings.Repeat("额", UpstreamQuotaErrorMessageMaxRunes)
	for _, tc := range []struct {
		name             string
		settings         ChannelOtherSettings
		wantErr          string
		effectiveMessage string
		effectiveStatus  int
	}{
		{name: "unset", effectiveMessage: "上游tokens数量不足", effectiveStatus: 403},
		{name: "blank message", settings: ChannelOtherSettings{UpstreamQuotaErrorMessage: " \t\n"}, effectiveMessage: "上游tokens数量不足", effectiveStatus: 403},
		{name: "custom Chinese message is trimmed", settings: ChannelOtherSettings{UpstreamQuotaErrorMessage: "  服务繁忙，请稍后再试  "}, effectiveMessage: "服务繁忙，请稍后再试", effectiveStatus: 403},
		{name: "500 characters", settings: ChannelOtherSettings{UpstreamQuotaErrorMessage: " " + long + " "}, effectiveMessage: long, effectiveStatus: 403},
		{name: "501 characters", settings: ChannelOtherSettings{UpstreamQuotaErrorMessage: long + "x"}, wantErr: "upstream_quota_error_message must be at most 500 characters", effectiveMessage: long, effectiveStatus: 403},
		{name: "lowest status", settings: ChannelOtherSettings{UpstreamQuotaErrorStatusCode: 400}, effectiveMessage: "上游tokens数量不足", effectiveStatus: 400},
		{name: "custom status", settings: ChannelOtherSettings{UpstreamQuotaErrorStatusCode: 429}, effectiveMessage: "上游tokens数量不足", effectiveStatus: 429},
		{name: "highest status", settings: ChannelOtherSettings{UpstreamQuotaErrorStatusCode: 599}, effectiveMessage: "上游tokens数量不足", effectiveStatus: 599},
		{name: "status below range", settings: ChannelOtherSettings{UpstreamQuotaErrorStatusCode: 399}, wantErr: "upstream_quota_error_status_code must be between 400 and 599", effectiveMessage: "上游tokens数量不足", effectiveStatus: 403},
		{name: "status above range", settings: ChannelOtherSettings{UpstreamQuotaErrorStatusCode: 600}, wantErr: "upstream_quota_error_status_code must be between 400 and 599", effectiveMessage: "上游tokens数量不足", effectiveStatus: 403},
		{name: "negative status", settings: ChannelOtherSettings{UpstreamQuotaErrorStatusCode: -403}, wantErr: "upstream_quota_error_status_code must be between 400 and 599", effectiveMessage: "上游tokens数量不足", effectiveStatus: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErr == "" {
				assert.NoError(t, tc.settings.ValidateUpstreamQuotaError())
			} else {
				assert.EqualError(t, tc.settings.ValidateUpstreamQuotaError(), tc.wantErr)
			}
			assert.Equal(t, tc.effectiveMessage, tc.settings.EffectiveUpstreamQuotaErrorMessage())
			assert.Equal(t, tc.effectiveStatus, tc.settings.EffectiveUpstreamQuotaErrorStatusCode())
		})
	}
	var unset *ChannelOtherSettings
	assert.NoError(t, unset.ValidateUpstreamQuotaError())
	assert.Equal(t, "上游tokens数量不足", unset.EffectiveUpstreamQuotaErrorMessage())
	assert.Equal(t, 403, unset.EffectiveUpstreamQuotaErrorStatusCode())

	// The switch is off unless saved on, and only saved values are written.
	var decoded ChannelOtherSettings
	require.NoError(t, json.Unmarshal([]byte(`{"novelai_hd_disabled":true}`), &decoded))
	assert.False(t, decoded.UpstreamQuotaErrorMaskEnabled)
	encoded, err := json.Marshal(ChannelOtherSettings{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))
	encoded, err = json.Marshal(ChannelOtherSettings{UpstreamQuotaErrorMaskEnabled: true, UpstreamQuotaErrorMessage: "余额维护中", UpstreamQuotaErrorStatusCode: 429})
	require.NoError(t, err)
	assert.JSONEq(t, `{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_message":"余额维护中","upstream_quota_error_status_code":429}`, string(encoded))
}

// A Gemini channel's free key mode is off unless saved on and writes only
// what it saves; its settings default to countTokens, 3 attempts and the
// built-in limits per model, and saving rejects values outside their range,
// naming the field.
func TestChannelOtherSettingsGeminiFreeKey(t *testing.T) {
	var unset *ChannelOtherSettings
	assert.NoError(t, unset.ValidateGeminiFreeKey())
	assert.Equal(t, "count_tokens", unset.EffectiveGeminiFreeKeyTokenCounter())
	assert.Equal(t, 3, unset.EffectiveGeminiFreeKeyMaxAttempts())
	assert.Equal(t, DefaultGeminiFreeKeyModelLimits(), unset.EffectiveGeminiFreeKeyModelLimits())
	assert.Equal(t, DefaultGeminiFreeKeyModelLimits(), (&ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{}}).EffectiveGeminiFreeKeyModelLimits(), "an empty list is not stored")

	defaults := DefaultGeminiFreeKeyModelLimits()
	require.NoError(t, (&ChannelOtherSettings{GeminiFreeKeyModelLimits: defaults}).ValidateGeminiFreeKey(), "the defaults are valid")
	assert.Equal(t, GeminiFreeKeyModelLimit{Model: "gemini-3.6-flash", Aliases: []string{"gemini-3.5-flash"}, RPM: 5, TPM: 250000, RPD: 20}, defaults[1])
	assert.Equal(t, GeminiFreeKeyModelLimit{Model: "gemma-4-26b-a4b-it", Aliases: []string{"gemma-4-26b"}, RPM: 30, TPM: 16000, RPD: 14400}, defaults[9])
	assert.Equal(t, GeminiFreeKeyModelLimit{Model: "gemini-2.5-pro", RPM: 0, TPM: 0, RPD: 0}, defaults[28])
	defaults[0].RPM = 99
	assert.Equal(t, 5, DefaultGeminiFreeKeyModelLimits()[0].RPM, "each call returns a new table")

	row := func(model string, aliases ...string) GeminiFreeKeyModelLimit {
		return GeminiFreeKeyModelLimit{Model: model, Aliases: aliases, RPM: 1, TPM: 1, RPD: 1}
	}
	tooManyRows := make([]GeminiFreeKeyModelLimit, 301)
	for i := range tooManyRows {
		tooManyRows[i] = row(fmt.Sprintf("m-%d", i))
	}
	tooManyAliases := make([]string, 21)
	for i := range tooManyAliases {
		tooManyAliases[i] = fmt.Sprintf("a-%d", i)
	}
	for _, tc := range []struct {
		name     string
		settings ChannelOtherSettings
		wantErr  string
	}{
		{name: "empty"},
		{name: "local counter", settings: ChannelOtherSettings{GeminiFreeKeyTokenCounter: "local"}},
		{name: "countTokens counter", settings: ChannelOtherSettings{GeminiFreeKeyTokenCounter: "count_tokens"}},
		{name: "unknown counter", settings: ChannelOtherSettings{GeminiFreeKeyTokenCounter: "tiktoken"}, wantErr: "gemini_free_key_token_counter must be count_tokens or local"},
		{name: "1 attempt", settings: ChannelOtherSettings{GeminiFreeKeyMaxAttempts: 1}},
		{name: "10 attempts", settings: ChannelOtherSettings{GeminiFreeKeyMaxAttempts: 10}},
		{name: "11 attempts", settings: ChannelOtherSettings{GeminiFreeKeyMaxAttempts: 11}, wantErr: "gemini_free_key_max_attempts must be between 1 and 10"},
		{name: "negative attempts", settings: ChannelOtherSettings{GeminiFreeKeyMaxAttempts: -1}, wantErr: "gemini_free_key_max_attempts must be between 1 and 10"},
		{name: "300 rows", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: tooManyRows[:300]}},
		{name: "301 rows", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: tooManyRows}, wantErr: "gemini_free_key_model_limits must have at most 300 models"},
		{name: "empty model", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a"), row("")}}, wantErr: "gemini_free_key_model_limits[1].model must not be empty"},
		{name: "untrimmed model", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row(" a")}}, wantErr: "gemini_free_key_model_limits[0].model must not start or end with spaces"},
		{name: "long model", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row(strings.Repeat("模", 129))}}, wantErr: "gemini_free_key_model_limits[0].model must be at most 128 characters"},
		{name: "128 characters", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row(strings.Repeat("模", 128))}}},
		{name: "20 aliases", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a", tooManyAliases[:20]...)}}},
		{name: "21 aliases", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a", tooManyAliases...)}}, wantErr: "gemini_free_key_model_limits[0].aliases must have at most 20 names"},
		{name: "empty alias", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a", "b", "")}}, wantErr: "gemini_free_key_model_limits[0].aliases[1] must not be empty"},
		{name: "duplicate model ignoring case", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("Gemini-X"), row("gemini-x")}}, wantErr: `gemini_free_key_model_limits[1].model "gemini-x" is listed more than once in gemini_free_key_model_limits`},
		{name: "alias of another row", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a"), row("b", "A")}}, wantErr: `gemini_free_key_model_limits[1].aliases[0] "A" is listed more than once in gemini_free_key_model_limits`},
		{name: "unlimited and zero", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{{Model: "a", RPM: -1, TPM: 0, RPD: 1_000_000_000}}}},
		{name: "rpm below -1", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{{Model: "a", RPM: -2}}}, wantErr: "gemini_free_key_model_limits[0].rpm must be between -1 and 1000000000"},
		{name: "tpm above the maximum", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{{Model: "a", TPM: 1_000_000_001}}}, wantErr: "gemini_free_key_model_limits[0].tpm must be between -1 and 1000000000"},
		{name: "rpd below -1", settings: ChannelOtherSettings{GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a"), {Model: "b", RPD: -5}}}, wantErr: "gemini_free_key_model_limits[1].rpd must be between -1 and 1000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErr == "" {
				assert.NoError(t, tc.settings.ValidateGeminiFreeKey())
			} else {
				assert.EqualError(t, tc.settings.ValidateGeminiFreeKey(), tc.wantErr)
			}
		})
	}

	custom := ChannelOtherSettings{GeminiFreeKeyTokenCounter: "local", GeminiFreeKeyMaxAttempts: 7, GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{row("a")}}
	assert.Equal(t, "local", custom.EffectiveGeminiFreeKeyTokenCounter())
	assert.Equal(t, 7, custom.EffectiveGeminiFreeKeyMaxAttempts())
	assert.Equal(t, []GeminiFreeKeyModelLimit{row("a")}, custom.EffectiveGeminiFreeKeyModelLimits())
	assert.Equal(t, 10, (&ChannelOtherSettings{GeminiFreeKeyMaxAttempts: 50}).EffectiveGeminiFreeKeyMaxAttempts())

	// Only saved values are written, and a 0 limit is a value.
	encoded, err := json.Marshal(ChannelOtherSettings{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "gemini_free_key")
	encoded, err = json.Marshal(ChannelOtherSettings{
		GeminiFreeKeyEnabled:     true,
		GeminiFreeKeyModelLimits: []GeminiFreeKeyModelLimit{{Model: "gemini-2.5-pro", RPM: 0, TPM: 0, RPD: 0}, {Model: "gemini-x", Aliases: []string{"x"}, RPM: -1, TPM: 5, RPD: 1}},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"gemini_free_key_enabled":true,"gemini_free_key_model_limits":[{"model":"gemini-2.5-pro","rpm":0,"tpm":0,"rpd":0},{"model":"gemini-x","aliases":["x"],"rpm":-1,"tpm":5,"rpd":1}]}`, string(encoded))
	var decoded ChannelOtherSettings
	require.NoError(t, json.Unmarshal([]byte(`{"gemini_free_key_enabled":true,"gemini_free_key_reveal_tier_errors":true,"gemini_free_key_token_counter":"local","gemini_free_key_max_attempts":5}`), &decoded))
	assert.Equal(t, ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyRevealTierErrors: true, GeminiFreeKeyTokenCounter: "local", GeminiFreeKeyMaxAttempts: 5}, decoded)
}

// Moderation settings are off and write nothing unless saved; their effective
// values fall back to the defaults, and saving rejects every malformed value
// with a message that names the field.
func TestChannelOtherSettingsModeration(t *testing.T) {
	normalised := func(text string) string {
		return fmt.Sprintf("moderation_rules[0].pattern contains %q, which content moderation normalises before matching (write the half-width form, e.g. ! ? , ( ) instead of ！？，（）, plain ' \" - instead of curly quotes and dashes, a single space instead of tabs or full-width spaces, and no space between Chinese characters)", text)
	}
	var unset *ChannelOtherSettings
	assert.NoError(t, unset.ValidateModeration())
	assert.Equal(t, 10000, unset.EffectiveModerationMaxChars())
	assert.Equal(t, 10*time.Second, unset.EffectiveModerationTimeout())
	assert.Nil(t, unset.EffectiveModerationRulePresets())
	for _, tc := range []struct {
		settings ChannelOtherSettings
		maxChars int
		timeout  time.Duration
	}{
		{ChannelOtherSettings{}, 10000, 10 * time.Second},
		{ChannelOtherSettings{ModerationMaxChars: 1000, ModerationTimeoutSeconds: 1}, 1000, time.Second},
		{ChannelOtherSettings{ModerationMaxChars: 200000, ModerationTimeoutSeconds: 60}, 200000, 60 * time.Second},
		{ChannelOtherSettings{ModerationMaxChars: 999, ModerationTimeoutSeconds: 61}, 10000, 10 * time.Second},
		{ChannelOtherSettings{ModerationMaxChars: -1, ModerationTimeoutSeconds: -1}, 10000, 10 * time.Second},
	} {
		assert.Equal(t, tc.maxChars, tc.settings.EffectiveModerationMaxChars())
		assert.Equal(t, tc.timeout, tc.settings.EffectiveModerationTimeout())
	}
	empty := []string{}
	listed := []string{"zh-reveal-cot", "en-reveal-cot"}
	assert.Nil(t, (&ChannelOtherSettings{ModerationRulePresets: &empty}).EffectiveModerationRulePresets())
	assert.Equal(t, listed, (&ChannelOtherSettings{ModerationRulePresets: &listed}).EffectiveModerationRulePresets())

	presets := func(ids ...string) *[]string { return &ids }
	keyword := func(pattern string) ModerationRule {
		return ModerationRule{Type: ModerationRuleTypeKeyword, Pattern: pattern}
	}
	regex := func(pattern string) ModerationRule {
		return ModerationRule{Type: ModerationRuleTypeRegex, Pattern: pattern}
	}
	rules := func(n int, rule func(int) ModerationRule) []ModerationRule {
		out := make([]ModerationRule, 0, n)
		for i := range n {
			out = append(out, rule(i))
		}
		return out
	}
	heavy := "a" + strings.Repeat(".{0,999}a", 50) + ".{0,999}q"
	longError := "(" + strings.Repeat("字", 300)
	for _, tc := range []struct {
		name     string
		settings ChannelOtherSettings
		wantErr  string
	}{
		{name: "empty"},
		{name: "bounds", settings: ChannelOtherSettings{ModerationMaxChars: 1000, ModerationTimeoutSeconds: 60}},
		{name: "upper bounds", settings: ChannelOtherSettings{ModerationMaxChars: 200000, ModerationTimeoutSeconds: 1}},
		{name: "max chars below", settings: ChannelOtherSettings{ModerationMaxChars: 999}, wantErr: "moderation_max_chars must be 0 or between 1000 and 200000"},
		{name: "max chars above", settings: ChannelOtherSettings{ModerationMaxChars: 200001}, wantErr: "moderation_max_chars must be 0 or between 1000 and 200000"},
		{name: "max chars negative", settings: ChannelOtherSettings{ModerationMaxChars: -1}, wantErr: "moderation_max_chars must be 0 or between 1000 and 200000"},
		{name: "timeout above", settings: ChannelOtherSettings{ModerationTimeoutSeconds: 61}, wantErr: "moderation_timeout_seconds must be 0 or between 1 and 60"},
		{name: "timeout negative", settings: ChannelOtherSettings{ModerationTimeoutSeconds: -1}, wantErr: "moderation_timeout_seconds must be 0 or between 1 and 60"},
		{name: "categories", settings: ChannelOtherSettings{ModerationCategories: []string{"sexual", "violence/graphic"}}},
		{name: "unknown category", settings: ChannelOtherSettings{ModerationCategories: []string{"sexual", "nsfw"}}, wantErr: `moderation_categories[1] "nsfw" is not an OpenAI moderation category`},
		{name: "repeated category", settings: ChannelOtherSettings{ModerationCategories: []string{"hate", "sexual", "hate"}}, wantErr: `moderation_categories[2] "hate" is listed more than once`},
		{name: "explicit empty presets", settings: ChannelOtherSettings{ModerationRulePresets: presets()}},
		{name: "unknown preset", settings: ChannelOtherSettings{ModerationRulePresets: presets("en-reveal-cot", "gone")}, wantErr: `moderation_rule_presets[1] "gone" is not a built-in preset`},
		{name: "repeated preset", settings: ChannelOtherSettings{ModerationRulePresets: presets("en-reveal-cot", "en-reveal-cot")}, wantErr: `moderation_rule_presets[1] "en-reveal-cot" is listed more than once`},
		{name: "100 rules", settings: ChannelOtherSettings{ModerationRules: rules(100, func(i int) ModerationRule { return keyword(fmt.Sprintf("k%d", i)) })}},
		{name: "101 rules", settings: ChannelOtherSettings{ModerationRules: rules(101, func(i int) ModerationRule { return keyword(fmt.Sprintf("k%d", i)) })}, wantErr: "moderation_rules must have at most 100 rules"},
		{name: "30 regexes", settings: ChannelOtherSettings{ModerationRules: rules(30, func(i int) ModerationRule { return regex(fmt.Sprintf("r%d", i)) })}},
		{name: "31 regexes", settings: ChannelOtherSettings{ModerationRules: rules(31, func(i int) ModerationRule { return regex(fmt.Sprintf("r%d", i)) })}, wantErr: "moderation_rules must have at most 30 regex rules"},
		{name: "unknown type", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword("a"), {Type: "glob", Pattern: "b"}}}, wantErr: "moderation_rules[1].type must be keyword or regex"},
		{name: "untrimmed name", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{{Name: "x ", Type: "keyword", Pattern: "a"}}}, wantErr: "moderation_rules[0].name must not start or end with spaces"},
		{name: "64-rune name", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{{Name: strings.Repeat("名", 64), Type: "keyword", Pattern: "a"}}}},
		{name: "65-rune name", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{{Name: strings.Repeat("名", 65), Type: "keyword", Pattern: "a"}}}, wantErr: "moderation_rules[0].name must be at most 64 characters"},
		{name: "empty pattern", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword("")}}, wantErr: "moderation_rules[0].pattern must not be empty"},
		{name: "untrimmed pattern", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword(" a")}}, wantErr: "moderation_rules[0].pattern must not start or end with spaces"},
		{name: "512-rune pattern", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword(strings.Repeat("词", 512))}}},
		{name: "513-rune pattern", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword(strings.Repeat("词", 513))}}, wantErr: "moderation_rules[0].pattern must be at most 512 characters"},
		{name: "U+200E only", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword("‎")}}, wantErr: "moderation_rules[0].pattern must contain a visible character"},
		{name: "combining marks only", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword("̲́️")}}, wantErr: "moderation_rules[0].pattern must contain a visible character"},
		{name: "macron becomes a combining mark", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword("¯")}}, wantErr: "moderation_rules[0].pattern must contain a visible character"},
		{name: "keyword repeated ignoring case", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{keyword("Secret"), regex("secret"), {Type: "keyword", Pattern: "SECRET", CaseSensitive: true}}}, wantErr: "moderation_rules[2].pattern repeats moderation_rules[0]"},
		{name: "regex repeated", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("a+b"), regex("A+B"), regex("a+b")}}, wantErr: "moderation_rules[2].pattern repeats moderation_rules[0]"},
		{name: "invalid regex", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`(?=x)y`)}}, wantErr: "moderation_rules[0].pattern is not a valid regular expression: error parsing regexp: invalid or unsupported Perl syntax: `(?=`"},
		{name: "invalid regex error is cut", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(longError)}}, wantErr: "moderation_rules[0].pattern is not a valid regular expression: " + cutModerationError("error parsing regexp: missing closing ): `(?i)"+longError+"`")},
		{name: "too complex", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(heavy)}}, wantErr: "moderation_rules[0].pattern is too complex (more than 2000 regular expression instructions)"},
		{name: "2002 instructions", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("a.{0,999}q")}}, wantErr: "moderation_rules[0].pattern is too complex (more than 2000 regular expression instructions)"},
		{name: "matches empty text", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("a*")}}, wantErr: "moderation_rules[0].pattern matches empty text"},
		{name: "full-width exclamation mark", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("思维链！")}}, wantErr: normalised("！")},
		{name: "full-width letters", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`ｓｅｃｒｅｔ\s*plan`)}}, wantErr: normalised("Ｓ")},
		{name: "full-width letters, case-sensitive", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{{Type: "regex", Pattern: `ｓｅｃｒｅｔ`, CaseSensitive: true}}}, wantErr: normalised("ｓ")},
		{name: "escaped full-width rune", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`a\x{FF01}`)}}, wantErr: normalised("！")},
		{name: "tab", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`a\tb`)}}, wantErr: normalised("\t")},
		{name: "zero-width space", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`a\x{200B}b`)}}, wantErr: normalised("\u200b")},
		{name: "two spaces", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("secret  plan")}}, wantErr: normalised("  ")},
		{name: "class of full-width forms only", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("思维链[！？]")}}, wantErr: normalised("！")},
		{name: "class with a half-width alternative", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex("思维链[！!]"), regex(`输出[^。！？!?\n]{0,10}推理`), regex(`(?i)dump\s+cot`), regex("。")}}},
		{name: "curly apostrophe", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`don’t\s+reveal`)}}, wantErr: normalised("’")},
		{name: "curly quote", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`“cot”`)}}, wantErr: normalised("“")},
		{name: "Unicode dash", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`chain–of–thought`)}}, wantErr: normalised("–")},
		{name: "space between Chinese characters", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`你的 思维链`)}}, wantErr: normalised("的 思")},
		{name: "line break between Chinese characters", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`思维\n链`)}}, wantErr: normalised("维\n链")},
		{name: "spaces the rule view keeps", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{regex(`回答 别输出`), regex(`输出 cot`), regex(`思维\s*链`), regex(`chain-of-thought`), regex(`don't`)}}},
		{name: "valid rules", settings: ChannelOtherSettings{ModerationRules: []ModerationRule{{Name: "cot", Type: "keyword", Pattern: "chain of thought"}, {Type: "regex", Pattern: `(?i)reveal\s+your`, CaseSensitive: true}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErr == "" {
				assert.NoError(t, tc.settings.ValidateModeration())
			} else {
				assert.EqualError(t, tc.settings.ValidateModeration(), tc.wantErr)
			}
		})
	}
	assert.LessOrEqual(t, len("moderation_rules[0].pattern is not a valid regular expression: "+cutModerationError("error parsing regexp: missing closing ): `(?i)"+longError+"`")), len("moderation_rules[0].pattern is not a valid regular expression: ")+200+len("…"))
	assert.Equal(t, 2002, moderationRegexProgramSize("a.{0,999}q", false))
	imagine, ok := ModerationRulePresetByID("en-imagine-internal-reasoning")
	require.True(t, ok)
	assert.Equal(t, 606, moderationRegexProgramSize(imagine.Pattern, false))
	assert.NoError(t, (&ChannelOtherSettings{ModerationRules: []ModerationRule{regex(imagine.Pattern)}}).ValidateModeration(), "the largest preset is within the limit")

	// Only saved values are written; an explicitly empty preset list survives
	// the round trip that re-saving the settings does, an absent one stays absent.
	encoded, err := json.Marshal(ChannelOtherSettings{})
	require.NoError(t, err)
	assert.Equal(t, "{}", string(encoded))
	for _, raw := range []string{`{"moderation_rule_presets":[]}`, `{"moderation_enabled":true}`} {
		var decoded ChannelOtherSettings
		require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
		encoded, err = json.Marshal(decoded)
		require.NoError(t, err)
		assert.JSONEq(t, raw, string(encoded))
	}
	full := `{"moderation_enabled":true,"moderation_fail_open":true,"moderation_categories":["sexual"],"moderation_skip_images":true,"moderation_max_chars":5000,"moderation_timeout_seconds":5,"moderation_rule_presets":["en-reveal-cot"],"moderation_rules":[{"name":"n","type":"regex","pattern":"x+","case_sensitive":true}],"send_safety_identifier":true}`
	var decoded ChannelOtherSettings
	require.NoError(t, json.Unmarshal([]byte(full), &decoded))
	assert.Equal(t, ChannelOtherSettings{
		ModerationEnabled: true, ModerationFailOpen: true, ModerationCategories: []string{"sexual"}, ModerationSkipImages: true,
		ModerationMaxChars: 5000, ModerationTimeoutSeconds: 5, ModerationRulePresets: presets("en-reveal-cot"),
		ModerationRules:      []ModerationRule{{Name: "n", Type: "regex", Pattern: "x+", CaseSensitive: true}},
		SendSafetyIdentifier: true,
	}, decoded)
	encoded, err = json.Marshal(decoded)
	require.NoError(t, err)
	assert.JSONEq(t, full, string(encoded))
}

// The built-in rules are the catalogue the web shows: unique IDs in two risk
// groups, every pattern valid with its case flag, not matching empty text,
// within the program limit and with hints; both negation guards compile, and
// each call returns a fresh catalogue.
func TestModerationRulePresetsAreValid(t *testing.T) {
	presets := ModerationRulePresets()
	require.Len(t, presets, 16)
	seen := map[string]bool{}
	for _, preset := range presets {
		assert.False(t, seen[preset.ID], preset.ID)
		seen[preset.ID] = true
		assert.Contains(t, []string{ModerationPresetGroupLower, ModerationPresetGroupHigher}, preset.Group, preset.ID)
		assert.Equal(t, preset.Group == ModerationPresetGroupLower, preset.DefaultOn, preset.ID)
		assert.Contains(t, []string{ModerationNegationNone, ModerationNegationEnglish, ModerationNegationChinese}, preset.Negation, preset.ID)
		assert.NotEmpty(t, preset.Hints, preset.ID)
		for _, hint := range preset.Hints {
			assert.Equal(t, strings.ToLower(hint), hint, preset.ID)
		}
		re, err := CompileModerationRegex(preset.Pattern, preset.CaseSensitive)
		require.NoError(t, err, preset.ID)
		assert.Empty(t, moderationRegexUnmatchableText(preset.Pattern, preset.CaseSensitive), preset.ID)
		assert.False(t, re.MatchString(""), preset.ID)
		assert.LessOrEqual(t, moderationRegexProgramSize(preset.Pattern, preset.CaseSensitive), ModerationRegexProgramMax, preset.ID)
		found, ok := ModerationRulePresetByID(preset.ID)
		assert.True(t, ok)
		assert.Equal(t, preset, found)
	}
	// relay/moderation compiles the guards with the standard library's RE2.
	for _, guard := range []string{ModerationNegationGuardEnglish, ModerationNegationGuardChinese} {
		_, err := stdregexp.Compile(guard)
		assert.NoError(t, err)
	}
	_, ok := ModerationRulePresetByID("gone")
	assert.False(t, ok)
	presets[0].Pattern = "x"
	presets[0].Hints[0] = "x"
	assert.NotEqual(t, "x", ModerationRulePresets()[0].Pattern)
	assert.Equal(t, "chain", ModerationRulePresets()[0].Hints[0])
}
