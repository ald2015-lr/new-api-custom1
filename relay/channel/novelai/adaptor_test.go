package novelai

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
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

// The adaptor sends the native routes to their NovelAI path and a converted
// OpenAI images request to /ai/generate-image; a pass-through body, which
// skips the conversion, and every other route are refused.
func TestAdaptorRequestURLs(t *testing.T) {
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

	converted := &Adaptor{generation: &Generation{}}
	streamed := &Adaptor{generation: &Generation{}, streamed: true}
	for _, relayMode := range []int{relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits, relayconstant.RelayModeChatCompletions} {
		info := &relaycommon.RelayInfo{RelayMode: relayMode, ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "http://10.0.0.2:3585/", ApiKey: "pst-key"}}
		requestURL, err := converted.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "http://10.0.0.2:3585/ai/generate-image", requestURL)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		c.Request.Header.Set("Accept", "application/json")
		header := http.Header{}
		require.NoError(t, converted.SetupRequestHeader(c, &header, info))
		assert.Equal(t, "application/json", header.Get("Content-Type"))
		assert.Empty(t, header.Values("Accept"), "NovelAI answers its default zip")
		assert.Equal(t, "Bearer pst-key", header.Get("Authorization"))

		if relayMode == relayconstant.RelayModeChatCompletions {
			// Chat asks NovelAI for the whole answer, whatever its stream.
			continue
		}
		// An images stream on a model that streams asks for NovelAI's events.
		requestURL, err = streamed.GetRequestURL(info)
		require.NoError(t, err)
		assert.Equal(t, "http://10.0.0.2:3585/ai/generate-image-stream", requestURL)
		header = http.Header{}
		require.NoError(t, streamed.SetupRequestHeader(c, &header, info))
		assert.Equal(t, "text/event-stream", header.Get("Accept"))
	}
	stream := (&Generation{Body: map[string]any{"parameters": map[string]any{"steps": 23}}}).RequestBody(true)
	assert.Equal(t, "sse", stream["parameters"].(map[string]any)["stream"])
	_, err := adaptor.ConvertOpenAIRequest(nil, &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeCompletions}, &dto.GeneralOpenAIRequest{})
	var completionErr *types.NewAPIError
	require.ErrorAs(t, err, &completionErr)
	assert.Equal(t, i18n.Translate("", i18n.MsgNovelAIChannelNativeOnly), completionErr.Error())

	// Pass-through bodies skip the conversions, so the URL refuses too.
	_, err = adaptor.GetRequestURL(&relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{}})
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Equal(t, i18n.Translate("", i18n.MsgNovelAIChannelNativeOnly), apiErr.Error())
	_, err = adaptor.ConvertImageRequest(nil, &relaycommon.RelayInfo{}, dto.ImageRequest{Model: "nai-diffusion-4-5-full"})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Contains(t, adaptor.GetModelList(), "nai-diffusion-4-5-full-inpainting")
	assert.Subset(t, adaptor.GetModelList(), []string{"nai-diffusion-5-full-inpainting", "nai-diffusion-5-full-medium-inpainting"})
	// Every model the channel offers converts, and nothing else.
	assert.ElementsMatch(t, ModelList, slices.Collect(maps.Keys(profiles)))
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

var updateGolden = flag.Bool("update", false, "update the golden NovelAI request bodies in testdata")

// testImage is a PNG of width x height filled by pixel.
func testImage(width, height int, pixel func(x, y int) color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, pixel(x, y))
		}
	}
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, img)
	return buffer.Bytes()
}

func solid(c color.Color) func(x, y int) color.Color {
	return func(int, int) color.Color { return c }
}

// decodedBase64Image decodes a base64 PNG of a NovelAI request.
func decodedBase64Image(t *testing.T, value any) image.Image {
	t.Helper()
	encoded, ok := value.(string)
	require.True(t, ok, "a base64 string")
	data, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Equal(t, "png", SniffImageFormat(data), "raw base64 PNG without a data: prefix")
	img, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	return img
}

// requestJSON marshals a generation's body, with its image and mask replaced
// by "PNG WxH" once they decode as PNGs of the canvas size.
func requestJSON(t *testing.T, generation *Generation) []byte {
	t.Helper()
	body, err := common.Marshal(generation.Body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(body, &decoded))
	parameters := decoded["parameters"].(map[string]any)
	for _, key := range []string{"image", "mask"} {
		if value, ok := parameters[key]; ok {
			bounds := decodedBase64Image(t, value).Bounds()
			assert.Equal(t, image.Pt(generation.Width, generation.Height), bounds.Size(), key+" matches the canvas")
			parameters[key] = fmt.Sprintf("PNG %dx%d", bounds.Dx(), bounds.Dy())
		}
	}
	normalized, err := common.IndentJson(mustMarshal(t, decoded))
	require.NoError(t, err)
	return append(normalized, '\n')
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}

func raw(value string) json.RawMessage { return json.RawMessage(value) }

// The NovelAI request of every model family for a generation, an img2img
// and an inpainting, as the web client sends them (params_version 4, its
// preset texts, the V4 captions mirroring input and negative_prompt), from
// the same OpenAI-format input: prompt "1girl, cat ears", negative "bad
// hands", seed 1234567890, an auto size, and for the edits a 96x64 source
// (auto size: its aspect at about 1024x1024, 1216x832) with img2img strength
// 0.5, and a mask whose right half is transparent. Run with -update to
// rewrite testdata/requests after a deliberate change, and review the diff.
func TestBuildGenerationGoldenRequests(t *testing.T) {
	require.NoError(t, i18n.Init())
	source := testImage(96, 64, func(x, y int) color.Color { return color.NRGBA{R: uint8(x * 2), G: uint8(y * 3), B: 128, A: 255} })
	mask := testImage(96, 64, func(x, y int) color.Color {
		if x >= 48 {
			return color.NRGBA{}
		}
		return color.NRGBA{A: 255}
	})
	extras := map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`), "seed": raw("1234567890")}
	type variant struct {
		name   string
		intent GenerationIntent
	}
	models := []string{
		"nai-diffusion-3", "nai-diffusion-furry-3", "nai-diffusion-4-curated-preview", "nai-diffusion-4-full",
		"nai-diffusion-4-5-curated", "nai-diffusion-4-5-full", "nai-diffusion-5-full", "nai-diffusion-5-curated", "nai-diffusion-5-full-medium",
	}
	var variants []variant
	for _, model := range models {
		base := GenerationIntent{Model: model, Prompt: "1girl, cat ears", N: 1, Extras: extras}
		img2img := base
		img2img.Image = source
		img2img.Extras = map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`), "seed": raw("1234567890"), "strength": raw("0.5")}
		infill := base
		infill.Image, infill.Mask = source, mask
		variants = append(variants, variant{model + "-generate", base}, variant{model + "-img2img", img2img}, variant{model + "-infill", infill})
	}
	partial := GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "1girl, cat ears", N: 1, Image: source, Mask: mask,
		Extras: map[string]json.RawMessage{"seed": raw("1234567890"), "strength": raw("0.6"), "noise": raw("0.1")}}
	variants = append(variants, variant{"nai-diffusion-4-5-full-infill-strength", partial})

	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			generation, apiErr := BuildGeneration(nil, tc.intent)
			require.Nil(t, apiErr)
			got := requestJSON(t, generation)
			path := filepath.Join("testdata", "requests", tc.name+".json")
			if *updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, got, 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

// buildParameters builds a generation and returns its parameters, failing
// the test on a rejection.
func buildParameters(t *testing.T, intent GenerationIntent) (*Generation, map[string]any) {
	t.Helper()
	if intent.N == 0 {
		intent.N = 1
	}
	generation, apiErr := BuildGeneration(nil, intent)
	require.Nil(t, apiErr)
	var parameters map[string]any
	require.NoError(t, common.Unmarshal(mustMarshal(t, generation.Body["parameters"]), &parameters))
	return generation, parameters
}

// buildRejected builds a generation that must be rejected with a final 400
// and returns the message.
func buildRejected(t *testing.T, intent GenerationIntent) string {
	t.Helper()
	if intent.N == 0 {
		intent.N = 1
	}
	generation, apiErr := BuildGeneration(nil, intent)
	require.NotNil(t, apiErr)
	assert.Nil(t, generation)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr), "another channel would reject it as well")
	return apiErr.Error()
}

// The OpenAI size becomes NovelAI's canvas: auto is 1024x1024 for a
// generation and the source's aspect ratio at about 1024x1024 for an edit,
// other sizes round to multiples of 64 and shrink into 3,145,728 pixels;
// parameters.width / height set it exactly. n is never clamped: billing
// reserved it, so a count past NovelAI's limit for the area (8 up to
// 360,448 px, 6 up to 409,600, else 4) or a different parameters.n_samples
// is rejected.
func TestBuildGenerationSizeAndCount(t *testing.T) {
	require.NoError(t, i18n.Init())
	wide := testImage(300, 100, solid(color.White))
	for _, tc := range []struct {
		name          string
		intent        GenerationIntent
		width, height int
	}{
		{"auto generation", GenerationIntent{Size: "auto"}, 1024, 1024},
		{"empty size", GenerationIntent{}, 1024, 1024},
		{"rounded to 64", GenerationIntent{Size: "1000x1500"}, 1024, 1472},
		{"small side held at 64", GenerationIntent{Size: "10x20"}, 64, 64},
		{"capped area keeps the aspect", GenerationIntent{Size: "4096x4096"}, 1728, 1728},
		{"extreme aspect stays in the area", GenerationIntent{Size: "65536x64"}, 49152, 64},
		{"aspect_ratio extension", GenerationIntent{Size: "auto", Extras: map[string]json.RawMessage{"aspect_ratio": raw(`"2:3"`)}}, 832, 1216},
		{"edit at the source aspect", GenerationIntent{Image: wide}, 1728, 576},
		{"edit at an explicit size", GenerationIntent{Image: wide, Size: "512x512"}, 512, 512},
		{"parameters set the canvas", GenerationIntent{Size: "1024x1024", Parameters: map[string]json.RawMessage{"width": raw("832"), "height": raw("1216")}}, 832, 1216},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.intent.Model = "nai-diffusion-4-5-full"
			generation, parameters := buildParameters(t, tc.intent)
			assert.Equal(t, float64(tc.width), parameters["width"])
			assert.Equal(t, float64(tc.height), parameters["height"])
			assert.Equal(t, fmt.Sprintf("%dx%d", tc.width, tc.height), generation.Size())
			assert.LessOrEqual(t, tc.width*tc.height, maxPixels)
		})
	}

	for _, tc := range []struct {
		size string
		n    int
		ok   bool
	}{
		{"512x704", 8, true}, {"512x704", 9, false},
		{"640x640", 6, true}, {"640x640", 7, false},
		{"1024x1024", 4, true}, {"1024x1024", 5, false},
		{"1728x1728", 4, true},
	} {
		t.Run(fmt.Sprintf("n %d at %s", tc.n, tc.size), func(t *testing.T) {
			intent := GenerationIntent{Model: "nai-diffusion-4-5-full", Size: tc.size, N: tc.n}
			if !tc.ok {
				assert.Contains(t, buildRejected(t, intent), "n must be at most")
				return
			}
			_, parameters := buildParameters(t, intent)
			assert.Equal(t, float64(tc.n), parameters["n_samples"])
		})
	}

	for name, intent := range map[string]GenerationIntent{
		"n_samples other than n":     {Size: "512x512", N: 2, Parameters: map[string]json.RawMessage{"n_samples": raw("3")}},
		"n past the tier of width":   {Size: "512x512", N: 8, Parameters: map[string]json.RawMessage{"width": raw("1024"), "height": raw("1024")}},
		"width not a multiple of 64": {Parameters: map[string]json.RawMessage{"width": raw("1000")}},
		"canvas past the area":       {Parameters: map[string]json.RawMessage{"width": raw("2048"), "height": raw("2048")}},
		"size not WxH":               {Size: "large"},
		"size side past the bound":   {Size: "100000x64"},
	} {
		t.Run(name, func(t *testing.T) {
			intent.Model = "nai-diffusion-4-5-full"
			buildRejected(t, intent)
		})
	}
	_, parameters := buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", N: 2, Parameters: map[string]json.RawMessage{"n_samples": raw("2"), "n": raw("7")}})
	assert.Equal(t, float64(2), parameters["n_samples"], "an equal n_samples is accepted")
	assert.NotContains(t, parameters, "n", "new-api's billing parameters.n never reaches NovelAI")
}

// NovelAI keys come from the parameters object (NovelAI wire names, any key)
// over the top-level extension keys over the OpenAI fields over the model's
// defaults. The relay's own keys cannot be set, keys the relay does not
// interpret go to NovelAI as sent, and the model's rules have the last word.
func TestBuildGenerationPassthroughPrecedence(t *testing.T) {
	require.NoError(t, i18n.Init())
	generation, parameters := buildParameters(t, GenerationIntent{
		Model: "nai-diffusion-4-5-full", Prompt: "1girl",
		Extras: map[string]json.RawMessage{"steps": raw("30"), "scale": raw("4"), "sampler": raw(`"k_euler"`), "seed": raw("77")},
		Parameters: map[string]json.RawMessage{
			"steps": raw("28"), "seed": raw("88"), "noise_schedule": raw(`"native"`),
			"reference_strength_multiple": raw("[0.6]"), "add_original_image": raw("false"), "future_flag": raw(`{"a":1}`),
		},
	})
	assert.Equal(t, float64(28), parameters["steps"], "parameters over the top level")
	assert.Equal(t, float64(4), parameters["scale"], "the top level over the default")
	assert.Equal(t, "k_euler", parameters["sampler"])
	assert.Equal(t, float64(88), parameters["seed"])
	assert.Equal(t, int64(88), generation.Seed)
	assert.Equal(t, "karras", parameters["noise_schedule"], "V4.5 has no native schedule")
	assert.Equal(t, []any{0.6}, parameters["reference_strength_multiple"], "unknown keys are forwarded verbatim")
	assert.Equal(t, false, parameters["add_original_image"], "a forwarded key replaces the relay's default")
	assert.Equal(t, map[string]any{"a": float64(1)}, parameters["future_flag"])

	for _, unset := range []string{"0", "-1", "null"} {
		generation, parameters := buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Extras: map[string]json.RawMessage{"seed": raw(unset)}})
		assert.True(t, generation.Seed >= 1 && generation.Seed <= MaxSeed, "seed %s draws one NovelAI takes: %d", unset, generation.Seed)
		assert.Equal(t, float64(generation.Seed), parameters["seed"])
	}

	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-5-full", Parameters: map[string]json.RawMessage{
		"noise_schedule": raw(`"exponential"`), "skip_cfg_above_sigma": raw("19"), "dynamic_thresholding": raw("true"),
	}})
	assert.Equal(t, "karras", parameters["noise_schedule"], "V5 always uses karras")
	assert.NotContains(t, parameters, "skip_cfg_above_sigma", "V5 has no Variety+")
	assert.Equal(t, false, parameters["dynamic_thresholding"], "V5 has no decrisper")

	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-5-full-medium", Prompt: "1girl", Extras: map[string]json.RawMessage{
		"steps": raw("28"), "sampler": raw(`"k_dpmpp_2m"`), "cfg_rescale": raw("0.5"), "negative_prompt": raw(`"mine"`), "ucPresetId": raw(`"none"`),
	}})
	assert.Equal(t, float64(14), parameters["steps"], "V5 Full Medium fixes its steps")
	assert.Equal(t, "k_euler_ancestral", parameters["sampler"])
	assert.NotContains(t, parameters, "cfg_rescale")
	assert.Equal(t, "nsfw, "+ucV5Heavy, parameters["negative_prompt"], "V5 Full Medium takes only the heavy UC")

	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-3", Extras: map[string]json.RawMessage{"sampler": raw(`"nai_smea_dyn"`)}})
	assert.Equal(t, "k_euler_ancestral", parameters["sampler"])
	assert.Equal(t, true, parameters["sm"])
	assert.Equal(t, true, parameters["sm_dyn"])
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-3", Extras: map[string]json.RawMessage{"sampler": raw(`"ddim"`)}})
	assert.Equal(t, "ddim_v3", parameters["sampler"])
	assert.NotContains(t, parameters, "noise_schedule", "DDIM takes no noise schedule")
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-curated", Extras: map[string]json.RawMessage{"variety": raw("true")}, Size: "1024x1024"})
	assert.InDelta(t, 59.047, parameters["skip_cfg_above_sigma"], 0.001, "Variety+ at 1024x1024 on V4.5")

	for name, intent := range map[string]GenerationIntent{
		"relay-owned image":               {Parameters: map[string]json.RawMessage{"image": raw(`"abc"`)}},
		"relay-owned action":              {Parameters: map[string]json.RawMessage{"action": raw(`"infill"`)}},
		"relay-owned stream":              {Parameters: map[string]json.RawMessage{"stream": raw(`"sse"`)}},
		"cache key":                       {Parameters: map[string]json.RawMessage{"image_cache_secret_key": raw(`"k"`)}},
		"steps out of range":              {Extras: map[string]json.RawMessage{"steps": raw("51")}},
		"scale out of range":              {Parameters: map[string]json.RawMessage{"scale": raw("11")}},
		"seed out of range":               {Extras: map[string]json.RawMessage{"seed": raw("4294967289")}},
		"fractional seed":                 {Extras: map[string]json.RawMessage{"seed": raw("1.5")}},
		"steps as text":                   {Extras: map[string]json.RawMessage{"steps": raw(`"many"`)}},
		"unknown sampler":                 {Extras: map[string]json.RawMessage{"sampler": raw(`"ddim_v3"`)}},
		"SMEA on V4.5":                    {Extras: map[string]json.RawMessage{"sm": raw("true")}},
		"unknown noise schedule":          {Extras: map[string]json.RawMessage{"noise_schedule": raw(`"linear"`)}},
		"V3 sampler alias on V4.5":        {Extras: map[string]json.RawMessage{"sampler": raw(`"nai_smea"`)}},
		"light quality only on V5":        {Extras: map[string]json.RawMessage{"qualityPresetId": raw(`"light"`)}},
		"UC preset index past list":       {Extras: map[string]json.RawMessage{"ucPreset": raw("5")}},
		"characters past six on V4.5":     {Extras: map[string]json.RawMessage{"characterPrompts": raw(`[{"prompt":"a"},{"prompt":"b"},{"prompt":"c"},{"prompt":"d"},{"prompt":"e"},{"prompt":"f"},{"prompt":"g"}]`)}},
		"unknown model":                   {Model: "nai-diffusion-2"},
		"inpainting model without a mask": {Model: "nai-diffusion-4-5-full-inpainting"},
		"transparency before V5":          {Transparent: true},
	} {
		t.Run(name, func(t *testing.T) {
			if intent.Model == "" {
				intent.Model = "nai-diffusion-4-5-full"
			}
			buildRejected(t, intent)
		})
	}
	assert.Contains(t, buildRejected(t, GenerationIntent{Model: "nai-diffusion-3", Extras: map[string]json.RawMessage{"characterPrompts": raw(`[{"prompt":"a"}]`)}}), "at most 0 character")
}

// The extension keys come from an allowlist under NovelAI's names; an alias
// counts only when the NovelAI name is absent, other top-level keys are
// ignored, and keys whose meaning is unknown are rejected. A multipart form
// gives the same fields, with SDK bracket keys for parameters.
func TestParseExtensions(t *testing.T) {
	require.NoError(t, i18n.Init())
	// Go visits a map in random order: the result must not depend on it.
	for range 32 {
		extras, parameters, apiErr := ParseExtensions(nil, map[string]json.RawMessage{
			"negative": raw(`"alias"`), "negative_prompt": raw(`"canonical"`), "characters": raw(`[{"prompt":"a"}]`),
			"uc_preset": raw(`"light"`), "steps": raw("28"), "user": raw(`"u-1"`), "moderation": raw(`"low"`),
			"parameters": raw(`{"scale":6}`),
		})
		require.Nil(t, apiErr)
		require.Equal(t, map[string]json.RawMessage{
			"negative_prompt": raw(`"canonical"`), "characterPrompts": raw(`[{"prompt":"a"}]`), "ucPresetId": raw(`"light"`), "steps": raw("28"),
		}, extras)
		require.Equal(t, map[string]json.RawMessage{"scale": raw("6")}, parameters)
	}

	for _, key := range []string{"cfg", "SMEA", "DYN", "addition"} {
		_, _, apiErr := ParseExtensions(nil, map[string]json.RawMessage{key: raw("5")})
		require.NotNil(t, apiErr, key)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	}
	_, _, apiErr := ParseExtensions(nil, map[string]json.RawMessage{"parameters": raw(`[1]`)})
	require.NotNil(t, apiErr)

	fields := FormFields(map[string][]string{
		"negative_prompt": {"123"}, "steps": {"28"}, "qualityToggle": {"false"}, "parameters[scale]": {"6"}, "parameters[sampler]": {"k_euler"}, "prompt": {"true"},
	})
	assert.Equal(t, raw(`"123"`), fields["negative_prompt"], "a string key keeps its text")
	assert.Equal(t, raw("28"), fields["steps"])
	assert.Equal(t, raw("false"), fields["qualityToggle"])
	assert.Equal(t, raw(`"true"`), fields["prompt"])
	var formParameters map[string]json.RawMessage
	require.NoError(t, common.Unmarshal(fields["parameters"], &formParameters))
	assert.Equal(t, map[string]json.RawMessage{"scale": raw("6"), "sampler": raw(`"k_euler"`)}, formParameters)
}

// NovelAI does not expand presets, so the relay does as the web client:
// the quality tags after the prompt (after "transparent background" when
// asked for) and the UC preset in front of the negative prompt, with "nsfw, "
// first on non-Curated models unless the prompt mentions nsfw. A request
// turns them off with qualityToggle false, qualityPresetId or ucPresetId
// "none" (V3's none is "lowres" without a negative prompt), a channel can
// make them opt-in, and V4 captions a request sends are used verbatim.
func TestBuildGenerationPresets(t *testing.T) {
	require.NoError(t, i18n.Init())
	prompts := func(intent GenerationIntent) (string, string) {
		generation, parameters := buildParameters(t, intent)
		return generation.Body["input"].(string), parameters["negative_prompt"].(string)
	}
	input, negative := prompts(GenerationIntent{Model: "nai-diffusion-5-full", Prompt: "1girl", Transparent: true, Extras: map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`)}})
	assert.Equal(t, "1girl, transparent background, "+qualityV5Standard, input)
	assert.Equal(t, "nsfw, "+ucV5Heavy+", bad hands", negative)
	input, negative = prompts(GenerationIntent{Model: "nai-diffusion-5-full", Prompt: "1girl, nsfw", Extras: map[string]json.RawMessage{"qualityPresetId": raw(`"light"`), "ucPresetId": raw(`"light"`)}})
	assert.Equal(t, "1girl, nsfw, "+qualityV5Light, input)
	assert.Equal(t, ucV5Light, negative, "no nsfw tag when the prompt asks for it")
	input, negative = prompts(GenerationIntent{Model: "nai-diffusion-4-5-curated", Prompt: "1girl", Extras: map[string]json.RawMessage{"qualityToggle": raw("false"), "negative_prompt": raw(`"bad hands"`)}})
	assert.Equal(t, "1girl", input)
	assert.Equal(t, ucV45CurHeavy+", bad hands", negative, "Curated models get no nsfw tag")
	input, negative = prompts(GenerationIntent{Model: "nai-diffusion-4-full", Prompt: "1girl", Extras: map[string]json.RawMessage{"qualityPresetId": raw(`"none"`), "ucPresetId": raw(`"none"`), "negative_prompt": raw(`"bad hands"`)}})
	assert.Equal(t, "1girl", input)
	assert.Equal(t, "bad hands", negative)
	_, negative = prompts(GenerationIntent{Model: "nai-diffusion-3", Prompt: "1girl", Extras: map[string]json.RawMessage{"ucPresetId": raw(`"none"`)}})
	assert.Equal(t, "lowres", negative)
	_, negative = prompts(GenerationIntent{Model: "nai-diffusion-3", Prompt: "1girl", Extras: map[string]json.RawMessage{"ucPreset": raw("2")}})
	assert.Equal(t, "nsfw, "+ucV3HumanFocus, negative, "the legacy index follows the model's order")

	// A channel can make the presets opt-in.
	input, negative = prompts(GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "1girl", PresetsOff: true, Extras: map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`)}})
	assert.Equal(t, "1girl", input)
	assert.Equal(t, "bad hands", negative)
	input, negative = prompts(GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "1girl", PresetsOff: true, Extras: map[string]json.RawMessage{"qualityToggle": raw("true"), "ucPresetId": raw(`"light"`)}})
	assert.Equal(t, "1girl, "+qualityV5Standard, input)
	assert.Equal(t, "nsfw, "+ucV45FullLight, negative)

	generation, parameters := buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "ignored", Parameters: map[string]json.RawMessage{
		"v4_prompt":          raw(`{"caption":{"base_caption":"expert","char_captions":[]},"use_coords":false,"use_order":true}`),
		"v4_negative_prompt": raw(`{"caption":{"base_caption":"expert uc","char_captions":[]},"legacy_uc":false}`),
	}})
	assert.Equal(t, "expert", generation.Body["input"])
	assert.Equal(t, "expert", generation.Prompt)
	assert.Equal(t, "expert uc", parameters["negative_prompt"])
	assert.Equal(t, "expert", parameters["v4_prompt"].(map[string]any)["caption"].(map[string]any)["base_caption"])

	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "2girls", Extras: map[string]json.RawMessage{
		"characterPrompts": raw(`[{"prompt":"girl, red hair","uc":"hat","center":{"x":0.25,"y":0.62}},{"prompt":"off","enabled":false},{"prompt":"girl, blue hair"}]`),
	}})
	assert.Equal(t, true, parameters["use_coords"])
	assert.Equal(t, []any{
		map[string]any{"prompt": "girl, red hair", "uc": "hat", "center": map[string]any{"x": 0.3, "y": 0.7}, "enabled": true},
		map[string]any{"prompt": "girl, blue hair", "uc": "", "center": map[string]any{"x": float64(0), "y": float64(0)}, "enabled": true},
	}, parameters["characterPrompts"], "V4.5 snaps a position to its grid, 0,0 is automatic")
	captions := parameters["v4_prompt"].(map[string]any)["caption"].(map[string]any)["char_captions"].([]any)
	assert.Equal(t, map[string]any{"char_caption": "girl, red hair", "centers": []any{map[string]any{"x": 0.3, "y": 0.7}}}, captions[0])
	negatives := parameters["v4_negative_prompt"].(map[string]any)["caption"].(map[string]any)["char_captions"].([]any)
	assert.Equal(t, "hat", negatives[0].(map[string]any)["char_caption"])
}

// An edit's image and mask go to NovelAI at exactly the canvas size: the
// centred part with the canvas's aspect ratio, resampled (the mask with
// nearest neighbour). OpenAI's mask marks the area to edit with transparent
// pixels, NovelAI's with white on black, snapped to its 8x8 latent blocks; a
// mask without transparency is read as a black-and-white NovelAI mask, and
// one that selects nothing is rejected. A V5 Curated edit with a mask
// inpaints with V4.5 Curated's inpainting model and its rules.
func TestBuildGenerationEditImages(t *testing.T) {
	require.NoError(t, i18n.Init())
	// Red | green | blue thirds: a square canvas keeps the green centre.
	thirds := testImage(192, 64, func(x, y int) color.Color {
		switch {
		case x < 64:
			return color.NRGBA{R: 255, A: 255}
		case x < 128:
			return color.NRGBA{G: 255, A: 255}
		}
		return color.NRGBA{B: 255, A: 255}
	})
	generation, parameters := buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: thirds, Size: "64x64"})
	assert.Equal(t, ActionImg2Img, generation.Action)
	resized := decodedBase64Image(t, parameters["image"])
	assert.Equal(t, image.Rect(0, 0, 64, 64), resized.Bounds())
	for _, point := range []image.Point{{0, 0}, {63, 63}, {32, 32}} {
		r, g, b, _ := resized.At(point.X, point.Y).RGBA()
		assert.Equal(t, [3]uint32{0, 0xffff, 0}, [3]uint32{r, g, b}, "cover crop keeps the centre at %v", point)
	}
	assert.Equal(t, 0.7, parameters["strength"], "web default img2img strength")
	assert.Equal(t, float64(0), parameters["noise"])
	assert.Equal(t, true, parameters["color_correct"])
	assert.Equal(t, parameters["seed"], parameters["extra_noise_seed"])

	maskValue := func(img image.Image, x, y int) uint32 {
		r, g, b, a := img.At(x, y).RGBA()
		require.Equal(t, uint32(0xffff), a, "NovelAI's mask is opaque")
		require.Equal(t, r, g)
		require.Equal(t, r, b)
		return r >> 8
	}
	// OpenAI mask at half the image size: the square crop keeps columns
	// 32-63, transparent up to column 42, which at 128x128 covers columns
	// 0-43: blocks 0-4 white, block 5 (40-47) half white, so black.
	openAIMask := testImage(96, 32, func(x, y int) color.Color {
		if x < 32+11 {
			return color.NRGBA{}
		}
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	})
	source := testImage(192, 64, solid(color.Gray{Y: 128}))
	generation, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-5-curated", Image: source, Mask: openAIMask, Size: "128x128"})
	assert.Equal(t, ActionInfill, generation.Action)
	assert.Equal(t, "nai-diffusion-4-5-curated-inpainting", generation.Model, "V5 Curated inpaints with V4.5 Curated's model")
	assert.Equal(t, "nai-diffusion-4-5-curated-inpainting", generation.Body["model"])
	assert.NotContains(t, parameters, "straight_alpha", "V4.5 Curated's rules")
	assert.Equal(t, float64(5), parameters["scale"])
	assert.Equal(t, "NativeInfillingRequest", parameters["request_type"])
	assert.Equal(t, float64(1), parameters["inpaintImg2ImgStrength"], "pure inpainting")
	assert.NotContains(t, parameters, "img2img")
	mask := decodedBase64Image(t, parameters["mask"])
	assert.Equal(t, image.Rect(0, 0, 128, 128), mask.Bounds())
	assert.Equal(t, uint32(255), maskValue(mask, 0, 0))
	assert.Equal(t, uint32(255), maskValue(mask, 39, 127))
	assert.Equal(t, uint32(0), maskValue(mask, 40, 0), "an 8x8 block half white or less stays black")
	assert.Equal(t, uint32(0), maskValue(mask, 43, 0))
	assert.Equal(t, uint32(0), maskValue(mask, 127, 64))

	// A black-and-white mask without transparency: white repaints.
	bw := testImage(64, 64, func(x, y int) color.Color {
		if y >= 32 {
			return color.White
		}
		return color.Black
	})
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-3", Image: testImage(64, 64, solid(color.White)), Mask: bw, Size: "64x64"})
	mask = decodedBase64Image(t, parameters["mask"])
	assert.Equal(t, uint32(0), maskValue(mask, 10, 10))
	assert.Equal(t, uint32(255), maskValue(mask, 10, 40))
	assert.Equal(t, false, parameters["sm"], "no SMEA with an image")

	opaque := testImage(64, 64, solid(color.Black))
	assert.Contains(t, buildRejected(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: opaque, Mask: opaque}), "mask selects no area")
	assert.Contains(t, buildRejected(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: []byte("GIF89a"), Size: "64x64"}), "must be a PNG, JPEG or WebP")
	buildRejected(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Mask: bw})
	buildRejected(t, GenerationIntent{Model: "nai-diffusion-3", Image: opaque, Mask: bw, Extras: map[string]json.RawMessage{"strength": raw("0.5")}})
	buildRejected(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: opaque, Extras: map[string]json.RawMessage{"strength": raw("1")}})

	// Transparency: V5 only, and not on V5 Curated's V4.5 inpainting.
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-5-full", Transparent: true})
	assert.Equal(t, true, parameters["straight_alpha"])
	assert.Equal(t, true, parameters["tag_hint_transparent_background"])
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-5-full"})
	assert.NotContains(t, parameters, "tag_hint_transparent_background")
	buildRejected(t, GenerationIntent{Model: "nai-diffusion-5-curated", Image: source, Mask: openAIMask, Transparent: true})
}

// NovelAI's /ai/generate-image answers with a zip of the images (or JSON, or
// one bare image); only image payloads count. The client gets each image in
// the format it asked for: jpeg is transcoded with the alpha on white, png
// when NovelAI sent another format, webp as NovelAI encoded it.
func TestParseAndRenderImages(t *testing.T) {
	first := testImage(8, 8, solid(color.NRGBA{R: 255, A: 255}))
	second := testImage(8, 8, solid(color.NRGBA{A: 0}))
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, data := range map[string][]byte{"image_0.png": first, "notes.txt": []byte("not an image")} {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write(data)
		require.NoError(t, err)
	}
	entry, err := writer.Create("image_1.png")
	require.NoError(t, err)
	_, err = entry.Write(second)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	images, err := ParseImages(archive.Bytes())
	require.NoError(t, err)
	require.Len(t, images, 2)
	assert.Equal(t, second, images[1], "archive order")
	images, err = ParseImages([]byte(`{"images":[{"image":"` + base64.StdEncoding.EncodeToString(first) + `"},{"image":""},{"seed":3}]}`))
	require.NoError(t, err)
	assert.Equal(t, [][]byte{first}, images, "payload-less entries count nothing")
	images, err = ParseImages(first)
	require.NoError(t, err)
	assert.Len(t, images, 1)
	images, err = ParseImages([]byte(`{"statusCode":500,"message":"Error generating image"}`))
	require.NoError(t, err)
	assert.Empty(t, images)
	_, err = ParseImages([]byte("<html>"))
	assert.Error(t, err)

	rendered, format, err := RenderImage(second, formatJPEG, 90)
	require.NoError(t, err)
	assert.Equal(t, formatJPEG, format)
	decoded, err := jpeg.Decode(bytes.NewReader(rendered))
	require.NoError(t, err)
	r, g, b, _ := decoded.At(4, 4).RGBA()
	assert.Greater(t, min(r, g, b), uint32(0xf000), "transparency is flattened onto white")
	rendered, format, err = RenderImage(first, formatPNG, 100)
	require.NoError(t, err)
	assert.Equal(t, formatPNG, format)
	assert.Equal(t, first, rendered, "a PNG goes out as NovelAI sent it")
	webpImage, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	require.NoError(t, err)
	rendered, format, err = RenderImage(webpImage, formatWebP, 100)
	require.NoError(t, err)
	assert.Equal(t, formatWebP, format)
	assert.Equal(t, webpImage, rendered)
	rendered, format, err = RenderImage(webpImage, formatPNG, 100)
	require.NoError(t, err)
	assert.Equal(t, formatPNG, format, "a WebP NovelAI sent for a PNG request is transcoded")
	assert.Equal(t, formatPNG, SniffImageFormat(rendered))
}

// On NovelAI's own host one key generates one request at a time: the next
// waits for the slot (released when the response body closes, or when the
// client's request ends), fails with a retryable 429 after the bounded wait,
// and stops waiting with its client. Other hosts are not serialized.
func TestGenerationSlotSerializesNovelAIKeys(t *testing.T) {
	require.NoError(t, i18n.Init())
	savedWait := generationWait
	generationWait = 50 * time.Millisecond
	t.Cleanup(func() { generationWait = savedWait })
	request := func(ctx context.Context) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/images/generations", nil)
		return c
	}
	official := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "https://image.novelai.net", ApiKey: "pst-slot-test"}}

	release, apiErr := holdGeneration(request(context.Background()), official)
	require.Nil(t, apiErr)
	_, apiErr = holdGeneration(request(context.Background()), official)
	require.NotNil(t, apiErr, "the key is busy")
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.False(t, types.IsSkipRetryError(apiErr), "another channel may take it")
	assert.Equal(t, i18n.Translate("", i18n.MsgNovelAIAccountBusy), apiErr.Error())

	other := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "https://image.novelai.net", ApiKey: "pst-other-key"}}
	releaseOther, apiErr := holdGeneration(request(context.Background()), other)
	require.Nil(t, apiErr, "another key has its own slot")
	releaseOther()
	forwarder := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://10.0.0.2:3585", ApiKey: "pst-slot-test"}}
	releaseForwarder, apiErr := holdGeneration(request(context.Background()), forwarder)
	require.Nil(t, apiErr, "a forwarder queues for itself")
	releaseForwarder()

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan *types.NewAPIError)
	go func() {
		_, apiErr := holdGeneration(request(ctx), official)
		waiting <- apiErr
	}()
	cancel()
	apiErr = <-waiting
	require.NotNil(t, apiErr, "a client that left stops waiting")

	// A response body closing releases the slot for the next request.
	body := &generationBody{ReadCloser: io.NopCloser(strings.NewReader("")), release: release}
	require.NoError(t, body.Close())
	require.NoError(t, body.Close())
	next, apiErr := holdGeneration(request(context.Background()), official)
	require.Nil(t, apiErr)

	// The end of the client's request releases a slot nothing closed.
	ended, end := context.WithCancel(context.Background())
	next()
	_, apiErr = holdGeneration(request(ended), official)
	require.Nil(t, apiErr)
	end()
	assert.Eventually(t, func() bool {
		release, apiErr := holdGeneration(request(context.Background()), official)
		if apiErr != nil {
			return false
		}
		release()
		return true
	}, time.Second, 10*time.Millisecond)
}

// A NovelAI channel on NovelAI's image host holds its key's generation slot
// from the send until the response body closes, on the native routes as on
// the converted ones: a second generation meanwhile gets the retryable 429.
// The fake proxy plays NovelAI's host, which the test cannot reach.
func TestAdaptorSerializesGenerationsOnNovelAIsHost(t *testing.T) {
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	savedWait := generationWait
	generationWait = 50 * time.Millisecond
	t.Cleanup(func() { generationWait = savedWait })
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "http://image.novelai.net/ai/generate-image", r.URL.String())
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK\x03\x04"))
	}))
	defer proxy.Close()
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeNovelAIGenerateImage, ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://image.novelai.net", ApiKey: "pst-serialized",
		ChannelSetting: dto.ChannelSettings{Proxy: proxy.URL},
	}}
	send := func() (*http.Response, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", strings.NewReader("{}"))
		resp, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(`{"input":"a cat"}`))
		if err != nil {
			return nil, err
		}
		return resp.(*http.Response), nil
	}

	first, err := send()
	require.NoError(t, err)
	_, err = send()
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.Equal(t, int32(1), requests.Load(), "the second generation never reached NovelAI")

	require.NoError(t, first.Body.Close())
	second, err := send()
	require.NoError(t, err, "a closed response frees the slot")
	require.NoError(t, second.Body.Close())
	assert.Equal(t, int32(2), requests.Load())
}

// sseEvents is NovelAI's /ai/generate-image-stream answer in SSE mode.
func sseEvents(t *testing.T, events ...map[string]any) string {
	t.Helper()
	var body strings.Builder
	for _, event := range events {
		fmt.Fprintf(&body, "event: %s\ndata: %s\n\n", event["event_type"], mustMarshal(t, event))
	}
	return body.String()
}

// streamedEvent is one event an OpenAI image stream client received.
type streamedEvent struct {
	name string
	data map[string]any
}

// streamedEvents splits an SSE response into its events; a data-only event
// (an error chunk, [DONE]) has no name.
func streamedEvents(t *testing.T, body string) []streamedEvent {
	t.Helper()
	var events []streamedEvent
	for block := range strings.SplitSeq(body, "\n\n") {
		var event streamedEvent
		for line := range strings.SplitSeq(block, "\n") {
			if name, ok := strings.CutPrefix(line, "event: "); ok {
				event.name = name
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				if data == "[DONE]" {
					event.data = map[string]any{"[DONE]": true}
					continue
				}
				require.NoError(t, common.UnmarshalJsonStr(data, &event.data), data)
			}
		}
		if event.data != nil {
			events = append(events, event)
		}
	}
	return events
}

// imageStreamContext is a stream: true images request whose NovelAI answer
// is body, reserved at count images per call.
func imageStreamContext(t *testing.T, body, contentType string, count int) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	savedTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = savedTimeout })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, IsStream: true, RelayFormat: types.RelayFormatOpenAIImage}
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", float64(count))
	return c, recorder, resp, info
}

// cancelAfterWriter is a client that leaves once it received needle.
type cancelAfterWriter struct {
	gin.ResponseWriter
	needle string
	cancel context.CancelFunc
}

func (w *cancelAfterWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if strings.Contains(string(data), w.needle) {
		w.cancel()
	}
	return n, err
}

func (w *cancelAfterWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

// failingWriter is a client whose connection broke.
type failingWriter struct {
	gin.ResponseWriter
	attempts int
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.attempts++
	return 0, errors.New("broken pipe")
}

func (w *failingWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

// An images stream relays NovelAI's SSE as OpenAI's events: at most
// partial_images previews of sample 0, evenly spaced over the steps (here 4
// steps and 2 partials: after steps 2 and 3), reported with their own format
// and size; one completed event per final in sample order whatever order
// NovelAI finished them in, with the seed of each image and the usage on the
// last only; no [DONE]. The billed count becomes the completed events once
// NovelAI ended the stream: an error event after 200, a duplicate final or a
// stream that ends early bills the images delivered (those after a missing
// one included) and ends with an error chunk; an error before any image
// fails the request, which bills nothing; a client that leaves keeps the
// reservation. V3, which cannot stream, answers whole and gets completed
// events only, named for edits on /v1/images/edits.
func TestImagesStreamRelaysNovelAIEvents(t *testing.T) {
	require.NoError(t, i18n.Init())
	red := testImage(16, 16, solid(color.NRGBA{R: 255, A: 255}))
	blue := testImage(16, 16, solid(color.NRGBA{B: 255, A: 255}))
	encode := base64.StdEncoding.EncodeToString
	// preview is a JPEG whose size names its sample and step: (8 + sample)
	// x (step + 1).
	preview := func(sample, step int) string {
		var buffer bytes.Buffer
		require.NoError(t, jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 8+sample, step+1)), nil))
		return encode(buffer.Bytes())
	}
	intermediate := func(sample, step int) map[string]any {
		return map[string]any{"event_type": "intermediate", "samp_ix": sample, "step_ix": step, "gen_id": 7, "sigma": 1.5, "image": preview(sample, step)}
	}
	final := func(sample int, data []byte) map[string]any {
		return map[string]any{"event_type": "final", "samp_ix": sample, "gen_id": 7, "image": encode(data)}
	}
	failure := map[string]any{"event_type": "error", "code": 500, "message": "Error generating image, an internal error occurred"}
	generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 2, Extras: map[string]json.RawMessage{"steps": raw("4"), "seed": raw("100")}})
	require.Nil(t, apiErr)
	output := ImageOutput{OutputFormat: formatPNG, Background: "opaque", PartialImages: 2, Quality: "auto", Event: "image_generation"}
	promptTokens := generation.Usage().PromptTokens
	completed := func(t *testing.T, event streamedEvent, want []byte, seed float64, last bool) {
		t.Helper()
		assert.Equal(t, "image_generation.completed", event.name)
		assert.Equal(t, event.name, event.data["type"])
		got, err := base64.StdEncoding.DecodeString(event.data["b64_json"].(string))
		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Equal(t, seed, event.data["seed"])
		assert.Equal(t, "png", event.data["output_format"])
		assert.Equal(t, "1024x1024", event.data["size"])
		assert.Equal(t, "auto", event.data["quality"])
		assert.Equal(t, "opaque", event.data["background"])
		usage := event.data["usage"].(map[string]any)
		if last {
			assert.Equal(t, float64(promptTokens), usage["input_tokens"])
			assert.Equal(t, float64(promptTokens), usage["total_tokens"])
		} else {
			assert.Zero(t, usage["input_tokens"], "the usage counts once, on the last image")
		}
		assert.Zero(t, usage["output_tokens"])
	}
	errorChunk := func(t *testing.T, events []streamedEvent, message string) {
		t.Helper()
		require.GreaterOrEqual(t, len(events), 2)
		chunk := events[len(events)-2]
		assert.Empty(t, chunk.name)
		require.Contains(t, chunk.data, "error")
		assert.Contains(t, chunk.data["error"].(map[string]any)["message"], message)
		assert.Equal(t, map[string]any{"[DONE]": true}, events[len(events)-1].data)
	}

	t.Run("partials from sample 0, completed in sample order", func(t *testing.T) {
		body := sseEvents(t, intermediate(0, 0), intermediate(1, 0), intermediate(0, 1), intermediate(1, 1), intermediate(1, 2), intermediate(0, 2), intermediate(0, 3), final(1, blue), final(0, red))
		c, recorder, resp, info := imageStreamContext(t, body, "text/event-stream", 2)

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output)

		require.Nil(t, apiErr)
		assert.Equal(t, promptTokens, usage.PromptTokens)
		assert.Zero(t, usage.CompletionTokens)
		assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 4, recorder.Body.String())
		for i, event := range events[:2] {
			assert.Equal(t, "image_generation.partial_image", event.name)
			assert.Equal(t, float64(i), event.data["partial_image_index"])
			assert.Equal(t, preview(0, i+1), event.data["b64_json"], "sample 0 after steps 2 and 3")
			assert.Equal(t, "jpeg", event.data["output_format"], "the preview's own format")
			assert.Equal(t, fmt.Sprintf("8x%d", i+2), event.data["size"], "the preview's own size")
		}
		completed(t, events[2], red, 100, false)
		completed(t, events[3], blue, 101, true)
		assert.NotContains(t, recorder.Body.String(), "[DONE]", "OpenAI image streams send no [DONE]")
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
		assert.Contains(t, []relaycommon.StreamEndReason{relaycommon.StreamEndReasonDone, relaycommon.StreamEndReasonEOF}, info.StreamStatus.EndReason)
		assert.False(t, info.StreamStatus.ResponseFailed())
	})

	// The stream of a forwarder that labels it otherwise is still read as one.
	t.Run("no partials unless asked", func(t *testing.T) {
		body := sseEvents(t, intermediate(0, 0), intermediate(0, 1), intermediate(0, 2), final(0, red), final(1, blue))
		c, recorder, resp, info := imageStreamContext(t, body, "application/octet-stream", 2)
		withoutPartials := output
		withoutPartials.PartialImages = 0

		_, apiErr := imagesStreamResponse(c, resp, info, generation, withoutPartials)

		require.Nil(t, apiErr)
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 2)
		completed(t, events[0], red, 100, false)
		completed(t, events[1], blue, 101, true)
	})

	// An image sent in order before the failure went out as not the last,
	// without the usage; one held back for a missing sample goes out last,
	// with it.
	for _, tc := range []struct {
		name      string
		events    []map[string]any
		want      [][]byte
		seeds     []float64
		withUsage bool
		message   string
	}{
		{"an error event after 200 bills the images delivered", []map[string]any{final(0, red), failure}, [][]byte{red}, []float64{100}, false, "Error generating image, an internal error occurred"},
		{"a stream that ends early bills the images that arrived", []map[string]any{intermediate(0, 0), final(1, blue)}, [][]byte{blue}, []float64{101}, true, "ended after 1 of 2 images"},
		{"a duplicate final breaks the stream", []map[string]any{final(0, red), final(0, blue)}, [][]byte{red}, []float64{100}, false, "already finished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder, resp, info := imageStreamContext(t, sseEvents(t, tc.events...), "text/event-stream", 2)

			usage, apiErr := imagesStreamResponse(c, resp, info, generation, output)

			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			events := streamedEvents(t, recorder.Body.String())
			require.Len(t, events, len(tc.want)+2, recorder.Body.String())
			for i, want := range tc.want {
				completed(t, events[i], want, tc.seeds[i], tc.withUsage && i == len(tc.want)-1)
			}
			errorChunk(t, events, tc.message)
			assert.Equal(t, float64(len(tc.want)), info.PriceData.OtherRatios()["n"])
			assert.True(t, info.StreamStatus.ResponseFailed())
		})
	}

	t.Run("an error before any image fails the request", func(t *testing.T) {
		c, recorder, resp, info := imageStreamContext(t, sseEvents(t, intermediate(0, 0), failure), "text/event-stream", 2)

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output)

		require.NotNil(t, apiErr)
		assert.Nil(t, usage)
		assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
		assert.True(t, types.IsSkipRetryError(apiErr), "NovelAI accepted the job")
		assert.Contains(t, apiErr.Error(), "Error generating image, an internal error occurred")
		assert.NotContains(t, recorder.Body.String(), "completed")
	})

	t.Run("a client that leaves keeps the reservation", func(t *testing.T) {
		c, _, resp, info := imageStreamContext(t, sseEvents(t, final(0, red), final(1, blue)), "text/event-stream", 2)
		ctx, cancel := context.WithCancel(c.Request.Context())
		t.Cleanup(cancel)
		c.Request = c.Request.WithContext(ctx)
		c.Writer = &cancelAfterWriter{ResponseWriter: c.Writer, needle: "completed", cancel: cancel}

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output)

		require.Nil(t, apiErr)
		require.NotNil(t, usage)
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "a client abort never lowers the charge")
	})

	// NovelAI may still generate what a broken stream (a transport error, a
	// timeout) was charged for: the reservation stays, and the client learns
	// the stream was cut.
	t.Run("a broken stream keeps the reservation", func(t *testing.T) {
		c, recorder, resp, info := imageStreamContext(t, "", "text/event-stream", 2)
		resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(sseEvents(t, final(0, red))), iotest.ErrReader(errors.New("connection reset by peer"))))

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output)

		require.Nil(t, apiErr)
		require.NotNil(t, usage)
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 3, recorder.Body.String())
		completed(t, events[0], red, 100, false)
		errorChunk(t, events, "upstream stream ended before the response was complete")
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
		assert.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	})

	// A client whose connection broke is a client that left, even when
	// NovelAI's stream ended before the failed write, and nothing more is
	// written to it.
	for _, event := range []map[string]any{intermediate(0, 3), final(0, red)} {
		t.Run(fmt.Sprintf("a failed write of a %s event keeps the reservation", event["event_type"]), func(t *testing.T) {
			c, _, _, info := imageStreamContext(t, "", "text/event-stream", 2)
			writer := &failingWriter{ResponseWriter: c.Writer}
			c.Writer = writer
			info.StreamStatus = relaycommon.NewStreamStatus()
			stream := &imageStream{c: c, info: info, generation: generation, output: output, usage: generation.Usage(),
				received: make([]bool, generation.NSamples), pending: map[int]renderedImage{}}

			stream.handle(string(mustMarshal(t, event)), &helper.StreamResult{})
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
			usage, apiErr := stream.finish()

			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "a client abort never lowers the charge")
			assert.Equal(t, 1, writer.attempts, "one failed write, no retry")
		})
	}

	t.Run("V3 answers whole: completed events from the zip", func(t *testing.T) {
		v3, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-3", Prompt: "a cat", N: 3, Image: red, Extras: map[string]json.RawMessage{"seed": raw("100")}})
		require.Nil(t, apiErr)
		require.False(t, v3.Streamable)
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for i, data := range [][]byte{red, blue} {
			entry, err := writer.Create(fmt.Sprintf("image_%d.png", i))
			require.NoError(t, err)
			_, err = entry.Write(data)
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		// NovelAI returned two of the three images reserved.
		c, recorder, resp, info := imageStreamContext(t, archive.String(), "application/zip", 3)
		edit := output
		edit.Event = "image_edit"

		usage, apiErr := imagesStreamResponse(c, resp, info, v3, edit)

		require.Nil(t, apiErr)
		assert.Equal(t, v3.Usage().PromptTokens, usage.PromptTokens)
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 2, recorder.Body.String())
		for i, event := range events {
			assert.Equal(t, "image_edit.completed", event.name)
			assert.Equal(t, v3.Size(), event.data["size"])
			assert.Equal(t, float64(100+i), event.data["seed"])
		}
		assert.Zero(t, events[0].data["usage"].(map[string]any)["input_tokens"])
		assert.Equal(t, float64(v3.Usage().PromptTokens), events[1].data["usage"].(map[string]any)["input_tokens"])
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "billed per image received")
		assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	})
}

// chatContext is a chat completion whose raw body is body.
func chatContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder, *dto.GeneralOpenAIRequest) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	request := &dto.GeneralOpenAIRequest{}
	require.NoError(t, common.UnmarshalBodyReusable(c, request))
	return c, recorder, request
}

// A chat completion generates from the text of the last user message only,
// its text parts joined by newlines; the system prompt and the history,
// which carries earlier images as base64, are left out. An image in that
// message makes it img2img; n is the number of images and size the canvas.
// NovelAI's extension keys come from the top level or extra_body, the top
// level first.
func TestChatGenerationIntent(t *testing.T) {
	require.NoError(t, i18n.Init())
	source := testImage(32, 32, solid(color.NRGBA{G: 255, A: 255}))
	dataURL := DataURL(source, formatPNG)
	history := `{"role":"system","content":"You draw."},{"role":"user","content":"a dog"},{"role":"assistant","content":"![image](` + dataURL + `)"}`
	for _, tc := range []struct {
		name   string
		body   string
		prompt string
		image  bool
		n      int
		size   string
		extras map[string]string
	}{
		{"string content", `{"model":"nai-diffusion-4-5-full","messages":[` + history + `,{"role":"user","content":"1girl, smile"}]}`, "1girl, smile", false, 1, "", map[string]string{}},
		{"text parts and an image", `{"model":"nai-diffusion-4-5-full","messages":[` + history + `,{"role":"user","content":[{"type":"text","text":"1girl"},{"type":"image_url","image_url":{"url":"` + dataURL + `"}},{"type":"text","text":"smile"}]}]}`, "1girl\nsmile", true, 1, "", map[string]string{}},
		{"n, size, extension keys and extra_body", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"n":2,"size":"832x1216","seed":7,"steps":10,"extra_body":{"steps":20,"negative_prompt":"bad hands"},"temperature":0.5}`, "a cat", false, 2, "832x1216", map[string]string{"seed": "7", "steps": "10", "negative_prompt": `"bad hands"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, request := chatContext(t, tc.body)

			intent, apiErr := chatGenerationIntent(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}, request)

			require.Nil(t, apiErr)
			assert.Equal(t, "nai-diffusion-4-5-full", intent.Model)
			assert.Equal(t, tc.prompt, intent.Prompt)
			assert.Equal(t, tc.n, intent.N)
			assert.Equal(t, tc.size, intent.Size)
			assert.Equal(t, formatPNG, intent.ImageFormat)
			if tc.image {
				assert.Equal(t, source, intent.Image)
			} else {
				assert.Nil(t, intent.Image)
			}
			extras := map[string]string{}
			for key, value := range intent.Extras {
				extras[key] = string(value)
			}
			assert.Equal(t, tc.extras, extras)
		})
	}

	for _, tc := range []struct {
		name, body, message string
	}{
		{"no user message", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"system","content":"a cat"}]}`, i18n.Translate("", i18n.MsgNovelAIChatPromptMissing)},
		{"two images", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + dataURL + `"}},{"type":"image_url","image_url":"` + dataURL + `"}]}]}`, "the request carries 2"},
		{"n above the image limit", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"n":129}`, "n must be between 1 and 128"},
		{"a negative n", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"n":-1}`, "n must be between 1 and 128"},
		{"an ambiguous key in extra_body", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"extra_body":{"cfg":5}}`, "cfg is not supported"},
		{"an image that is not one", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,bm90IGFuIGltYWdl"}}]}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, request := chatContext(t, tc.body)

			intent, apiErr := chatGenerationIntent(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}, request)
			if apiErr == nil {
				// An image is checked once the generation decodes it.
				_, apiErr = BuildGeneration(c, intent)
			}

			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.Contains(t, apiErr.Error(), tc.message)
		})
	}
}

// A chat completion answers with the images as Markdown PNG data URLs (a
// WebP NovelAI sent is transcoded: some chat clients drop other formats):
// without stream, one assistant message; with stream, a role chunk, one
// chunk per whole image, the stop chunk, the usage chunk unless the client
// turned it off, and [DONE]. The usage is the prompt estimate with no
// completion tokens, and the billed count the images sent. A 2xx without
// images is a final 502, which on a stream follows the role chunk.
func TestChatResponse(t *testing.T) {
	require.NoError(t, i18n.Init())
	red := testImage(16, 16, solid(color.NRGBA{R: 255, A: 255}))
	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	require.NoError(t, err)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for i, data := range [][]byte{red, webp} {
		entry, err := writer.Create(fmt.Sprintf("image_%d.png", i))
		require.NoError(t, err)
		_, err = entry.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 2})
	require.Nil(t, apiErr)
	promptTokens := generation.Usage().PromptTokens
	respond := func(t *testing.T, stream, includeUsage bool, body []byte) (*httptest.ResponseRecorder, *relaycommon.RelayInfo, *dto.Usage, *types.NewAPIError) {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Set(common.RequestIdKey, "req-1")
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "nai-diffusion-4-5-full"}, IsStream: stream, ShouldIncludeUsage: includeUsage}
		info.PriceData.UsePrice = true
		info.PriceData.AddOtherRatio("n", 3)
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/zip"}}, Body: io.NopCloser(bytes.NewReader(body))}
		usage, apiErr := chatResponse(c, resp, info, generation)
		return recorder, info, usage, apiErr
	}
	// markdownPNGs reads the Markdown images of content back as PNGs.
	markdownPNGs := func(t *testing.T, content string) [][]byte {
		t.Helper()
		var images [][]byte
		for part := range strings.SplitSeq(content, "\n\n") {
			encoded, ok := strings.CutPrefix(part, "![image](data:image/png;base64,")
			require.True(t, ok, part[:min(len(part), 40)])
			encoded, ok = strings.CutSuffix(encoded, ")")
			require.True(t, ok)
			data, err := base64.StdEncoding.DecodeString(encoded)
			require.NoError(t, err)
			require.Equal(t, formatPNG, SniffImageFormat(data))
			images = append(images, data)
		}
		return images
	}

	t.Run("non-stream: one message with every image", func(t *testing.T) {
		recorder, info, usage, apiErr := respond(t, false, true, archive.Bytes())

		require.Nil(t, apiErr)
		assert.Equal(t, &dto.Usage{PromptTokens: promptTokens, TotalTokens: promptTokens}, usage)
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
		var response struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "chatcmpl-req-1", response.ID)
		assert.Equal(t, "chat.completion", response.Object)
		assert.Equal(t, "nai-diffusion-4-5-full", response.Model)
		require.Len(t, response.Choices, 1)
		assert.Equal(t, "stop", response.Choices[0].FinishReason)
		assert.Equal(t, "assistant", response.Choices[0].Message.Role)
		images := markdownPNGs(t, response.Choices[0].Message.Content)
		require.Len(t, images, 2)
		assert.Equal(t, red, images[0])
		assert.Equal(t, [3]int{promptTokens, 0, promptTokens}, [3]int{response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens})
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "billed per image received")
	})

	for _, includeUsage := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream, include_usage %t", includeUsage), func(t *testing.T) {
			recorder, info, usage, apiErr := respond(t, true, includeUsage, archive.Bytes())

			require.Nil(t, apiErr)
			assert.Equal(t, promptTokens, usage.PromptTokens)
			assert.Zero(t, usage.CompletionTokens)
			assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
			events := streamedEvents(t, recorder.Body.String())
			wantEvents := 6
			if !includeUsage {
				wantEvents = 5
			}
			require.Len(t, events, wantEvents, recorder.Body.String())
			for _, event := range events[:len(events)-1] {
				assert.Equal(t, "chat.completion.chunk", event.data["object"])
				assert.Equal(t, "chatcmpl-req-1", event.data["id"])
				assert.Equal(t, "nai-diffusion-4-5-full", event.data["model"])
			}
			delta := func(i int) map[string]any {
				return events[i].data["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
			}
			assert.Equal(t, map[string]any{"role": "assistant", "content": ""}, delta(0))
			first, second := delta(1)["content"].(string), delta(2)["content"].(string)
			assert.Equal(t, [][]byte{red}, markdownPNGs(t, first), "one whole image per chunk")
			require.True(t, strings.HasPrefix(second, "\n\n"))
			assert.Len(t, markdownPNGs(t, strings.TrimPrefix(second, "\n\n")), 1)
			assert.Equal(t, "stop", events[3].data["choices"].([]any)[0].(map[string]any)["finish_reason"])
			if includeUsage {
				chunkUsage := events[4].data["usage"].(map[string]any)
				assert.Equal(t, float64(promptTokens), chunkUsage["prompt_tokens"])
				assert.Zero(t, chunkUsage["completion_tokens"])
				assert.Empty(t, events[4].data["choices"])
			}
			assert.Equal(t, map[string]any{"[DONE]": true}, events[len(events)-1].data)
			assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
			assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
		})
	}

	t.Run("a 2xx without images is final", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			recorder, info, usage, apiErr := respond(t, stream, true, []byte(`{"statusCode":500,"message":"Error generating image"}`))

			require.NotNil(t, apiErr)
			assert.Nil(t, usage)
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.Equal(t, 3.0, info.PriceData.OtherRatios()["n"], "the failed request is refunded, not settled")
			if stream {
				events := streamedEvents(t, recorder.Body.String())
				require.Len(t, events, 1, "the role chunk went out first")
			} else {
				assert.Empty(t, recorder.Body.String())
			}
		}
	})
}
