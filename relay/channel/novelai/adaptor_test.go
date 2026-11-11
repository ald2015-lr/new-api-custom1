package novelai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NovelAI generates images at image.novelai.net and answers the account at
// api.novelai.net; a NovelAI-compatible forwarder answers both itself.
func TestAccountBaseURLMapsNovelAIsImageHostToItsAccountHost(t *testing.T) {
	for baseURL, want := range map[string]string{
		"":                                      "https://api.novelai.net",
		"https://image.novelai.net":             "https://api.novelai.net",
		" https://image.novelai.net/ ":          "https://api.novelai.net",
		"http://IMAGE.NovelAI.net.":             "https://api.novelai.net",
		"http://10.0.0.2:3585/":                 "http://10.0.0.2:3585",
		"https://nai.example.com/base":          "https://nai.example.com/base",
		"https://image.novelai.net.example.com": "https://image.novelai.net.example.com",
	} {
		assert.Equal(t, want, AccountBaseURL(baseURL), baseURL)
	}
}

func TestAdaptorServesOnlyTheNovelAINativeRoutes(t *testing.T) {
	require.NoError(t, i18n.Init())
	adaptor := &Adaptor{}
	for _, tc := range []struct {
		baseURL   string
		relayMode int
		want      string
	}{
		{"", relayconstant.RelayModeNovelAIGenerateImage, "https://image.novelai.net/ai/generate-image"},
		{"https://image.novelai.net/", relayconstant.RelayModeNovelAIGenerateImageStream, "https://image.novelai.net/ai/generate-image-stream"},
		{"http://10.0.0.2:3585", relayconstant.RelayModeNovelAIGenerateImage, "http://10.0.0.2:3585/ai/generate-image"},
	} {
		requestURL, err := adaptor.GetRequestURL(&relaycommon.RelayInfo{RelayMode: tc.relayMode, ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: tc.baseURL}})
		require.NoError(t, err)
		assert.Equal(t, tc.want, requestURL)
	}

	// Pass-through bodies skip the conversions, so the URL refuses too.
	_, err := adaptor.GetRequestURL(&relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{}})
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Equal(t, i18n.Translate("", i18n.MsgNovelAIChannelNativeOnly), apiErr.Error())
	_, err = adaptor.ConvertImageRequest(nil, &relaycommon.RelayInfo{}, dto.ImageRequest{Model: "nai-diffusion-4-5-full"})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Contains(t, adaptor.GetModelList(), "nai-diffusion-4-5-full-inpainting")
}

// The channel test and the balance read the account with GET
// /user/subscription, which spends no Anlas, and nothing else.
func TestFetchSubscriptionReadsTheAccount(t *testing.T) {
	service.InitHttpClient()
	const valid = `{"tier":3,"active":true,"expiresAt":1790000000,"trainingStepsLeft":{"fixedTrainingStepsLeft":10000,"purchasedTrainingSteps":250},"accountType":0}`
	anlas := func(value int64) *int64 { return &value }
	for _, tc := range []struct {
		name, body string
		status     int
		want       *Subscription
		// wantStatus and wantMessage describe a failure.
		wantStatus  int
		wantMessage string
	}{
		{name: "subscription", status: http.StatusOK, body: valid, want: &Subscription{Tier: 3, Active: true, Anlas: anlas(10250)}},
		{name: "inactive account without Anlas fields", status: http.StatusOK, body: `{"tier":0,"active":false}`, want: &Subscription{Tier: 0, Active: false}},
		{name: "purchased steps missing", status: http.StatusOK, body: `{"tier":1,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":5}}`, want: &Subscription{Tier: 1, Active: true}},
		{name: "negative steps", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":-1,"purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "fractional steps", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":1.5,"purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "steps past 2^53", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":9007199254740991,"purchasedTrainingSteps":1}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "huge steps", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":1e300,"purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "steps as a string", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":"100","purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "steps past float64", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":1e400,"purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "steps as a boolean", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":true,"purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "null steps", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":null,"purchasedTrainingSteps":0}}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "training steps as an array", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":[]}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "training steps as a string", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":"x"}`, want: &Subscription{Tier: 3, Active: true}},
		{name: "largest exact balance", status: http.StatusOK, body: `{"tier":3,"active":true,"trainingStepsLeft":{"fixedTrainingStepsLeft":9007199254740990,"purchasedTrainingSteps":1}}`, want: &Subscription{Tier: 3, Active: true, Anlas: anlas(1<<53 - 1)}},
		{name: "tier missing", status: http.StatusOK, body: `{"active":true}`, wantStatus: http.StatusInternalServerError, wantMessage: "NovelAI account response is not a subscription"},
		{name: "active missing", status: http.StatusOK, body: `{"tier":3}`, wantStatus: http.StatusInternalServerError, wantMessage: "NovelAI account response is not a subscription"},
		{name: "tier of another type", status: http.StatusOK, body: `{"tier":"3","active":true}`, wantStatus: http.StatusInternalServerError, wantMessage: "NovelAI account response is not a subscription"},
		{name: "not JSON", status: http.StatusOK, body: `<html>`, wantStatus: http.StatusInternalServerError, wantMessage: "NovelAI account response is not a subscription"},
		{name: "oversized", status: http.StatusOK, body: `{"tier":3,"active":true,"padding":"` + strings.Repeat("x", subscriptionResponseLimit) + `"}`, wantStatus: http.StatusInternalServerError, wantMessage: "NovelAI account response exceeds 262144 bytes"},
		{name: "rejected key", status: http.StatusUnauthorized, body: `{"statusCode":401,"message":"Invalid accessToken."}`, wantStatus: http.StatusUnauthorized, wantMessage: "NovelAI account answered HTTP 401: Invalid accessToken."},
		{name: "failure without a message", status: http.StatusBadGateway, body: `bad gateway`, wantStatus: http.StatusBadGateway, wantMessage: "NovelAI account answered HTTP 502"},
		{name: "long message", status: http.StatusTooManyRequests, body: `{"message":"` + strings.Repeat("y", 400) + `"}`, wantStatus: http.StatusTooManyRequests, wantMessage: "NovelAI account answered HTTP 429: " + strings.Repeat("y", maxErrorMessageRunes) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/user/subscription", r.URL.Path)
				assert.Equal(t, "Bearer pst-test", r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			subscription, apiErr := FetchSubscription(context.Background(), server.URL+"/", "pst-test", dto.ChannelSettings{}, nil)

			if tc.wantMessage != "" {
				require.NotNil(t, apiErr)
				assert.Nil(t, subscription)
				assert.Equal(t, tc.wantStatus, apiErr.StatusCode)
				assert.Equal(t, tc.wantMessage, apiErr.Error())
				return
			}
			require.Nil(t, apiErr)
			assert.Equal(t, tc.want, subscription)
		})
	}

	t.Run("redirect is returned, not followed with the key", func(t *testing.T) {
		var redirected atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirected.Add(1)
			_, _ = w.Write([]byte(valid))
		}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/user/subscription", http.StatusFound)
		}))
		defer server.Close()

		subscription, apiErr := FetchSubscription(context.Background(), server.URL, "pst-test", dto.ChannelSettings{}, nil)

		require.NotNil(t, apiErr)
		assert.Nil(t, subscription)
		assert.Equal(t, http.StatusFound, apiErr.StatusCode)
		assert.Zero(t, redirected.Load())
	})

	t.Run("channel proxy and header overrides apply", func(t *testing.T) {
		var proxied atomic.Int32
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxied.Add(1)
			// A forward proxy receives the absolute URL of the account host.
			assert.Equal(t, "http://nai-forwarder.invalid/user/subscription", r.URL.String())
			assert.Equal(t, "Bearer pst-override", r.Header.Get("Authorization"))
			assert.Equal(t, "pst-test", r.Header.Get("X-Upstream-Key"))
			_, _ = w.Write([]byte(valid))
		}))
		defer proxy.Close()

		subscription, apiErr := FetchSubscription(context.Background(), "http://nai-forwarder.invalid", "pst-test", dto.ChannelSettings{Proxy: proxy.URL},
			map[string]any{"Authorization": "Bearer pst-override", "X-Upstream-Key": "{api_key}"})

		require.Nil(t, apiErr)
		assert.Equal(t, anlas(10250), subscription.Anlas)
		assert.Equal(t, int32(1), proxied.Load())
	})
}
