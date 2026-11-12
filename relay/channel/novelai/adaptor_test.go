package novelai

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
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
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/draw"
)

// NovelAI answers the account on its image host (api.novelai.net/user/* now
// answers 400 "update to the image URL"), and a NovelAI-compatible forwarder
// answers it itself, so the account API is at the channel's base URL, which is
// NovelAI's image host when empty.
func TestAccountBaseURLIsTheChannelsImageHost(t *testing.T) {
	for baseURL, want := range map[string]string{
		"":                                      "https://image.novelai.net",
		"  ":                                    "https://image.novelai.net",
		"https://image.novelai.net":             "https://image.novelai.net",
		" https://image.novelai.net/ ":          "https://image.novelai.net",
		"http://IMAGE.NovelAI.net.":             "http://IMAGE.NovelAI.net.",
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
	// The model square and /v1/models offer an inpainting model only the
	// native endpoint, and every other model the converted ones as well.
	for _, model := range ModelList {
		endpoints := common.GetEndpointTypesByChannelType(constant.ChannelTypeNovelAI, model)
		assert.Equal(t, inpaintingModels[model], slices.Equal([]constant.EndpointType{constant.EndpointTypeNovelAI}, endpoints), model)
	}
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

	// An official channel (empty base URL) reads its account on NovelAI's
	// image host, not on the retired api.novelai.net. The proxy refuses the
	// tunnel: *.novelai.net is not reachable from the tests.
	t.Run("official channel reads the account on the image host", func(t *testing.T) {
		var tunnels []string
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tunnels = append(tunnels, r.Method+" "+r.Host)
			w.WriteHeader(http.StatusForbidden)
		}))
		defer proxy.Close()

		_, apiErr := FetchSubscription(context.Background(), "", "pst-test", dto.ChannelSettings{Proxy: proxy.URL}, nil)

		require.NotNil(t, apiErr)
		assert.Equal(t, []string{"CONNECT image.novelai.net:443"}, tunnels)
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

// pngHeader is a PNG that ends after its IHDR chunk (truecolor at bitDepth,
// Adam7 when interlace is 1): its header reads, its pixels do not decode.
func pngHeader(width, height int, bitDepth, interlace byte) []byte {
	chunk := []byte("IHDR")
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(width))
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(height))
	chunk = append(chunk, bitDepth, 2, 0, 0, interlace)
	data := binary.BigEndian.AppendUint32([]byte("\x89PNG\r\n\x1a\n"), uint32(len(chunk)-4))
	data = append(data, chunk...)
	return binary.BigEndian.AppendUint32(data, crc32.ChecksumIEEE(chunk))
}

// jpegHeader is a JPEG that ends at its scan: a frame header (sof is SOF0,
// baseline, or SOF2, progressive) with a component for each sampling factor
// (horizontal << 4 | vertical).
func jpegHeader(width, height int, sof byte, sampling ...byte) []byte {
	data := []byte{0xff, 0xd8, 0xff, sof}
	data = binary.BigEndian.AppendUint16(data, uint16(8+3*len(sampling)))
	data = append(data, 8)
	data = binary.BigEndian.AppendUint16(data, uint16(height))
	data = binary.BigEndian.AppendUint16(data, uint16(width))
	data = append(data, byte(len(sampling)))
	for i, factors := range sampling {
		data = append(data, byte(i+1), factors, 0)
	}
	return append(data, 0xff, 0xda, 0, 2)
}

// losslessWebPHeader is a lossless WebP that ends after its VP8L header.
func losslessWebPHeader(width, height int) []byte {
	chunk := []byte{0x2f}
	chunk = binary.LittleEndian.AppendUint32(chunk, uint32(width-1)|uint32(height-1)<<14)
	chunk = append(chunk, 0)
	data := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(4+8+len(chunk)))
	data = append(data, "WEBPVP8L"...)
	data = binary.LittleEndian.AppendUint32(data, 5)
	return append(data, chunk...)
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
// 0.5, and a mask whose right half is transparent; and prompts with a Text:
// block, character fields and V3 mixing, whose preset tags go where the web
// client puts them. Run with -update to rewrite testdata/requests after a
// deliberate change, and review the diff.
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
	for name, prompt := range map[string]string{
		// Before the Text: block, which NovelAI draws as literal text.
		"nai-diffusion-4-5-full-text-block": "1girl, holding sign, Text: OPEN",
		"nai-diffusion-5-full-text-block":   "1girl, holding sign, Text: OPEN",
		// In the first character field only.
		"nai-diffusion-4-5-full-character-fields": "girl | boy",
		// Before each mixed prompt's weight.
		"nai-diffusion-3-mixed-prompts": "cat:0.6 | dog:0.4",
	} {
		model, _, _ := strings.Cut(name, "-text-block")
		model, _, _ = strings.Cut(model, "-character-fields")
		model, _, _ = strings.Cut(model, "-mixed-prompts")
		intent := GenerationIntent{Model: model, Prompt: prompt, N: 1, Extras: extras, Transparent: model == "nai-diffusion-5-full"}
		variants = append(variants, variant{name, intent})
	}

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
// generation (aspect_ratio's preset canvas, or its ratio at about that
// area) and the source's aspect ratio within 1024x1024's area for an edit,
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
		{"aspect_ratio preset", GenerationIntent{Extras: map[string]json.RawMessage{"aspect_ratio": raw(`"3:4"`)}}, 896, 1152},
		{"aspect_ratio without a preset", GenerationIntent{Extras: map[string]json.RawMessage{"aspect_ratio": raw(`"1:2"`)}}, 704, 1408},
		{"edit at the source aspect", GenerationIntent{Image: wide}, 1728, 576},
		// A side held at 64 must not leave the other past Opus's free area,
		// let alone past NovelAI's: an auto size is never rejected.
		{"edit at an extreme aspect stays in the free area", GenerationIntent{Image: testImage(1, 400, solid(color.White))}, 64, 16384},
		{"edit past NovelAI's aspect still gets an auto size", GenerationIntent{Image: testImage(2400, 1, solid(color.White))}, 16384, 64},
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
			if tc.intent.Size == "" {
				assert.LessOrEqual(t, tc.width*tc.height, autoEditPixels, "an auto size is free on Opus")
			}
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
	for _, steps := range []string{"3", "9", "27", "36", "37"} {
		_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-furry-3", Extras: map[string]json.RawMessage{"sampler": raw(`"ddim_v3"`), "steps": raw(steps)}})
		assert.Equal(t, "k_euler_ancestral", parameters["sampler"], "NovelAI's DDIM fails at %s steps", steps)
		assert.Equal(t, "karras", parameters["noise_schedule"])
	}
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

	// OpenRouter's image_config.aspect_ratio stands in for aspect_ratio.
	extras, _, apiErr := ParseExtensions(nil, map[string]json.RawMessage{"image_config": raw(`{"aspect_ratio":"16:9","image_size":"2K"}`)})
	require.Nil(t, apiErr)
	assert.Equal(t, map[string]json.RawMessage{"aspect_ratio": raw(`"16:9"`)}, extras)
	extras, _, apiErr = ParseExtensions(nil, map[string]json.RawMessage{"aspect_ratio": raw(`"3:2"`), "image_config": raw(`{"aspect_ratio":"16:9"}`)})
	require.Nil(t, apiErr)
	assert.Equal(t, map[string]json.RawMessage{"aspect_ratio": raw(`"3:2"`)}, extras)

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

// An OpenAI images request takes NovelAI's keys from its top level or from
// new-api's extra_fields object, the top level first. Edits take one image
// and one mask: a request with more is rejected before any is read, so a
// list of URLs downloads nothing.
func TestImageGenerationIntent(t *testing.T) {
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	fetch := system_setting.GetFetchSetting()
	savedFetch := *fetch
	// The test server is on loopback, which the SSRF guard refuses.
	fetch.EnableSSRFProtection = false
	savedDownloadMB := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 64
	t.Cleanup(func() { *fetch, constant.MaxFileDownloadMB = savedFetch, savedDownloadMB })
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testImage(8, 8, solid(color.White)))
	}))
	t.Cleanup(server.Close)
	intent := func(path, body string) (GenerationIntent, *types.NewAPIError) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		request := &dto.ImageRequest{}
		require.NoError(t, common.UnmarshalBodyReusable(c, request))
		relayMode := relayconstant.RelayModeImagesGenerations
		if path == "/v1/images/edits" {
			relayMode = relayconstant.RelayModeImagesEdits
		}
		intent, _, apiErr := imageGenerationIntent(c, &relaycommon.RelayInfo{RelayMode: relayMode, ChannelMeta: &relaycommon.ChannelMeta{}}, request)
		return intent, apiErr
	}

	got, apiErr := intent("/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"1girl","steps":30,"extra_fields":{"negative_prompt":"bad hands","steps":10,"seed":5}}`)
	require.Nil(t, apiErr)
	assert.Equal(t, map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`), "seed": raw("5"), "steps": raw("30")}, got.Extras)

	url := func(i int) string { return fmt.Sprintf(`{"image_url":"%s/image.png?%d"}`, server.URL, i) }
	for _, tc := range []struct{ name, body, message string }{
		{"images", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","images":[` + url(0) + `,` + url(1) + `,` + url(2) + `]}`, "the request carries 3"},
		{"image and images", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","image":` + url(0) + `,"images":[` + url(1) + `]}`, "the request carries 2"},
		{"masks", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","images":[` + url(0) + `],"mask":[` + url(1) + `,` + url(2) + `]}`, "mask"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			downloads.Store(0)

			_, apiErr := intent("/v1/images/edits", tc.body)

			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Contains(t, apiErr.Error(), tc.message)
			assert.LessOrEqual(t, downloads.Load(), int32(1), "only a lone image is read, the mask list is not")
			if tc.name != "masks" {
				assert.Zero(t, downloads.Load(), "a request with several images downloads none")
			}
		})
	}
	downloads.Store(0)
	got, apiErr = intent("/v1/images/edits", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","images":[`+url(0)+`]}`)
	require.Nil(t, apiErr)
	assert.Equal(t, int32(1), downloads.Load())
	assert.Equal(t, "png", SniffImageFormat(got.Image))
}

// NovelAI does not expand presets, so the relay does as the web client:
// the quality tags after the prompt (after "transparent background" when
// asked for; before a Text: block and in the first character field only, or
// on V3 before each mixed prompt's weight) and the UC preset in front of the
// negative prompt, with "nsfw, "
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

	// The web client's Text: marker: any case, not "text::", at a tag
	// boundary; it ends the base prompt, as does the first single bar. A ||
	// randomizer's bars separate alternatives, not character fields.
	for _, tc := range []struct{ model, prompt, want string }{
		{"nai-diffusion-5-curated", "a shop sign. text: Bakery", "a shop sign, " + qualityV5Standard + ". text: Bakery"},
		{"nai-diffusion-4-5-full", "Text: OPEN", qualityV5Standard + ". Text: OPEN"},
		{"nai-diffusion-4-5-full", "1girl, [Text: hi]", "1girl, " + qualityV5Standard + ". [Text: hi]"},
		{"nai-diffusion-4-5-full", "1girl, ||Text: a|Text: b||", "1girl, " + qualityV5Standard + ". ||Text: a|Text: b||"},
		{"nai-diffusion-4-5-full", "1girl, context: night", "1girl, context: night, " + qualityV5Standard},
		{"nai-diffusion-4-5-full", "1girl, Text::2", "1girl, Text::2, " + qualityV5Standard},
		{"nai-diffusion-4-5-full", "1girl, ||red|blue|| hair | boy", "1girl, ||red|blue|| hair, " + qualityV5Standard + " | boy"},
		{"nai-diffusion-3", "cat:0.2 |frog:-0.5", "cat, " + qualityV3 + ":0.2 |frog, " + qualityV3 + ":-0.5"},
		{"nai-diffusion-3", "cute face :3", "cute face :3, " + qualityV3},
	} {
		input, _ := prompts(GenerationIntent{Model: tc.model, Prompt: tc.prompt})
		assert.Equal(t, tc.want, input, tc.prompt)
	}

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

	// Each canvas pixel reads the mask's pixel nearest its centre: on a
	// 64x64 canvas, a 128x128 mask white in its odd columns is all white.
	oddColumns := testImage(128, 128, func(x, y int) color.Color {
		if x%2 == 1 {
			return color.White
		}
		return color.Black
	})
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: testImage(128, 128, solid(color.White)), Mask: oddColumns, Size: "64x64"})
	mask = decodedBase64Image(t, parameters["mask"])
	assert.Equal(t, uint32(255), maskValue(mask, 0, 0))
	assert.Equal(t, uint32(255), maskValue(mask, 63, 63))

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

	// A mask of another size than the image is stretched over it, so both
	// get the same crop: rows 0-47 of a 128x128 mask over a 256x128 image
	// are canvas rows 0-23 at 128x64, not the rows of the mask's own crop.
	topRows := testImage(128, 128, func(x, y int) color.Color {
		if y < 48 {
			return color.NRGBA{}
		}
		return color.NRGBA{A: 255}
	})
	_, parameters = buildParameters(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: testImage(256, 128, solid(color.White)), Mask: topRows, Size: "128x64"})
	mask = decodedBase64Image(t, parameters["mask"])
	for y := range 64 {
		want := uint32(0)
		if y < 24 {
			want = 255
		}
		assert.Equal(t, want, maskValue(mask, 0, y), "canvas row %d", y)
	}
}

// Image work is bounded by memory, not pixels, and comes last. An image may
// take at most 64 MiB to decode, read from its header: a 16-bit PNG decodes
// at 8 bytes a pixel, an interlaced one at twice its pixels, a progressive
// JPEG keeps 4-byte coefficients for each sample, a lossless WebP a copy of
// its pixels; and it has at most 65535 pixels a side. Headers are checked
// before anything decodes, and the source decodes only once everything else
// is valid, after the mask, so a mask that selects nothing (a free 400)
// costs no source decode. The work of all requests shares imageWork: a
// request reserves the memory its work holds (to the byte), waits while
// other requests hold it, and ends when its client leaves. The resize holds
// about its canvas and the crop's rows, not a buffer of 32 bytes for each
// canvas column and crop row, and a mask's pixels are read without an
// allocation each.
func TestBuildGenerationBoundsImageWork(t *testing.T) {
	require.NoError(t, i18n.Init())
	area := testImage(64, 64, func(x, y int) color.Color {
		if x < 32 {
			return color.NRGBA{}
		}
		return color.NRGBA{A: 255}
	})
	for _, tc := range []struct {
		name              string
		image, mask       []byte
		message, strength string
	}{
		{name: "an 8-bit image past 4096x4096", image: pngHeader(4097, 4096, 8, 0), message: "image must be a PNG, JPEG or WebP image of at most 16777216 pixels"},
		{name: "a 16-bit image past half of it", image: pngHeader(2897, 2897, 16, 0), message: "image must be a PNG, JPEG or WebP image of at most 8388608 pixels"},
		{name: "a 16-bit mask past half of it", image: area, mask: pngHeader(2897, 2897, 16, 0), message: "mask must be a PNG, JPEG or WebP image of at most 8388608 pixels"},
		{name: "an interlaced image past half of it", image: pngHeader(2897, 2897, 8, 1), message: "image must be a PNG, JPEG or WebP image of at most 8388608 pixels"},
		{name: "an image past 65535 pixels a side", image: testImage(65536, 1, solid(color.White)), message: "image must be a PNG, JPEG or WebP image of at most 16777216 pixels and 65535 pixels a side"},
		{name: "a progressive 4:4:4 JPEG past 15 bytes a pixel", image: jpegHeader(2116, 2116, 0xc2, 0x11, 0x11, 0x11), message: "image must be a PNG, JPEG or WebP image of at most 4473924 pixels"},
		{name: "a progressive 4:2:0 JPEG mask past 7.5 bytes a pixel", image: area, mask: jpegHeader(3000, 3000, 0xc2, 0x22, 0x11, 0x11), message: "mask must be a PNG, JPEG or WebP image of at most 8947848 pixels"},
		// Its header reads; it fails to decode.
		{name: "a baseline 4:2:0 JPEG of that size", image: jpegHeader(3000, 3000, 0xc0, 0x22, 0x11, 0x11), message: "image must be a PNG, JPEG or WebP image of at most 16777216 pixels"},
		{name: "a lossless WebP past half of it", image: losslessWebPHeader(2897, 2897), message: "image must be a PNG, JPEG or WebP image of at most 8388608 pixels"},
		// pngHeader(64, 64, 8, 0) reads as an image but does not decode.
		{name: "the mask before the source", image: pngHeader(64, 64, 8, 0), mask: testImage(64, 64, solid(color.Black)), message: "mask selects no area"},
		{name: "the strength before the source", image: pngHeader(64, 64, 8, 0), mask: area, strength: "5", message: "strength must be between"},
		{name: "the source last", image: pngHeader(64, 64, 8, 0), mask: area, message: "image must be a PNG, JPEG or WebP image of at most 16777216 pixels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := GenerationIntent{Model: "nai-diffusion-4-5-full", Image: tc.image, Mask: tc.mask}
			if tc.strength != "" {
				intent.Extras = map[string]json.RawMessage{"strength": raw(tc.strength)}
			}
			assert.Contains(t, buildRejected(t, intent), tc.message)
		})
	}
	edit := GenerationIntent{Model: "nai-diffusion-4-5-full", N: 1, Image: area, Mask: area, Size: "64x64"}
	_, parameters := buildParameters(t, edit)
	assert.NotEmpty(t, parameters["image"])

	// Every build gave back the memory it took. While other work holds all
	// of it, a header is still rejected and a generation without images is
	// still built at once; an edit waits, and ends when its client leaves.
	require.True(t, imageWork.TryAcquire(imageWorkBudget), "builds gave back the memory they took")
	left, leave := context.WithCancel(context.Background())
	leave()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(left, http.MethodPost, "/v1/images/edits", nil)
	assert.Contains(t, buildRejected(t, GenerationIntent{Model: "nai-diffusion-4-5-full", Image: pngHeader(4097, 4096, 8, 0)}), "at most 16777216 pixels")
	generation, apiErr := BuildGeneration(c, GenerationIntent{Model: "nai-diffusion-4-5-full", N: 1, Prompt: "x"})
	assert.Nil(t, apiErr)
	assert.NotNil(t, generation)
	generation, apiErr = BuildGeneration(c, edit)
	imageWork.Release(imageWorkBudget)
	assert.Nil(t, generation)
	require.NotNil(t, apiErr)
	assert.Contains(t, apiErr.Error(), context.Canceled.Error())
	assert.True(t, types.IsSkipRetryError(apiErr), "its client left")

	// An edit reserves the memory its work holds and runs once that much is
	// free: the decoded image and mask at 4 bytes a pixel, the resize's
	// columns (as wide as the canvas and as high as the crop of the image),
	// and three RGBA canvases. With a byte less free it waits (TryAcquire(0)
	// fails only while a request waits); that byte lets it run. The 64x64
	// mask is 16384 bytes in every case; the canvas and the crop differ.
	for _, tc := range []struct {
		name     string
		image    []byte
		size     string
		reserved int64
	}{
		// 65536 + 16384 + 4*64*(128 crop rows + 3*64) = 163840.
		{name: "square image on a square canvas", image: testImage(128, 128, solid(color.White)), size: "64x64", reserved: 163840},
		// The crop of a square image for a 64x128 canvas is 64 wide and 128
		// high: 65536 + 16384 + 4*64*(128 + 3*128) = 212992.
		{name: "portrait canvas", image: testImage(128, 128, solid(color.White)), size: "64x128", reserved: 212992},
		// For a 128x64 canvas it is 128 wide and 64 high:
		// 65536 + 16384 + 4*128*(64 + 3*64) = 212992.
		{name: "landscape canvas", image: testImage(128, 128, solid(color.White)), size: "128x64", reserved: 212992},
		// A 64x256 image crops to its 64x64 centre: the columns cover the
		// crop's 64 rows, not the image's 256. 65536 + 16384 + 4*64*(64 +
		// 3*64) = 147456.
		{name: "tall image on a square canvas", image: testImage(64, 256, solid(color.White)), size: "64x64", reserved: 147456},
	} {
		t.Run("reserves its work: "+tc.name, func(t *testing.T) {
			held := imageWorkBudget - tc.reserved + 1
			require.True(t, imageWork.TryAcquire(held))
			waiting, stop := context.WithCancel(context.Background())
			defer stop()
			client, _ := gin.CreateTestContext(httptest.NewRecorder())
			client.Request = httptest.NewRequestWithContext(waiting, http.MethodPost, "/v1/images/edits", nil)
			sized := GenerationIntent{Model: "nai-diffusion-4-5-full", N: 1, Image: tc.image, Mask: area, Size: tc.size}
			built := make(chan *types.NewAPIError, 1)
			go func() {
				_, buildErr := BuildGeneration(client, sized)
				built <- buildErr
			}()
			for imageWork.TryAcquire(0) {
				select {
				case buildErr := <-built:
					imageWork.Release(held)
					require.Failf(t, "the edit ran with a byte less than it reserves free", "error: %v", buildErr)
				default:
					runtime.Gosched()
				}
			}
			imageWork.Release(1)
			ran := imageWork.TryAcquire(0)
			if !ran {
				stop()
			}
			buildErr := <-built
			imageWork.Release(held - 1)
			require.True(t, ran, "a byte more let the edit run")
			assert.Nil(t, buildErr)
			require.True(t, imageWork.TryAcquire(imageWorkBudget), "the edit gave back the memory it took")
			imageWork.Release(imageWorkBudget)
		})
	}

	// Red | green | blue thirds of 6144x2048: the 2048x2048 green centre of
	// a square canvas.
	thirds := image.NewRGBA(image.Rect(0, 0, 6144, 2048))
	for i := 0; i < len(thirds.Pix); i += 4 {
		// R, G or B by third, opaque.
		thirds.Pix[i+i/4%6144/2048] = 255
		thirds.Pix[i+3] = 255
	}
	resized := coverResize(thirds, coverCrop(thirds.Bounds(), 64, 64), 64, 64)
	for _, point := range []image.Point{{0, 0}, {63, 63}, {32, 32}} {
		assert.Equal(t, color.RGBA{G: 255, A: 255}, resized.RGBAAt(point.X, point.Y), "the centre at %v", point)
	}

	// The bands of the resize give what one CatmullRom call gives, to the
	// rounding of the 8-bit columns between its passes: no seams. (The
	// columns also clip the kernel's overshoot past black or white, which
	// one call keeps until the end; this pattern stays clear of both.)
	pattern := image.NewRGBA(image.Rect(0, 0, 1536, 1024))
	for i := 0; i < len(pattern.Pix); i += 4 {
		x, y := i/4%1536, i/4/1536
		pattern.Pix[i], pattern.Pix[i+1], pattern.Pix[i+2], pattern.Pix[i+3] = byte(64+x*3%128), byte(64+y*5%128), byte(64+(x^y)*7%128), 255
	}
	crop := coverCrop(pattern.Bounds(), 1024, 768)
	whole := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	draw.CatmullRom.Scale(whole, whole.Bounds(), pattern, crop, draw.Src, nil)
	banded := coverResize(pattern, crop, 1024, 768)
	largest := 0
	for i := range whole.Pix {
		largest = max(largest, int(whole.Pix[i])-int(banded.Pix[i]), int(banded.Pix[i])-int(whole.Pix[i]))
	}
	assert.LessOrEqual(t, largest, 1, "the largest difference of a channel")

	// A 4096x4096 image on a 1728x1728 canvas: its columns (1728x4096) and the
	// canvas, 39 MiB; one call would hold 216 MiB more. (Under the race
	// detector sync.Pool drops a quarter of the band buffers it is given
	// back, which are then allocated again.)
	large := image.NewRGBA(image.Rect(0, 0, 4096, 4096))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	coverResize(large, large.Bounds(), 1728, 1728)
	runtime.ReadMemStats(&after)
	build, _ := debug.ReadBuildInfo()
	if build == nil || !slices.Contains(build.Settings, debug.BuildSetting{Key: "-race", Value: "true"}) {
		assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4*1728*(4096+1728)+4<<20), "allocated for a 1728x1728 canvas")
	}

	// A 16-bit black-and-white mask: 65,536 pixels read in two passes.
	halves := image.NewNRGBA64(image.Rect(0, 0, 256, 256))
	for y := range 256 {
		for x := range 256 {
			halves.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x / 128 * 0xffff), G: uint16(x / 128 * 0xffff), B: uint16(x / 128 * 0xffff), A: 0xffff})
		}
	}
	var encoded string
	allocations := testing.AllocsPerRun(1, func() {
		var ok bool
		encoded, ok = novelAIMask(halves, halves.Bounds(), 256, 256)
		require.True(t, ok)
	})
	assert.Less(t, allocations, float64(1_000), "not one a pixel")
	mask := decodedBase64Image(t, encoded)
	r, _, _, _ := mask.At(0, 0).RGBA()
	assert.Zero(t, r, "black keeps")
	r, _, _, _ = mask.At(255, 0).RGBA()
	assert.Equal(t, uint32(0xffff), r, "white repaints")
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

// resetGenerationQueue restores the generation queue's globals after a test
// that uses them, which must have released every key it took.
func resetGenerationQueue(t *testing.T) {
	t.Helper()
	require.NoError(t, i18n.Init())
	savedMaxWait, savedCooldown := generationMaxWait, generationCooldown
	t.Cleanup(func() {
		generationLinesMu.Lock()
		assert.Empty(t, generationLines, "every key the test took was released")
		generationRefused = map[[sha256.Size]byte]time.Time{}
		generationStopped = false
		generationLinesMu.Unlock()
		generationMaxWait, generationCooldown = savedMaxWait, savedCooldown
	})
}

// queuedRequest is a client request that lasts as long as ctx.
func queuedRequest(ctx context.Context) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/images/generations", nil)
	return c
}

// officialKey is a request for key on a single-key NovelAI channel on
// NovelAI's host with the given queue limit per key (nil: the default).
func officialKey(key string, limit *int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "https://image.novelai.net", ApiKey: key,
		ChannelOtherSettings: dto.ChannelOtherSettings{NovelAIQueueLimit: limit},
	}}
}

// waitingFor is how many requests wait for key, or -1 while it is free.
func waitingFor(key string) int {
	generationLinesMu.Lock()
	defer generationLinesMu.Unlock()
	line, held := generationLines[sha256.Sum256([]byte(key))]
	if !held {
		return -1
	}
	return line.waiting.Len()
}

// keyIsFree takes key and gives it back at once when nobody holds it, and
// tells whether it could; it never waits.
func keyIsFree(key string) bool {
	ticket, err := enterGenerationLine([]generationKey{{key: key}}, 0, "", 0)
	if err != nil {
		return false
	}
	releaseGenerationKey(ticket.id)
	return true
}

// grantedKey is what a waiter got when its wait ended.
type grantedKey struct {
	position int
	release  func()
	err      error
}

// awaitGrant is the next waiter's grant, failing the test after 5 s.
func awaitGrant(t *testing.T, grants <-chan grantedKey) grantedKey {
	t.Helper()
	select {
	case grant := <-grants:
		return grant
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no waiter got the key")
		return grantedKey{}
	}
}

// rejectedAtOnce is the error of a hold that must fail without waiting,
// failing the test after 5 s.
func rejectedAtOnce(t *testing.T, info *relaycommon.RelayInfo) *types.NewAPIError {
	t.Helper()
	rejected := make(chan *types.NewAPIError, 1)
	go func() {
		release, apiErr := holdGeneration(queuedRequest(context.Background()), info)
		if apiErr == nil {
			release()
		}
		rejected <- apiErr
	}()
	select {
	case apiErr := <-rejected:
		require.NotNil(t, apiErr, "the hold took the key")
		return apiErr
	case <-time.After(5 * time.Second):
		require.FailNow(t, "a request past the limit waited")
		return nil
	}
}

// On NovelAI's own host one key generates one request at a time, and the
// requests that find it busy get it in the order they came: a release hands
// the key to the head of its line, so a newcomer never takes it first. A
// client that leaves stops waiting; a closed response body and the end of the
// client's request release the key. Other keys have their own line, and
// other hosts are not queued.
func TestGenerationQueueServesAKeyInArrivalOrder(t *testing.T) {
	resetGenerationQueue(t)
	key := []generationKey{{key: "pst-fifo"}}
	holder, err := enterGenerationLine(key, 0, "", 10)
	require.NoError(t, err)
	assert.Zero(t, holder.position, "the key was free")
	release, err := holder.wait(context.Background())
	require.NoError(t, err)

	grants := make(chan grantedKey)
	for want := 1; want <= 3; want++ {
		ticket, err := enterGenerationLine(key, 0, "", 10)
		require.NoError(t, err)
		require.Equal(t, want, ticket.position)
		go func() {
			release, err := ticket.wait(context.Background())
			grants <- grantedKey{ticket.position, release, err}
		}()
	}
	other, err := enterGenerationLine([]generationKey{{key: "pst-fifo-other"}}, 0, "", 10)
	require.NoError(t, err)
	assert.Nil(t, other.waiter, "another key has its own line")
	releaseGenerationKey(other.id)

	for want := 1; want <= 3; want++ {
		release()
		_, err := enterGenerationLine(key, 0, "", 0)
		assert.ErrorIs(t, err, errGenerationQueueFull, "the key went to the head of its line, not to whoever comes next")
		grant := awaitGrant(t, grants)
		require.NoError(t, grant.err)
		assert.Equal(t, want, grant.position, "first come, first served")
		release = grant.release
	}
	release()
	assert.Equal(t, -1, waitingFor("pst-fifo"), "a key nobody waits for is free")

	official := officialKey("pst-fifo", common.GetPointer(0))
	release, apiErr := holdGeneration(queuedRequest(context.Background()), official)
	require.Nil(t, apiErr)
	forwarder := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://10.0.0.2:3585", ApiKey: "pst-fifo"}}
	releaseForwarder, apiErr := holdGeneration(queuedRequest(context.Background()), forwarder)
	require.Nil(t, apiErr, "a forwarder queues for itself")
	releaseForwarder()

	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan *types.NewAPIError)
	go func() {
		_, apiErr := holdGeneration(queuedRequest(ctx), officialKey("pst-fifo", nil))
		left <- apiErr
	}()
	assert.Eventually(t, func() bool { return waitingFor("pst-fifo") == 1 }, 5*time.Second, time.Millisecond)
	cancel()
	apiErr = <-left
	require.NotNil(t, apiErr, "a client that left stops waiting")
	assert.Equal(t, context.Canceled.Error(), apiErr.Error())
	assert.Equal(t, 0, waitingFor("pst-fifo"), "and leaves the line")

	// A response body closing releases the key for the next request, also
	// while NovelAI is still answering.
	answering, _ := io.Pipe()
	body := newGenerationBody(answering, release)
	require.NoError(t, body.Close())
	require.NoError(t, body.Close())
	next, apiErr := holdGeneration(queuedRequest(context.Background()), official)
	require.Nil(t, apiErr)

	// The end of the client's request releases a key nothing closed.
	ended, end := context.WithCancel(context.Background())
	next()
	_, apiErr = holdGeneration(queuedRequest(ended), official)
	require.Nil(t, apiErr)
	end()
	assert.Eventually(t, func() bool { return keyIsFree("pst-fifo") }, 5*time.Second, time.Millisecond)
}

// The channel's queue limit per key is how many requests may wait for a busy
// key, the generating one aside: unset or negative is 10, 0 is no waiting,
// and a stored value above 100 acts as 100. One more fails at once, without
// waiting for anyone, with a 429 another channel may take. The limit of the
// request's channel applies to it, so an edited limit applies to new
// arrivals and leaves the requests already waiting in their place.
func TestGenerationQueueLimit(t *testing.T) {
	resetGenerationQueue(t)
	for _, tc := range []struct {
		name    string
		limit   *int
		waiting int
	}{
		{"unset", nil, 10},
		{"negative", common.GetPointer(-1), 10},
		{"no waiting", common.GetPointer(0), 0},
		{"three", common.GetPointer(3), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "pst-limit-" + tc.name
			info := officialKey(key, tc.limit)
			release, apiErr := holdGeneration(queuedRequest(context.Background()), info)
			require.Nil(t, apiErr, "a free key is taken whatever the limit")
			var waiters sync.WaitGroup
			cancels := make([]context.CancelFunc, 0, tc.waiting)
			for n := 1; n <= tc.waiting; n++ {
				ctx, cancel := context.WithCancel(context.Background())
				cancels = append(cancels, cancel)
				waiters.Go(func() {
					_, apiErr := holdGeneration(queuedRequest(ctx), officialKey(key, tc.limit))
					if assert.NotNil(t, apiErr) {
						assert.Equal(t, context.Canceled.Error(), apiErr.Error())
					}
				})
				require.Eventually(t, func() bool { return waitingFor(key) == n }, 5*time.Second, time.Millisecond)
			}

			apiErr = rejectedAtOnce(t, officialKey(key, tc.limit))
			assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCodeNovelAIKeyBusy, apiErr.GetErrorCode())
			assert.False(t, types.IsSkipRetryError(apiErr), "another channel may take it")
			assert.Equal(t, i18n.Translate("", i18n.MsgNovelAIQueueFull), apiErr.Error())
			assert.Equal(t, tc.waiting, waitingFor(key))

			for _, cancel := range cancels {
				cancel()
			}
			waiters.Wait()
			release()
		})
	}
	assert.Equal(t, 100, (&dto.ChannelOtherSettings{NovelAIQueueLimit: common.GetPointer(5000)}).EffectiveNovelAIQueueLimit())

	t.Run("an edited limit applies to new arrivals", func(t *testing.T) {
		const key = "pst-limit-edited"
		release, apiErr := holdGeneration(queuedRequest(context.Background()), officialKey(key, common.GetPointer(5)))
		require.Nil(t, apiErr)
		grants := make(chan grantedKey)
		for n := 1; n <= 3; n++ {
			go func() {
				release, apiErr := holdGeneration(queuedRequest(context.Background()), officialKey(key, common.GetPointer(5)))
				grant := grantedKey{position: n, release: release}
				if apiErr != nil {
					grant.err = apiErr
				}
				grants <- grant
			}()
			require.Eventually(t, func() bool { return waitingFor(key) == n }, 5*time.Second, time.Millisecond)
		}
		apiErr = rejectedAtOnce(t, officialKey(key, common.GetPointer(2)))
		assert.Equal(t, types.ErrorCodeNovelAIKeyBusy, apiErr.GetErrorCode(), "three wait, more than the new limit")
		for n := 1; n <= 3; n++ {
			release()
			grant := awaitGrant(t, grants)
			require.NoError(t, grant.err)
			assert.Equal(t, n, grant.position, "the waiters keep their place")
			release = grant.release
		}
		release()
	})
}

// A request that stops waiting leaves its line: it gets its client's error,
// and the key goes to the next in line. A turn that comes as the client
// leaves passes the key on, so a turn is never lost and the key is never
// held twice. A wait ends after generationMaxWait with a retryable 429. At
// shutdown every waiting request, and every later one, gets a 503 that is
// final and never reaches NovelAI, while the request generating finishes.
func TestGenerationQueueCancellation(t *testing.T) {
	resetGenerationQueue(t)

	t.Run("a waiter that leaves", func(t *testing.T) {
		const key = "pst-cancel-leave"
		release, apiErr := holdGeneration(queuedRequest(context.Background()), officialKey(key, nil))
		require.Nil(t, apiErr)
		grants := make(chan grantedKey)
		leave, cancel := context.WithCancel(context.Background())
		for n := 1; n <= 3; n++ {
			ctx := context.Background()
			if n == 2 {
				ctx = leave
			}
			go func() {
				release, apiErr := holdGeneration(queuedRequest(ctx), officialKey(key, nil))
				grant := grantedKey{position: n, release: release}
				if apiErr != nil {
					grant.err = apiErr
				}
				grants <- grant
			}()
			require.Eventually(t, func() bool { return waitingFor(key) == n }, 5*time.Second, time.Millisecond)
		}
		cancel()
		left := awaitGrant(t, grants)
		assert.Equal(t, 2, left.position)
		assert.EqualError(t, left.err, context.Canceled.Error())
		assert.Equal(t, 2, waitingFor(key), "it left the line")
		for _, want := range []int{1, 3} {
			release()
			grant := awaitGrant(t, grants)
			require.NoError(t, grant.err)
			assert.Equal(t, want, grant.position)
			release = grant.release
		}
		release()
		assert.Equal(t, -1, waitingFor(key))
	})

	// The turn and the cancellation are both there before the waiter looks,
	// so its select takes either: over 20 rounds it takes the cancellation
	// in some, and must then pass the key on.
	t.Run("a turn that comes as the client leaves", func(t *testing.T) {
		key := []generationKey{{key: "pst-cancel-race"}}
		left := 0
		for range 20 {
			holder, err := enterGenerationLine(key, 0, "", 10)
			require.NoError(t, err)
			release, err := holder.wait(context.Background())
			require.NoError(t, err)
			waiter, err := enterGenerationLine(key, 0, "", 10)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			release()
			cancel()
			granted, err := waiter.wait(ctx)
			if err != nil {
				assert.ErrorIs(t, err, context.Canceled)
				left++
			} else {
				granted()
			}
			require.True(t, keyIsFree("pst-cancel-race"), "the key is free once both are done")
		}
		assert.Positive(t, left, "the cancellation won some rounds")
	})

	t.Run("a wait past generationMaxWait", func(t *testing.T) {
		generationMaxWait = 20 * time.Millisecond
		t.Cleanup(func() { generationMaxWait = 30 * time.Minute })
		const key = "pst-cancel-timeout"
		release, apiErr := holdGeneration(queuedRequest(context.Background()), officialKey(key, nil))
		require.Nil(t, apiErr)
		_, apiErr = holdGeneration(queuedRequest(context.Background()), officialKey(key, nil))
		require.NotNil(t, apiErr)
		assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
		assert.Equal(t, types.ErrorCodeNovelAIKeyBusy, apiErr.GetErrorCode())
		assert.False(t, types.IsSkipRetryError(apiErr))
		assert.Equal(t, i18n.Translate("", i18n.MsgNovelAIQueueTimeout, map[string]any{"Minutes": 0}), apiErr.Error())
		assert.Equal(t, 0, waitingFor(key), "it left the line")
		release()
		assert.Equal(t, -1, waitingFor(key))
	})

	t.Run("shutdown", func(t *testing.T) {
		t.Cleanup(func() {
			generationLinesMu.Lock()
			generationStopped = false
			generationLinesMu.Unlock()
		})
		const key = "pst-cancel-stop"
		release, apiErr := holdGeneration(queuedRequest(context.Background()), officialKey(key, nil))
		require.Nil(t, apiErr)
		grants := make(chan grantedKey)
		for n := 1; n <= 2; n++ {
			go func() {
				release, apiErr := holdGeneration(queuedRequest(context.Background()), officialKey(key, nil))
				grant := grantedKey{position: n, release: release}
				if apiErr != nil {
					grant.err = apiErr
				}
				grants <- grant
			}()
			require.Eventually(t, func() bool { return waitingFor(key) == n }, 5*time.Second, time.Millisecond)
		}

		StopGenerationQueues()

		stoppedMessage := i18n.Translate("", i18n.MsgNovelAIQueueStopped)
		for range 2 {
			grant := awaitGrant(t, grants)
			var apiErr *types.NewAPIError
			require.ErrorAs(t, grant.err, &apiErr)
			assert.Nil(t, grant.release)
			assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCodeNovelAIKeyBusy, apiErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(apiErr), "the server that would retry it is stopping")
			assert.Equal(t, stoppedMessage, apiErr.Error())
		}
		_, apiErr = holdGeneration(queuedRequest(context.Background()), officialKey("pst-cancel-stop-free", nil))
		require.NotNil(t, apiErr, "a free key takes no new generation either")
		assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
		assert.Equal(t, stoppedMessage, apiErr.Error())
		release()
		assert.Equal(t, -1, waitingFor(key), "the generating request finished, and nothing was granted")
	})
}

// occupy holds key for another request and queues waiting more behind it,
// and returns what gives them all up again.
func occupy(t *testing.T, key string, waiting int) func() {
	t.Helper()
	holder, err := enterGenerationLine([]generationKey{{key: key}}, 0, "", dto.NovelAIQueueLimitMax)
	require.NoError(t, err)
	require.Nil(t, holder.waiter, "%s was free", key)
	waiters := make([]*generationTicket, waiting)
	for i := range waiters {
		waiters[i], err = enterGenerationLine([]generationKey{{key: key}}, 0, "", dto.NovelAIQueueLimitMax)
		require.NoError(t, err)
	}
	return func() {
		left, leave := context.WithCancel(context.Background())
		leave()
		for _, waiter := range waiters {
			_, err := waiter.wait(left)
			require.ErrorIs(t, err, context.Canceled)
		}
		releaseGenerationKey(holder.id)
	}
}

// A multi-key channel sends a request to its least loaded key (holding and
// waiting, across every channel with the same key) that has room in its line,
// and to a key NovelAI refused (401, 402, 403 or 429) only when no other has
// room, for generationCooldown. Ties go to the distributor's pick, then to
// the next tie after it under polling, a random tie under random, and the
// first tie otherwise. A request fails only when every key's line is full.
func TestGenerationQueuePicksTheLeastLoadedKey(t *testing.T) {
	resetGenerationQueue(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	answer := func(key string, status int) {
		// From another channel with the same key.
		noteGenerationAnswer(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7, ApiKey: key}}, status)
	}
	for _, tc := range []struct {
		name      string
		busy      map[string]int // key → requests waiting behind its holder
		answers   []string       // "key status", in order
		cooldown  time.Duration
		preferred int
		mode      constant.MultiKeyMode
		limit     int
		chosen    []string // the keys the choice may be
		queued    bool
		full      bool
	}{
		{name: "all idle: the distributor's pick", preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"b"}},
		{name: "polling passes over a busy pick", busy: map[string]int{"a": 0}, preferred: 0, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"b"}},
		{name: "polling takes the next tie after the pick", busy: map[string]int{"b": 0}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"c"}},
		{name: "polling wraps around", busy: map[string]int{"c": 0}, preferred: 2, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"a"}},
		{name: "random takes a tie", busy: map[string]int{"a": 0}, preferred: 0, mode: constant.MultiKeyModeRandom, limit: 1, chosen: []string{"b", "c"}},
		{name: "any other mode takes the first tie", busy: map[string]int{"a": 0}, preferred: -1, limit: 1, chosen: []string{"b"}},
		{name: "the least loaded key with room", busy: map[string]int{"a": 1, "b": 0, "c": 2}, preferred: 0, mode: constant.MultiKeyModePolling, limit: 3, chosen: []string{"b"}, queued: true},
		{name: "a key shared with another channel counts its load", busy: map[string]int{"a": 0, "b": 1, "c": 1}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 3, chosen: []string{"a"}, queued: true},
		{name: "every line full", busy: map[string]int{"a": 1, "b": 1, "c": 1}, preferred: 0, mode: constant.MultiKeyModePolling, limit: 1, full: true},
		{name: "a refused key waits behind the others", busy: map[string]int{"a": 0, "c": 0}, answers: []string{"b 402"}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"c"}, queued: true},
		{name: "a refused key takes the overflow", busy: map[string]int{"a": 1, "c": 1}, answers: []string{"b 429"}, preferred: 0, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"b"}},
		{name: "a refusal of every key with room", busy: map[string]int{"c": 1}, answers: []string{"a 401", "b 403"}, preferred: -1, limit: 1, chosen: []string{"a"}},
		{name: "a success clears a refusal", busy: map[string]int{"a": 0, "c": 0}, answers: []string{"b 402", "b 201"}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"b"}},
		{name: "a refusal ends after generationCooldown", busy: map[string]int{"a": 0, "c": 0}, answers: []string{"b 402"}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"b"}},
		{name: "other answers leave a refusal", busy: map[string]int{"a": 0, "c": 0}, answers: []string{"b 402", "b 400", "b 500"}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"c"}, queued: true},
		{name: "other answers refuse nothing", busy: map[string]int{"a": 0, "c": 0}, answers: []string{"b 400", "b 500"}, preferred: 1, mode: constant.MultiKeyModePolling, limit: 1, chosen: []string{"b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generationCooldown = time.Minute
			if tc.name == "a refusal ends after generationCooldown" {
				generationCooldown = 0
			}
			prefix := "pst-pick-" + strings.ReplaceAll(tc.name, " ", "-") + "-"
			for key, waiting := range tc.busy {
				t.Cleanup(occupy(t, prefix+key, waiting))
			}
			for _, answered := range tc.answers {
				key, status, _ := strings.Cut(answered, " ")
				code, err := strconv.Atoi(status)
				require.NoError(t, err)
				answer(prefix+key, code)
			}
			keys := []generationKey{{0, prefix + "a"}, {1, prefix + "b"}, {2, prefix + "c"}}

			ticket, err := enterGenerationLine(keys, tc.preferred, tc.mode, tc.limit)

			if tc.full {
				require.ErrorIs(t, err, errGenerationQueueFull)
				return
			}
			require.NoError(t, err)
			chosen := strings.TrimPrefix(ticket.key.key, prefix)
			assert.Contains(t, tc.chosen, chosen)
			assert.Equal(t, int(chosen[0]-'a'), ticket.key.index)
			assert.Equal(t, tc.queued, ticket.waiter != nil)
			if ticket.waiter != nil {
				left, leave := context.WithCancel(context.Background())
				leave()
				_, err := ticket.wait(left)
				require.ErrorIs(t, err, context.Canceled)
				return
			}
			releaseGenerationKey(ticket.id)
		})
	}
}

// Requests arriving at once see each other's choice: with two keys and a
// limit of 10, exactly two take a key, ten wait for each, and the other 18
// fail, and every waiter gets its key exactly once.
func TestGenerationQueueUnderConcurrentArrivals(t *testing.T) {
	resetGenerationQueue(t)
	keys := []generationKey{{0, "pst-arrivals-a"}, {1, "pst-arrivals-b"}}
	start := make(chan struct{})
	tickets := make(chan *generationTicket, 40)
	var rejected atomic.Int32
	var arrivals sync.WaitGroup
	for range 40 {
		arrivals.Go(func() {
			<-start
			ticket, err := enterGenerationLine(keys, -1, constant.MultiKeyModeRandom, 10)
			if err != nil {
				assert.ErrorIs(t, err, errGenerationQueueFull)
				rejected.Add(1)
				return
			}
			tickets <- ticket
		})
	}
	close(start)
	arrivals.Wait()
	close(tickets)

	holders, waiting := 0, map[string]int{}
	var served sync.WaitGroup
	var granted atomic.Int32
	for ticket := range tickets {
		if ticket.waiter == nil {
			holders++
		} else {
			waiting[ticket.key.key]++
		}
		served.Go(func() {
			release, err := ticket.wait(context.Background())
			assert.NoError(t, err)
			granted.Add(1)
			release()
		})
	}
	assert.Equal(t, 2, holders)
	assert.Equal(t, map[string]int{"pst-arrivals-a": 10, "pst-arrivals-b": 10}, waiting)
	assert.Equal(t, int32(18), rejected.Load())
	done := make(chan struct{})
	go func() {
		served.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "a waiter never got its key")
	}
	assert.Equal(t, int32(22), granted.Load())
}

// A NovelAI channel on NovelAI's image host holds its key from the send
// until NovelAI's answer ended or the response body closes, on the native
// routes as on the converted ones: with no waiting allowed a second
// generation meanwhile gets the retryable 429, and with room in the line it
// reaches NovelAI only once the first answer ended. NovelAI's refusals of a
// key mark it, a success clears the mark, and other answers leave it. The
// fake proxy plays NovelAI's host, which the test cannot reach; it answers
// the first generation until finish closes.
func TestAdaptorSerializesGenerationsOnNovelAIsHost(t *testing.T) {
	resetGenerationQueue(t)
	service.InitHttpClient()
	var requests atomic.Int32
	finish := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "http://image.novelai.net/ai/generate-image", r.URL.String())
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK\x03\x04"))
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
	}))
	defer proxy.Close()
	send := func(limit int) (*http.Response, error) {
		info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeNovelAIGenerateImage, ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://image.novelai.net", ApiKey: "pst-serialized",
			ChannelSetting:       dto.ChannelSettings{Proxy: proxy.URL},
			ChannelOtherSettings: dto.ChannelOtherSettings{NovelAIQueueLimit: &limit},
		}}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", strings.NewReader("{}"))
		resp, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(`{"input":"a cat"}`))
		if err != nil {
			return nil, err
		}
		return resp.(*http.Response), nil
	}

	first, err := send(0)
	require.NoError(t, err)
	_, err = send(0)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeNovelAIKeyBusy, apiErr.GetErrorCode())
	assert.Equal(t, int32(1), requests.Load(), "the second generation never reached NovelAI")

	require.NoError(t, first.Body.Close())
	second, err := send(0)
	require.NoError(t, err, "a closed response frees the key")
	_, err = send(0)
	require.ErrorAs(t, err, &apiErr, "the second generation holds the key while NovelAI answers it")

	type sent struct {
		resp *http.Response
		err  error
	}
	waited := make(chan sent)
	go func() {
		resp, err := send(1)
		waited <- sent{resp, err}
	}()
	require.Eventually(t, func() bool { return waitingFor("pst-serialized") == 1 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, int32(2), requests.Load(), "a waiting generation has not reached NovelAI")
	close(finish)
	var third sent
	select {
	case third = <-waited:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "an ended answer did not free the key")
	}
	require.NoError(t, third.err, "an ended answer frees the key, though nothing read it")
	require.NoError(t, second.Body.Close())
	require.NoError(t, third.resp.Body.Close())
	assert.Equal(t, int32(3), requests.Load())

	t.Run("NovelAI's answers mark and clear a refused key", func(t *testing.T) {
		statuses := make(chan int, 4)
		refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(<-statuses)
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(refusing.Close)
		id := sha256.Sum256([]byte("pst-refused"))
		marked := func() bool {
			generationLinesMu.Lock()
			defer generationLinesMu.Unlock()
			_, refused := generationRefused[id]
			return refused
		}
		for _, answer := range []struct {
			status int
			marked bool
		}{{http.StatusPaymentRequired, true}, {http.StatusCreated, false}, {http.StatusPaymentRequired, true}, {http.StatusInternalServerError, true}} {
			statuses <- answer.status
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeNovelAIGenerateImage, ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://image.novelai.net", ApiKey: "pst-refused",
				ChannelSetting: dto.ChannelSettings{Proxy: refusing.URL},
			}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", strings.NewReader("{}"))
			resp, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(`{"input":"a cat"}`))
			require.NoError(t, err)
			assert.Equal(t, answer.status, resp.(*http.Response).StatusCode)
			assert.Equal(t, answer.marked, marked(), "after %d", answer.status)
			require.NoError(t, resp.(*http.Response).Body.Close())
		}
	})
}

// stalledClient is a client that never reads: every write blocks until
// unblock is closed, and the first one closes entered.
type stalledClient struct {
	gin.ResponseWriter
	entered, unblock chan struct{}
	once             sync.Once
}

func (w *stalledClient) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.unblock
	return w.ResponseWriter.Write(data)
}

func (w *stalledClient) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

// endTracker records that NovelAI's answer was read to its end.
type endTracker struct {
	io.Reader
	ended atomic.Bool
}

func (r *endTracker) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.ended.Store(true)
	}
	return n, err
}

// NovelAI is free for the key's next generation once its answer has
// arrived, whatever the relay still has to write to its client: on every
// converted and native route, a client that never reads holds no slot once
// NovelAI's answer, larger than the relay's own buffers, ended. What waits
// for the relay is bounded: past generationReadAhead, NovelAI's answer is
// read no further until the relay takes some, and closing the body ends a
// read that waits on NovelAI. An error answer is read as it comes back, at
// most 1 MiB, and closed even when it breaks off, so the retry of the
// request on the same key does not wait for its own slot.
func TestGenerationSlotFreesOnceNovelAIAnswered(t *testing.T) {
	resetGenerationQueue(t)
	service.InitHttpClient()
	// A PNG of noise: about 1 MiB, which no compression shrinks.
	large := testImage(512, 512, func(x, y int) color.Color {
		v := crc32.ChecksumIEEE([]byte{byte(x), byte(x >> 8), byte(y), byte(y >> 8)})
		return color.NRGBA{R: uint8(v), G: uint8(v >> 8), B: uint8(v >> 16), A: 255}
	})
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("image_0.png")
	require.NoError(t, err)
	_, err = entry.Write(large)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	zipped := archive.String()
	// A step's preview each, then the final.
	var steps []map[string]any
	for step := range 28 {
		steps = append(steps, map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": step, "image": base64.StdEncoding.EncodeToString(testImage(8, 8, solid(color.Gray{Y: uint8(step)})))})
	}
	events := sseEvents(t, append(steps, map[string]any{"event_type": "final", "samp_ix": 0, "image": base64.StdEncoding.EncodeToString(large)})...)

	for _, tc := range []struct {
		mode              string
		relayMode         int
		model             string
		stream            bool
		body, contentType string
	}{
		{"chat", relayconstant.RelayModeChatCompletions, "nai-diffusion-4-5-full", false, zipped, "application/zip"},
		{"chat stream", relayconstant.RelayModeChatCompletions, "nai-diffusion-4-5-full", true, zipped, "application/zip"},
		{"images", relayconstant.RelayModeImagesGenerations, "nai-diffusion-4-5-full", false, zipped, "application/zip"},
		{"images stream", relayconstant.RelayModeImagesGenerations, "nai-diffusion-4-5-full", true, events, "text/event-stream"},
		{"V3 images stream", relayconstant.RelayModeImagesGenerations, "nai-diffusion-3", true, zipped, "application/zip"},
		{"native image", relayconstant.RelayModeNovelAIGenerateImage, "nai-diffusion-4-5-full", false, zipped, "application/zip"},
		{"native image stream", relayconstant.RelayModeNovelAIGenerateImageStream, "nai-diffusion-4-5-full", true, events, "text/event-stream"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, _, resp, info := imageStreamContext(t, tc.body, tc.contentType, 1)
			info.RelayMode, info.IsStream = tc.relayMode, tc.stream
			info.ChannelMeta = &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "https://image.novelai.net", ApiKey: "pst-stalled-" + tc.mode, UpstreamModelName: tc.model}
			release, apiErr := holdGeneration(c, info)
			require.Nil(t, apiErr)
			upstream := &endTracker{Reader: strings.NewReader(tc.body)}
			resp.Body = newGenerationBody(io.NopCloser(upstream), release)
			client := &stalledClient{ResponseWriter: c.Writer, entered: make(chan struct{}), unblock: make(chan struct{})}
			c.Writer = client
			generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: tc.model, Prompt: "a cat", N: 1, Extras: map[string]json.RawMessage{"steps": raw("28")}})
			require.Nil(t, apiErr)
			adaptor := &Adaptor{generation: generation, output: ImageOutput{ResponseFormat: "b64_json", OutputFormat: formatPNG, Background: "opaque", Quality: "auto", PartialImages: 1, Event: "image_generation"}}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = adaptor.DoResponse(c, resp, info)
			}()
			defer func() {
				close(client.unblock)
				<-done
			}()
			select {
			case <-client.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the relay never wrote to its client")
			}

			assert.Eventually(t, func() bool { return keyIsFree(info.ApiKey) }, 5*time.Second, time.Millisecond, "the key is free while the client has read nothing")
			assert.True(t, upstream.ended.Load(), "NovelAI's answer was read to its end")
		})
	}

	t.Run("what waits for the relay is bounded", func(t *testing.T) {
		savedAhead := generationReadAhead
		generationReadAhead = 64 << 10
		t.Cleanup(func() { generationReadAhead = savedAhead })
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", nil)
		info := officialKey("pst-read-ahead", common.GetPointer(0))
		release, apiErr := holdGeneration(c, info)
		require.Nil(t, apiErr)
		upstream := &endTracker{Reader: strings.NewReader(zipped)}
		body := newGenerationBody(io.NopCloser(upstream), release)
		defer body.Close()

		_, apiErr = holdGeneration(c, info)
		require.NotNil(t, apiErr, "NovelAI's answer is not read past the read-ahead while nothing takes it")
		assert.False(t, upstream.ended.Load())
		read, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, zipped, string(read), "the answer arrives whole and in order")
		assert.Eventually(t, func() bool { return keyIsFree(info.ApiKey) }, 5*time.Second, time.Millisecond)

		// A read waiting on a NovelAI that sends nothing more ends when the
		// body closes (the idle timeout and the stream cleanup close it),
		// which frees the slot.
		release, apiErr = holdGeneration(c, info)
		require.Nil(t, apiErr)
		silent, _ := io.Pipe()
		waiting := newGenerationBody(silent, release)
		readErr := make(chan error)
		go func() {
			_, err := waiting.Read(make([]byte, 8))
			readErr <- err
		}()
		require.NoError(t, waiting.Close())
		assert.ErrorIs(t, <-readErr, errGenerationBodyClosed)
		assert.True(t, keyIsFree(info.ApiKey))
	})

	t.Run("an error answer that breaks off", func(t *testing.T) {
		var requests atomic.Int32
		ended := make(chan struct{})
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requests.Add(1) > 1 {
				// More than the relay keeps, and no end before the test's.
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(bytes.Repeat([]byte("x"), 3<<20))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-ended:
				}
				return
			}
			// A 500 that promises more than it sends.
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"statusCode":500,"message":"Error gen`))
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		}))
		t.Cleanup(proxy.Close)
		t.Cleanup(func() { close(ended) })
		// No waiting: a key the failed attempt kept would fail the retry
		// with a 429 at once.
		info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://image.novelai.net", ApiKey: "pst-error-answer",
			ChannelSetting:       dto.ChannelSettings{Proxy: proxy.URL},
			ChannelOtherSettings: dto.ChannelOtherSettings{NovelAIQueueLimit: common.GetPointer(0)},
		}}
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 1})
		require.Nil(t, apiErr)
		adaptor := &Adaptor{generation: generation}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

		resp, err := adaptor.DoRequest(c, info, strings.NewReader(`{}`))
		require.NoError(t, err)
		// What the image relay does with an error answer.
		relayErr := service.RelayErrorHandler(c.Request.Context(), resp.(*http.Response), false)
		assert.Equal(t, http.StatusInternalServerError, relayErr.StatusCode)
		assert.False(t, types.IsSkipRetryError(relayErr), "a 500 is retried")

		// The retry, on the same key and client request.
		resp, err = adaptor.DoRequest(c, info, strings.NewReader(`{}`))
		require.NoError(t, err, "the failed attempt left the slot free")
		body, err := io.ReadAll(resp.(*http.Response).Body)
		require.NoError(t, err)
		assert.Len(t, body, 1<<20, "an error answer is kept up to 1 MiB")
		assert.True(t, keyIsFree(info.ApiKey), "the rest of the answer was not waited for: closing it freed the key")
		assert.Equal(t, int32(2), requests.Load())
	})
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

// leavingWriter is a client whose connection broke on the first write: the
// write fails, and the server ends the request.
type leavingWriter struct {
	gin.ResponseWriter
	cancel context.CancelFunc
}

func (w *leavingWriter) Write([]byte) (int, error) {
	w.cancel()
	return 0, errors.New("broken pipe")
}

func (w *leavingWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

// clientLeavingReader is a NovelAI answer whose read the client's leaving
// ends: it cancels the request and fails as the upstream read then does.
type clientLeavingReader struct {
	cancel context.CancelFunc
}

func (r clientLeavingReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, context.Canceled
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
// one included) and ends with OpenAI's error event, without [DONE]; an
// error before any image fails the request, which bills nothing; a client
// that leaves keeps the reservation. V3, which cannot stream, answers whole and gets completed
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
	tokens := generation.Tokens
	require.Equal(t, 1290, tokens.PerImage, "a 1024x1024 image")
	require.Zero(t, tokens.InputImages)
	// completed checks a completed event; the last one carries the usage of
	// the delivered images delivered with it, the others zeros.
	completed := func(t *testing.T, event streamedEvent, want []byte, seed float64, delivered int) {
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
		if delivered == 0 {
			assert.Equal(t, map[string]any{"input_tokens": 0.0, "input_tokens_details": map[string]any{"text_tokens": 0.0, "image_tokens": 0.0}, "output_tokens": 0.0, "total_tokens": 0.0}, usage, "the usage counts once, on the last image")
			return
		}
		assert.Equal(t, map[string]any{
			"input_tokens":         float64(tokens.Text),
			"input_tokens_details": map[string]any{"text_tokens": float64(tokens.Text), "image_tokens": 0.0},
			"output_tokens":        float64(delivered * 1290),
			"total_tokens":         float64(tokens.Text + delivered*1290),
		}, usage)
	}
	// errorEvent checks that a stream ends as OpenAI's image streams do on a
	// failure: an error event, typed, and no [DONE].
	errorEvent := func(t *testing.T, events []streamedEvent, message string) {
		t.Helper()
		require.NotEmpty(t, events)
		event := events[len(events)-1]
		assert.Equal(t, "error", event.name)
		assert.Equal(t, "error", event.data["type"])
		require.Contains(t, event.data, "error")
		assert.Contains(t, event.data["error"].(map[string]any)["message"], message)
	}

	t.Run("partials from sample 0, completed in sample order", func(t *testing.T) {
		body := sseEvents(t, intermediate(0, 0), intermediate(1, 0), intermediate(0, 1), intermediate(1, 1), intermediate(1, 2), intermediate(0, 2), intermediate(0, 3), final(1, blue), final(0, red))
		c, recorder, resp, info := imageStreamContext(t, body, "text/event-stream", 2)

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output, nil)

		require.Nil(t, apiErr)
		assert.Equal(t, tokens.Usage(2), usage)
		assert.Equal(t, 2*1290, usage.CompletionTokens)
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
		completed(t, events[2], red, 100, 0)
		completed(t, events[3], blue, 101, 2)
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

		_, apiErr := imagesStreamResponse(c, resp, info, generation, withoutPartials, nil)

		require.Nil(t, apiErr)
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 2)
		completed(t, events[0], red, 100, 0)
		completed(t, events[1], blue, 101, 2)
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

			usage, apiErr := imagesStreamResponse(c, resp, info, generation, output, nil)

			require.Nil(t, apiErr)
			assert.Equal(t, tokens.Usage(len(tc.want)), usage, "the images delivered, not the 2 asked for")
			events := streamedEvents(t, recorder.Body.String())
			require.Len(t, events, len(tc.want)+1, recorder.Body.String())
			for i, want := range tc.want {
				delivered := 0
				if tc.withUsage && i == len(tc.want)-1 {
					delivered = len(tc.want)
				}
				completed(t, events[i], want, tc.seeds[i], delivered)
			}
			errorEvent(t, events, tc.message)
			assert.Equal(t, float64(len(tc.want)), info.PriceData.OtherRatios()["n"])
			assert.True(t, info.StreamStatus.ResponseFailed())
		})
	}

	// The images held back for a missing sample reach the client in sample
	// order when the stream ends, and only the last of them carries the
	// usage, so a client that adds up the events counts it once.
	t.Run("images held back for a missing sample carry the usage on the last", func(t *testing.T) {
		three, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 3, Extras: map[string]json.RawMessage{"steps": raw("4"), "seed": raw("100")}})
		require.Nil(t, apiErr)
		require.Equal(t, tokens, three.Tokens)
		c, recorder, resp, info := imageStreamContext(t, sseEvents(t, final(2, blue), final(1, red)), "text/event-stream", 3)

		usage, apiErr := imagesStreamResponse(c, resp, info, three, output, nil)

		require.Nil(t, apiErr)
		assert.Equal(t, tokens.Usage(2), usage, "the images delivered, not the 3 asked for")
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 3, recorder.Body.String())
		completed(t, events[0], red, 101, 0)
		completed(t, events[1], blue, 102, 2)
		errorEvent(t, events, "ended after 2 of 3 images")
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
	})

	t.Run("an error before any image fails the request", func(t *testing.T) {
		c, recorder, resp, info := imageStreamContext(t, sseEvents(t, intermediate(0, 0), failure), "text/event-stream", 2)

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output, nil)

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

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output, nil)

		require.Nil(t, apiErr)
		assert.Equal(t, tokens.Usage(2), usage, "the reserved images")
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "a client abort never lowers the charge")
	})

	// NovelAI may still generate what a broken stream (a transport error, a
	// timeout) was charged for: the reservation stays, and the client learns
	// the stream was cut.
	t.Run("a broken stream keeps the reservation", func(t *testing.T) {
		c, recorder, resp, info := imageStreamContext(t, "", "text/event-stream", 2)
		resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(sseEvents(t, final(0, red))), iotest.ErrReader(errors.New("connection reset by peer"))))

		usage, apiErr := imagesStreamResponse(c, resp, info, generation, output, nil)

		require.Nil(t, apiErr)
		assert.Equal(t, tokens.Usage(2), usage, "the reserved images")
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 2, recorder.Body.String())
		completed(t, events[0], red, 100, 0)
		errorEvent(t, events, "upstream stream ended before the response was complete")
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
			stream := &imageStream{c: c, info: info, generation: generation, output: output,
				received: make([]bool, generation.NSamples), pending: map[int]renderedImage{}}

			stream.handle(string(mustMarshal(t, event)), &helper.StreamResult{})
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
			usage, apiErr := stream.finish()

			require.Nil(t, apiErr)
			assert.Equal(t, tokens.Usage(2), usage, "the reserved images")
			assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "a client abort never lowers the charge")
			assert.Equal(t, 1, writer.attempts, "one failed write, no retry")
			assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
			assert.Equal(t, perfmetrics.OutcomeIgnored, perfmetrics.ClassifyRelayOutcome(c.Request.Context(), info, apiErr))
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

		usage, apiErr := imagesStreamResponse(c, resp, info, v3, edit, nil)

		require.Nil(t, apiErr)
		// The 16x16 source makes a 1024x1024 canvas: the source counts as
		// one input image, and the two images delivered as completion.
		assert.Equal(t, "1024x1024", v3.Size())
		assert.Equal(t, GenerationTokens{Text: v3.Tokens.Text, InputImages: 1290, PerImage: 1290}, v3.Tokens)
		assert.Equal(t, v3.Tokens.Usage(2), usage)
		events := streamedEvents(t, recorder.Body.String())
		require.Len(t, events, 2, recorder.Body.String())
		for i, event := range events {
			assert.Equal(t, "image_edit.completed", event.name)
			assert.Equal(t, v3.Size(), event.data["size"])
			assert.Equal(t, float64(100+i), event.data["seed"])
		}
		assert.Zero(t, events[0].data["usage"].(map[string]any)["input_tokens"])
		assert.Equal(t, map[string]any{
			"input_tokens":         float64(v3.Tokens.Text + 1290),
			"input_tokens_details": map[string]any{"text_tokens": float64(v3.Tokens.Text), "image_tokens": 1290.0},
			"output_tokens":        2 * 1290.0,
			"total_tokens":         float64(v3.Tokens.Text + 3*1290),
		}, events[1].data["usage"])
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "billed per image received")
		assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
		assert.True(t, info.PerformanceWholeOutput, "the images arrived whole: throughput spans the request")
	})
}

// A client that leaves a converted request keeps the reserved charge, its
// usage included, and is no perf-metrics health sample, as on the native
// routes and the relay streams: a write to it fails, or its leaving ends the
// read of NovelAI's answer. That holds when NovelAI made fewer images than
// reserved too.
func TestConvertedResponsesCountClientAbortsAsCancellations(t *testing.T) {
	require.NoError(t, i18n.Init())
	red := testImage(16, 16, solid(color.NRGBA{R: 255, A: 255}))
	archives := map[int][]byte{}
	for _, made := range []int{1, 2} {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for i := range made {
			entry, err := writer.Create(fmt.Sprintf("image_%d.png", i))
			require.NoError(t, err)
			_, err = entry.Write(red)
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		archives[made] = archive.Bytes()
	}

	for _, mode := range []string{"chat", "chat stream", "images", "V3 images stream"} {
		for _, leaves := range []string{"write fails", "read ends"} {
			for _, made := range []int{2, 1} {
				t.Run(fmt.Sprintf("%s, %s, %d of 2 made", mode, leaves, made), func(t *testing.T) {
					archive := archives[made]
					model, relayMode := "nai-diffusion-4-5-full", relayconstant.RelayModeImagesGenerations
					switch mode {
					case "chat", "chat stream":
						relayMode = relayconstant.RelayModeChatCompletions
					case "V3 images stream":
						model = "nai-diffusion-3"
					}
					c, _, resp, info := imageStreamContext(t, string(archive), "application/zip", 2)
					info.RelayMode, info.IsStream, info.UpstreamModelName = relayMode, strings.HasSuffix(mode, "stream"), model
					ctx, cancel := context.WithCancel(c.Request.Context())
					t.Cleanup(cancel)
					c.Request = c.Request.WithContext(ctx)
					switch leaves {
					case "write fails":
						c.Writer = &leavingWriter{ResponseWriter: c.Writer, cancel: cancel}
					case "read ends":
						resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(archive[:16]), clientLeavingReader{cancel: cancel}))
					}
					generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: model, Prompt: "a cat", N: 2})
					require.Nil(t, apiErr)
					adaptor := &Adaptor{generation: generation, output: ImageOutput{ResponseFormat: "b64_json", OutputFormat: formatPNG, Background: "opaque", Quality: "auto", Event: "image_generation"}}

					usage, apiErr := adaptor.DoResponse(c, resp, info)

					require.Nil(t, apiErr)
					assert.Equal(t, generation.Tokens.Usage(2), usage, "the usage of the reserved images")
					assert.Equal(t, 2*generation.Tokens.PerImage, usage.(*dto.Usage).CompletionTokens)
					assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "a client abort never lowers the charge")
					assert.Equal(t, perfmetrics.OutcomeIgnored, perfmetrics.ClassifyRelayOutcome(c.Request.Context(), info, apiErr))
				})
			}
		}
	}
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
	// The 2 images delivered of the 3 reserved, at 1024x1024.
	prompt := generation.Tokens.Text
	delivered := &dto.Usage{PromptTokens: prompt, CompletionTokens: 2 * 1290, TotalTokens: prompt + 2*1290, PromptTokensDetails: dto.InputTokenDetails{TextTokens: prompt}}
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
		usage, apiErr := chatResponse(c, resp, info, generation, nil)
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
		assert.Equal(t, delivered, usage)
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
		assert.Equal(t, [3]int{prompt, 2 * 1290, prompt + 2*1290}, [3]int{response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens})
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "billed per image received")
		assert.False(t, info.PerformanceWholeOutput, "a non-stream response's throughput spans the request anyway")
	})

	for _, includeUsage := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream, include_usage %t", includeUsage), func(t *testing.T) {
			recorder, info, usage, apiErr := respond(t, true, includeUsage, archive.Bytes())

			require.Nil(t, apiErr)
			assert.Equal(t, delivered, usage)
			assert.True(t, info.PerformanceWholeOutput, "the images arrived whole: throughput spans the request")
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
				assert.Equal(t, float64(prompt), chunkUsage["prompt_tokens"])
				assert.Equal(t, 2*1290.0, chunkUsage["completion_tokens"])
				assert.Equal(t, float64(prompt+2*1290), chunkUsage["total_tokens"])
				assert.Empty(t, events[4].data["choices"])
			}
			assert.Equal(t, map[string]any{"[DONE]": true}, events[len(events)-1].data)
			assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
			assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
		})
	}

	// NovelAI was asked for the 2 images billing reserved: a third is
	// neither delivered nor billed.
	t.Run("more images than asked for", func(t *testing.T) {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for i := range 3 {
			entry, err := writer.Create(fmt.Sprintf("image_%d.png", i))
			require.NoError(t, err)
			_, err = entry.Write(red)
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())

		recorder, info, _, apiErr := respond(t, false, true, archive.Bytes())

		require.Nil(t, apiErr)
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.Len(t, response.Choices, 1)
		assert.Len(t, markdownPNGs(t, response.Choices[0].Message.Content), 2)
		assert.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
	})

	// NovelAI made one of the 2 images asked for: the usage counts the one
	// delivered and billed, in the response and in the log.
	t.Run("fewer images than asked for", func(t *testing.T) {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		entry, err := writer.Create("image_0.png")
		require.NoError(t, err)
		_, err = entry.Write(red)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		one := &dto.Usage{PromptTokens: prompt, CompletionTokens: 1290, TotalTokens: prompt + 1290, PromptTokensDetails: dto.InputTokenDetails{TextTokens: prompt}}
		for _, stream := range []bool{false, true} {
			recorder, info, usage, apiErr := respond(t, stream, true, archive.Bytes())

			require.Nil(t, apiErr)
			assert.Equal(t, one, usage)
			assert.Equal(t, 1.0, info.PriceData.OtherRatios()["n"])
			var reported map[string]any
			if stream {
				events := streamedEvents(t, recorder.Body.String())
				require.Len(t, events, 5, recorder.Body.String())
				reported = events[3].data["usage"].(map[string]any)
			} else {
				var response map[string]any
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				reported = response["usage"].(map[string]any)
			}
			assert.Equal(t, []float64{float64(prompt), 1290, float64(prompt + 1290)}, []float64{reported["prompt_tokens"].(float64), reported["completion_tokens"].(float64), reported["total_tokens"].(float64)})
		}
	})

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

// One image counts as 1290 tokens (Gemini's documented size of one
// generated image up to 1024x1024) scaled by its area and rounded up; the
// area counts at most NovelAI's 3,145,728-pixel canvas.
func TestImageTokens(t *testing.T) {
	for _, tc := range []struct{ width, height, want int }{
		{1024, 1024, 1290},
		{832, 1216, 1245}, // 1290 x 1,011,712 / 1,048,576 = 1244.7
		{1216, 832, 1245},
		{1024, 1536, 1935},
		{64, 64, 6},        // 5.04
		{1728, 1728, 3674}, // 3673.48
		{2048, 1536, 3870}, // NovelAI's largest canvas
		{65_536, 65_536, 3870},
		{0, -5, 1},
	} {
		assert.Equal(t, tc.want, ImageTokens(tc.width, tc.height), "%dx%d", tc.width, tc.height)
	}
}

// A NovelAI request counts as NovelAI reads it: the text of its V4
// captions (characters included) or else of input, and of its negative
// captions or else negative_prompt (or uc); its canvas, NovelAI's 832x1216
// by default; and as input images its img2img or infill source (not the
// mask) and each vibe or Precise Reference image, as base64 or as the
// cached lists of a multipart form. A key repeated in another case counts
// at its largest, one repeated inside a caption at all its values. V3 has no
// V4 captions and reads input and negative_prompt; for a model without a
// profile each side counts the larger of its caption and its plain prompt.
func TestRequestTokens(t *testing.T) {
	const model = "nai-diffusion-5-full"
	estimateFor := func(model string, texts ...string) int {
		total := 0
		for _, text := range texts {
			total += tokenkit.Estimate(model, text)
		}
		return total
	}
	estimate := func(texts ...string) int { return estimateFor(model, texts...) }
	long := strings.Repeat("masterpiece, best quality, 1girl, detailed background, ", 8)
	require.Greater(t, estimate(long), estimate("a", "b")+10, "a prompt much longer than a word")
	square := `"width":1024,"height":1024`
	references := func(count int) string {
		list := make([]string, count)
		for i := range list {
			list[i] = `"dmliZQ=="`
		}
		return "[" + strings.Join(list, ",") + "]"
	}
	for _, tc := range []struct {
		name string
		body string
		want GenerationTokens
	}{
		{"V4 captions count once, with their characters",
			`{"input":"1girl, very aesthetic","model":"` + model + `","action":"generate","parameters":{"width":832,"height":1216,"negative_prompt":"lowres, bad hands",` +
				`"v4_prompt":{"caption":{"base_caption":"1girl, very aesthetic","char_captions":[{"char_caption":"girl, red hair","centers":[{"x":0.5,"y":0.5}]}]},"use_order":true},` +
				`"v4_negative_prompt":{"caption":{"base_caption":"lowres, bad hands","char_captions":[{"char_caption":"blurry"}]}}}}`,
			GenerationTokens{Text: estimate("1girl, very aesthetic", "girl, red hair", "lowres, bad hands", "blurry"), PerImage: 1245}},
		{"input and negative_prompt without captions",
			`{"input":"a cat","model":"` + model + `","parameters":{` + square + `,"negative_prompt":"lowres"}}`,
			GenerationTokens{Text: estimate("a cat", "lowres"), PerImage: 1290}},
		{"uc without negative_prompt",
			`{"input":"a cat","model":"` + model + `","parameters":{` + square + `,"uc":"lowres"}}`,
			GenerationTokens{Text: estimate("a cat", "lowres"), PerImage: 1290}},
		{"NovelAI's default canvas", `{"input":"a cat","model":"` + model + `"}`, GenerationTokens{Text: estimate("a cat"), PerImage: 1245}},
		{"a side that is not a positive whole number keeps its default",
			`{"input":"a cat","model":"` + model + `","parameters":{"width":"1024","height":1000.5}}`,
			GenerationTokens{Text: estimate("a cat"), PerImage: 1245}},
		{"one side given", `{"input":"a cat","model":"` + model + `","parameters":{"width":1024}}`, GenerationTokens{Text: estimate("a cat"), PerImage: 1532}},
		{"an empty prompt counts 1", `{"input":"","model":"` + model + `"}`, GenerationTokens{Text: 1, PerImage: 1245}},
		{"img2img counts its source",
			`{"input":"a cat","model":"` + model + `","action":"img2img","parameters":{` + square + `,"image":"aW1n","strength":0.7}}`,
			GenerationTokens{Text: estimate("a cat"), InputImages: 1290, PerImage: 1290}},
		{"infill counts its source, not its mask",
			`{"input":"a cat","model":"` + model + `-inpainting","action":"infill","parameters":{` + square + `,"image":"aW1n","mask":"bWFzaw=="}}`,
			GenerationTokens{Text: estimate("a cat"), InputImages: 1290, PerImage: 1290}},
		{"a multipart form names its source part",
			`{"input":"a cat","model":"` + model + `","action":"img2img","parameters":{` + square + `,"image":"image","image_cache_secret_key":"ab12"}}`,
			GenerationTokens{Text: estimate("a cat"), InputImages: 1290, PerImage: 1290}},
		{"each vibe and reference image counts, base64 or cached",
			`{"input":"a cat","model":"` + model + `","parameters":{` + square + `,"reference_image_multiple":["dmliZQ==","dmliZQ==",""],"reference_strength_multiple":[0.6,0.6,0.6],` +
				`"director_reference_images_cached":[{"cache_secret_key":"a","data":"director_ref_0"},{"cache_secret_key":"b"},{"cache_secret_key":"c","data":"director_ref_2"}]}}`,
			GenerationTokens{Text: estimate("a cat"), InputImages: 5 * 1290, PerImage: 1290}},
		{"a list sent both ways counts once",
			`{"input":"a cat","model":"` + model + `","parameters":{` + square + `,"reference_image_multiple":["dmliZQ=="],"reference_image_multiple_cached":[{"cache_secret_key":"a","data":"ref_multiple_0"}]}}`,
			GenerationTokens{Text: estimate("a cat"), InputImages: 1290, PerImage: 1290}},
		{"a reference list counts at most 16 images",
			`{"input":"a cat","model":"` + model + `","parameters":{` + square + `,"reference_image_multiple":` + references(20) + `}}`,
			GenerationTokens{Text: estimate("a cat"), InputImages: 16 * 1290, PerImage: 1290}},
		{"a key repeated in another case counts at its largest",
			`{"input":"a cat","model":"` + model + `","parameters":{"width":64,"Width":1024,"height":1024}}`,
			GenerationTokens{Text: estimate("a cat"), PerImage: 1290}},
		// JSON.parse and Python's json keep the last of a repeated key.
		{"a key repeated inside a caption counts all its values",
			`{"input":"a","model":"` + model + `","parameters":{` + square + `,"v4_prompt":{"caption":{"base_caption":"a","base_caption":"` + long + `",` +
				`"char_captions":[{"char_caption":"b","char_caption":"` + long + `"}]}},"v4_negative_prompt":{"caption":{"base_caption":"b"},"caption":{"base_caption":"` + long + `"}}}}`,
			GenerationTokens{Text: estimate("a", long, "b", long, "b", long), PerImage: 1290}},
		{"V3 reads input and negative_prompt, not V4 captions",
			`{"input":"` + long + `","model":"nai-diffusion-3","parameters":{` + square + `,"negative_prompt":"` + long + `",` +
				`"v4_prompt":{"caption":{"base_caption":"","char_captions":[]}},"v4_negative_prompt":{"caption":{"base_caption":"a"}}}}`,
			GenerationTokens{Text: estimateFor("nai-diffusion-3", long, long), PerImage: 1290}},
		{"V3 reads uc without negative_prompt",
			`{"input":"a cat","model":"nai-diffusion-furry-3","parameters":{` + square + `,"uc":"` + long + `","v4_prompt":{"caption":{"base_caption":"` + long + `"}}}}`,
			GenerationTokens{Text: estimateFor("nai-diffusion-furry-3", "a cat", long), PerImage: 1290}},
		{"a model without a profile counts each side's larger prompt",
			`{"input":"` + long + `","model":"nai-diffusion-9","parameters":{` + square + `,"negative_prompt":"lowres",` +
				`"v4_prompt":{"caption":{"base_caption":"a"}},"v4_negative_prompt":{"caption":{"base_caption":"` + long + `","char_captions":[{"char_caption":"b"}]}}}}`,
			GenerationTokens{Text: estimateFor("nai-diffusion-9", long) + estimateFor("nai-diffusion-9", long, "b"), PerImage: 1290}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RequestTokens([]byte(tc.body)))
		})
	}
}

// A converted request counts as the native routes count the request NovelAI
// receives (RequestTokens of its body): its final prompts, presets and
// characters included, its canvas, its source image (the infill mask is not
// an input image) and the references it passes through.
func TestBuildGenerationCountsTheRequestNovelAIReceives(t *testing.T) {
	require.NoError(t, i18n.Init())
	source := testImage(96, 64, solid(color.White))
	mask := testImage(96, 64, func(x, y int) color.Color {
		if x >= 48 {
			return color.NRGBA{}
		}
		return color.NRGBA{A: 255}
	})
	const model = "nai-diffusion-4-5-full"
	for _, tc := range []struct {
		name        string
		intent      GenerationIntent
		characters  []string
		inputImages int
	}{
		{"generation with a negative prompt and characters", GenerationIntent{Prompt: "1girl", Size: "1024x1024",
			Extras: map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`), "characterPrompts": raw(`[{"prompt":"girl, red hair","uc":"blurry"}]`)}}, []string{"girl, red hair", "blurry"}, 0},
		{"img2img", GenerationIntent{Prompt: "1girl", Size: "1024x1024", Image: source}, nil, 1},
		{"infill", GenerationIntent{Prompt: "1girl", Size: "1024x1024", Image: source, Mask: mask}, nil, 1},
		{"vibes passed through", GenerationIntent{Prompt: "1girl", Size: "1024x1024",
			Parameters: map[string]json.RawMessage{"reference_image_multiple": raw(`["dmliZQ==","dmliZQ=="]`), "reference_strength_multiple": raw(`[0.6,0.6]`)}}, nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.intent.Model, tc.intent.N = model, 2
			generation, parameters := buildParameters(t, tc.intent)
			input, negative := generation.Body["input"].(string), parameters["negative_prompt"].(string)
			assert.Contains(t, negative, "lowres", "the UC preset is counted")
			text := tokenkit.Estimate(generation.Model, input) + tokenkit.Estimate(generation.Model, negative)
			for _, character := range tc.characters {
				text += tokenkit.Estimate(generation.Model, character)
			}
			assert.Equal(t, GenerationTokens{Text: text, InputImages: tc.inputImages * 1290, PerImage: 1290}, generation.Tokens)
			assert.Equal(t, RequestTokens(mustMarshal(t, generation.Body)), generation.Tokens, "as the native routes count the same body")
			assert.Equal(t, &dto.Usage{
				PromptTokens: text + tc.inputImages*1290, CompletionTokens: 2 * 1290, TotalTokens: text + (tc.inputImages+2)*1290,
				PromptTokensDetails: dto.InputTokenDetails{TextTokens: text, ImageTokens: tc.inputImages * 1290},
			}, generation.Tokens.Usage(2))
		})
	}
}

// The HD canvas is each side times 1.5, rounded down and then to the nearest
// multiple of 64 with ties up, as NovelAI's client sends its Enhance; there is
// none past NovelAI's 3,145,728 pixels or when the rounding stretches one
// side against the other by more than 3%. The rows the web client does not
// offer 1.5x for (16:9, 4:5, 704x1216) are enhanced as it enhances its
// hard-coded 832x1216.
func TestHDCanvas(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		want          [2]int
		ok            bool
	}{
		{832, 1216, [2]int{1280, 1856}, true},
		{1216, 832, [2]int{1856, 1280}, true},
		{1024, 1024, [2]int{1536, 1536}, true},
		{896, 1152, [2]int{1344, 1728}, true},
		{1152, 896, [2]int{1728, 1344}, true},
		{768, 1344, [2]int{1152, 2048}, true},
		{1344, 768, [2]int{2048, 1152}, true},
		{896, 1088, [2]int{1344, 1664}, true},
		{1088, 896, [2]int{1664, 1344}, true},
		{640, 1536, [2]int{960, 2304}, true},
		{1536, 640, [2]int{2304, 960}, true},
		{704, 1216, [2]int{1088, 1856}, true},
		{640, 640, [2]int{960, 960}, true},
		{512, 512, [2]int{768, 768}, true},
		{1152, 1152, [2]int{1728, 1728}, true},
		{1024, 1536, [2]int{1536, 2304}, false},
		{1536, 1024, [2]int{2304, 1536}, false},
		{1792, 1024, [2]int{2688, 1536}, false},
		{1024, 1792, [2]int{1536, 2688}, false},
		{1472, 1472, [2]int{2240, 2240}, false},
		{1088, 1920, [2]int{1664, 2880}, false},
		{448, 1024, [2]int{704, 1536}, false},
		{64, 1024, [2]int{128, 1536}, false},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			w, h, ok := hdCanvas(tc.width, tc.height)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, [2]int{w, h})
		})
	}
}

// The enhance prompt gets NovelAI's ", -2::upscaled, blurry::," as its web
// client splices it: before the first Text: section (from the separator in
// front of it), otherwise at the end, and not when the prompt already says
// "upscaled, blurry" in that case.
func TestHDPrompt(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1girl, very aesthetic, masterpiece, no text", "1girl, very aesthetic, masterpiece, no text, -2::upscaled, blurry::,"},
		{"1girl, holding sign, very aesthetic, masterpiece, no text. Text: OPEN", "1girl, holding sign, very aesthetic, masterpiece, no text., -2::upscaled, blurry::, Text: OPEN"},
		{"Text: hi", ", -2::upscaled, blurry::,Text: hi"},
		{"a, upscaled, blurry", "a, upscaled, blurry"},
		{"context: x", "context: x, -2::upscaled, blurry::,"},
		{"a text::b", "a text::b, -2::upscaled, blurry::,"},
		{"a, Upscaled, Blurry", "a, Upscaled, Blurry, -2::upscaled, blurry::,"},
		{"a|Text: b", "a|Text: b, -2::upscaled, blurry::,"},
	} {
		assert.Equal(t, tc.want, hdPrompt(tc.in), tc.in)
	}
}

// What an enhance costs, as NovelAI's client prices it: the log names it.
func TestEnhanceAnlas(t *testing.T) {
	assert.Equal(t, 23, enhanceAnlas(2_375_680, 28, false, false))
	assert.Equal(t, 35, enhanceAnlas(2_375_680, 28, true, false))
	assert.Equal(t, 20, enhanceAnlas(2_375_680, 23, false, false))
	assert.Equal(t, 30, enhanceAnlas(2_375_680, 23, true, false))
	assert.Equal(t, 34, enhanceAnlas(2_359_296, 28, true, false))
	assert.Equal(t, 19, enhanceAnlas(2_375_680, 14, true, true))
}

// An enhance seed follows from the image's own seed and is never it: the
// reported seed reproduces the HD image too.
func TestHDSeed(t *testing.T) {
	assert.Equal(t, int64(2452104379), hdSeed(1234567890))
	assert.Equal(t, int64(2724067900), hdSeed(1))
	assert.Equal(t, int64(2133847276), hdSeed(MaxSeed))
	for seed := int64(1); seed <= 10_000; seed++ {
		derived := hdSeed(seed)
		require.NotEqual(t, seed, derived)
		require.GreaterOrEqual(t, derived, int64(2))
		require.LessOrEqual(t, derived, int64(MaxSeed))
		require.Equal(t, derived, hdSeed(seed))
	}
}

// hdPNG is a PNG of width x height in one colour.
func hdPNG(width, height int, fill color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(fill), image.Point{}, draw.Src)
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, img)
	return buffer.Bytes()
}

// hdZip is NovelAI's answer to a generation: a zip of image_0.png, ...
func hdZip(images ...[]byte) []byte {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for i, data := range images {
		entry, _ := writer.Create(fmt.Sprintf("image_%d.png", i))
		_, _ = entry.Write(data)
	}
	_ = writer.Close()
	return archive.Bytes()
}

// hdBaseColor is the colour of base image i in the HD tests, hdColor that of
// an HD image.
func hdBaseColor(i int) color.NRGBA { return color.NRGBA{R: uint8(20 + 60*i), G: 140, B: 60, A: 255} }

var hdColor = color.NRGBA{R: 255, B: 255, A: 255}

// enhanceRequest plans HD from sent, the body as it went to NovelAI, and
// returns the enhance request of its first image, from a base image of the
// generation's canvas.
func enhanceRequest(t *testing.T, sent []byte, generation *Generation) (*hdPlan, []byte) {
	t.Helper()
	outbound, closer, err := relaycommon.NewOutboundJSONBody(sent)
	require.NoError(t, err)
	defer closer.Close()
	plan, reason := planHD(outbound, generation)
	require.NotNil(t, plan, reason)
	source, err := enhanceSource(context.Background(), hdPNG(plan.baseWidth, plan.baseHeight, hdBaseColor(0)), image.Pt(plan.baseWidth, plan.baseHeight), plan.width, plan.height)
	require.NoError(t, err)
	body, err := plan.requestBody(source, hdSeed(plan.seed))
	require.NoError(t, err)
	return plan, body
}

// enhanceJSON is an enhance request decoded, with its image replaced by
// "PNG WxH" once it decodes as an opaque PNG of the HD canvas.
func enhanceJSON(t *testing.T, plan *hdPlan, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(body, &decoded))
	parameters := decoded["parameters"].(map[string]any)
	source := decodedBase64Image(t, parameters["image"])
	assert.Equal(t, image.Pt(plan.width, plan.height), source.Bounds().Size(), "the image is the HD canvas")
	opaque, ok := source.(interface{ Opaque() bool })
	assert.True(t, ok && opaque.Opaque(), "the image is opaque")
	parameters["image"] = fmt.Sprintf("PNG %dx%d", source.Bounds().Dx(), source.Bounds().Dy())
	return decoded
}

// The enhance request of every family that gets HD, from the golden base
// requests (prompt "1girl, cat ears", negative "bad hands", seed 1234567890,
// auto size 1024x1024 → 1536x1536): the body as sent with only the enhance
// keys changed (img2img, the HD canvas and image, strength 0.5, noise 0, the
// seed hdSeed gives and its extra noise seed one below, no colour
// correction, one sample), NovelAI's prompt addition on V4.5 and V5 in input
// and its V4 caption, and Variety+ computed at the HD canvas as the web
// client computes it. The presets the base carries are not added again.
// Run with -update to rewrite testdata/requests/*-hd.json after a
// deliberate change, and review the diff.
func TestEnhanceGoldenRequests(t *testing.T) {
	require.NoError(t, i18n.Init())
	extras := map[string]json.RawMessage{"negative_prompt": raw(`"bad hands"`), "seed": raw("1234567890")}
	type variant struct {
		name   string
		intent GenerationIntent
	}
	var variants []variant
	for _, model := range []string{
		"nai-diffusion-4-curated-preview", "nai-diffusion-4-full", "nai-diffusion-4-5-curated", "nai-diffusion-4-5-full",
		"nai-diffusion-5-full", "nai-diffusion-5-curated", "nai-diffusion-5-full-medium",
	} {
		variants = append(variants, variant{model + "-hd", GenerationIntent{Model: model, Prompt: "1girl, cat ears", N: 1, Extras: extras}})
	}
	variety := maps.Clone(extras)
	variety["variety"] = raw("true")
	variants = append(variants,
		// The web client's captured Variety+ at 1280x1856: 88.87784456804029.
		variant{"nai-diffusion-4-5-full-variety-hd", GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "1girl, cat ears", Size: "832x1216", N: 1, Extras: variety}},
		// The addition goes before the Text: section.
		variant{"nai-diffusion-5-full-text-block-hd", GenerationIntent{Model: "nai-diffusion-5-full", Prompt: "1girl, holding sign, Text: OPEN", N: 1, Extras: extras}},
	)
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			generation, apiErr := BuildGeneration(nil, tc.intent)
			require.Nil(t, apiErr)
			require.True(t, generation.HD)
			plan, body := enhanceRequest(t, mustMarshal(t, generation.RequestBody(false)), generation)
			got, err := common.IndentJson(mustMarshal(t, enhanceJSON(t, plan, body)))
			require.NoError(t, err)
			got = append(got, '\n')
			path := filepath.Join("testdata", "requests", tc.name+".json")
			if *updateGolden {
				require.NoError(t, os.WriteFile(path, got, 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}

	sigma := func(t *testing.T, model string, parameters map[string]json.RawMessage) any {
		t.Helper()
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: model, Prompt: "a cat", N: 1, Extras: extras, Parameters: parameters})
		require.Nil(t, apiErr)
		plan, body := enhanceRequest(t, mustMarshal(t, generation.RequestBody(false)), generation)
		return enhanceJSON(t, plan, body)["parameters"].(map[string]any)["skip_cfg_above_sigma"]
	}
	t.Run("Variety+ at the HD canvas", func(t *testing.T) {
		on := map[string]json.RawMessage{"variety": raw("true")}
		assert.Equal(t, 88.57083900622827, sigma(t, "nai-diffusion-4-5-full", on), "V4.5 at 1536x1536, as the web client sends it")
		assert.Equal(t, 29.014585191695467, sigma(t, "nai-diffusion-4-full", on), "V4 at 1536x1536")
		assert.Equal(t, 45.0, sigma(t, "nai-diffusion-4-5-full", map[string]json.RawMessage{"skip_cfg_above_sigma": raw("30")}), "a client's own value is scaled")
		assert.Nil(t, sigma(t, "nai-diffusion-4-5-full", nil), "off stays off")
	})

	t.Run("an override's values and the keys the relay does not know go out as sent", func(t *testing.T) {
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 1, Extras: extras})
		require.Nil(t, apiErr)
		var body, parameters map[string]json.RawMessage
		require.NoError(t, common.Unmarshal(mustMarshal(t, generation.Body), &body))
		require.NoError(t, common.Unmarshal(body["parameters"], &parameters))
		parameters["steps"], parameters["future_key"] = raw("28"), raw(`{"kept":[1,2.50]}`)
		body["parameters"] = mustMarshal(t, parameters)
		plan, enhance := enhanceRequest(t, mustMarshal(t, body), generation)
		assert.Contains(t, string(enhance), `"future_key":{"kept":[1,2.50]}`)
		assert.Contains(t, string(enhance), `"steps":28`)
		assert.Equal(t, 28, plan.steps, "the Anlas the log names are those of the steps sent")
	})

	t.Run("a stream base's stream mode is not sent", func(t *testing.T) {
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 1, Extras: extras})
		require.Nil(t, apiErr)
		plan, body := enhanceRequest(t, mustMarshal(t, generation.RequestBody(true)), generation)
		assert.NotContains(t, enhanceJSON(t, plan, body)["parameters"], "stream")
	})
}

// HD is on by default for a text-to-image request of V4 or later, on the
// images routes and chat. The channel turns it off for good (a request's
// hd true does not override it), and a request turns it off with hd false
// (at the top level, in extra_fields or extra_body, or in parameters, which
// NovelAI never receives) or quality low; any other quality leaves it on.
// Edits, img2img, V3, transparent backgrounds, reference images and canvases
// without an HD canvas get none. The plan checks again what was sent: a
// parameter override can take a request out of HD, and a body that cannot be
// read again or is too large gets none.
func TestHDEligibility(t *testing.T) {
	require.NoError(t, i18n.Init())
	source := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testImage(64, 64, solid(color.White)))
	convert := func(t *testing.T, path, body string, settings dto.ChannelOtherSettings) (*Generation, map[string]any, *types.NewAPIError) {
		t.Helper()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelOtherSettings: settings}}
		adaptor := &Adaptor{}
		var converted any
		var err error
		if path == "/v1/chat/completions" {
			request := &dto.GeneralOpenAIRequest{}
			require.NoError(t, common.UnmarshalBodyReusable(c, request))
			info.RelayMode = relayconstant.RelayModeChatCompletions
			converted, err = adaptor.ConvertOpenAIRequest(c, info, request)
		} else {
			request := &dto.ImageRequest{}
			require.NoError(t, common.UnmarshalBodyReusable(c, request))
			info.RelayMode = relayconstant.RelayModeImagesGenerations
			if path == "/v1/images/edits" {
				info.RelayMode = relayconstant.RelayModeImagesEdits
			}
			converted, err = adaptor.ConvertImageRequest(c, info, *request)
		}
		if err != nil {
			var apiErr *types.NewAPIError
			require.ErrorAs(t, err, &apiErr)
			return nil, nil, apiErr
		}
		var sent map[string]any
		require.NoError(t, common.Unmarshal(mustMarshal(t, converted), &sent))
		return adaptor.generation, sent["parameters"].(map[string]any), nil
	}
	off := dto.ChannelOtherSettings{NovelAIHDDisabled: true}
	const generations, edits, chat = "/v1/images/generations", "/v1/images/edits", "/v1/chat/completions"
	cat := func(extra string) string { return `{"model":"nai-diffusion-4-5-full","prompt":"a cat"` + extra + `}` }
	chatCat := func(extra string) string {
		return `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}]` + extra + `}`
	}
	for _, tc := range []struct {
		name, path, body string
		settings         dto.ChannelOtherSettings
		hd               bool
	}{
		{"default", generations, cat(""), dto.ChannelOtherSettings{}, true},
		{"the channel turned it off", generations, cat(""), off, false},
		{"hd false at the top level", generations, cat(`,"hd":false`), dto.ChannelOtherSettings{}, false},
		{"hd false in extra_fields", generations, cat(`,"extra_fields":{"hd":false}`), dto.ChannelOtherSettings{}, false},
		{"hd false in parameters", generations, cat(`,"parameters":{"hd":false}`), dto.ChannelOtherSettings{}, false},
		{"hd true on a channel that turned it off", generations, cat(`,"hd":true`), off, false},
		{"hd true in parameters on a channel that turned it off", generations, cat(`,"parameters":{"hd":true}`), off, false},
		{"quality low", generations, cat(`,"quality":"low"`), dto.ChannelOtherSettings{}, false},
		{"quality standard", generations, cat(`,"quality":"standard"`), dto.ChannelOtherSettings{}, true},
		{"quality hd", generations, cat(`,"quality":"hd"`), dto.ChannelOtherSettings{}, true},
		{"an edit", edits, cat(`,"image":"` + source + `"`), dto.ChannelOtherSettings{}, false},
		{"the image extension", generations, cat(`,"image":"` + source + `"`), dto.ChannelOtherSettings{}, false},
		{"V3", generations, `{"model":"nai-diffusion-3","prompt":"a cat"}`, dto.ChannelOtherSettings{}, false},
		{"Furry V3", generations, `{"model":"nai-diffusion-furry-3","prompt":"a cat"}`, dto.ChannelOtherSettings{}, false},
		{"a transparent background", generations, `{"model":"nai-diffusion-5-full","prompt":"a cat","background":"transparent"}`, dto.ChannelOtherSettings{}, false},
		{"reference images", generations, cat(`,"parameters":{"reference_image_multiple":["dmliZQ=="],"reference_strength_multiple":[0.6]}`), dto.ChannelOtherSettings{}, false},
		{"1024x1536", generations, cat(`,"size":"1024x1536"`), dto.ChannelOtherSettings{}, false},
		{"1536x1024", generations, cat(`,"size":"1536x1024"`), dto.ChannelOtherSettings{}, false},
		{"1792x1024", generations, cat(`,"size":"1792x1024"`), dto.ChannelOtherSettings{}, false},
		{"768x1344, which the web client does not enhance", generations, cat(`,"size":"768x1344"`), dto.ChannelOtherSettings{}, true},
		{"n=2 at 1216x832", generations, cat(`,"n":2,"size":"1216x832"`), dto.ChannelOtherSettings{}, true},
		{"a chat", chat, chatCat(""), dto.ChannelOtherSettings{}, true},
		{"a chat on a channel that turned it off", chat, chatCat(""), off, false},
		{"a chat with hd false in extra_body", chat, chatCat(`,"extra_body":{"hd":false}`), dto.ChannelOtherSettings{}, false},
		{"a chat with an image", chat, `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":[{"type":"text","text":"a cat"},{"type":"image_url","image_url":{"url":"` + source + `"}}]}]}`, dto.ChannelOtherSettings{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generation, parameters, apiErr := convert(t, tc.path, tc.body, tc.settings)
			require.Nil(t, apiErr)
			assert.Equal(t, tc.hd, generation.HD, generation.hdSkip)
			assert.NotContains(t, parameters, "hd", "NovelAI never gets the relay's hd")
		})
	}
	t.Run("an hd that is not a boolean is a final 400", func(t *testing.T) {
		_, _, apiErr := convert(t, generations, cat(`,"hd":"yes"`), dto.ChannelOtherSettings{})
		require.NotNil(t, apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
		assert.True(t, types.IsSkipRetryError(apiErr))
		assert.Contains(t, apiErr.Error(), "hd")
	})

	t.Run("the plan checks the body as sent", func(t *testing.T) {
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "a cat", N: 1})
		require.Nil(t, apiErr)
		sent := func(change func(body, parameters map[string]any)) io.Reader {
			var body map[string]any
			require.NoError(t, common.Unmarshal(mustMarshal(t, generation.Body), &body))
			change(body, body["parameters"].(map[string]any))
			outbound, closer, err := relaycommon.NewOutboundJSONBody(mustMarshal(t, body))
			require.NoError(t, err)
			t.Cleanup(func() { _ = closer.Close() })
			return outbound
		}
		plan, reason := planHD(sent(func(map[string]any, map[string]any) {}), generation)
		require.NotNil(t, plan, reason)
		assert.Equal(t, [4]int{1024, 1024, 1536, 1536}, [4]int{plan.baseWidth, plan.baseHeight, plan.width, plan.height})
		for name, change := range map[string]func(body, parameters map[string]any){
			"an override makes it an img2img":      func(body, _ map[string]any) { body["action"] = ActionImg2Img },
			"an override adds an image":            func(_, parameters map[string]any) { parameters["image"] = "iVBORw0KGgo=" },
			"an override adds reference images":    func(_, parameters map[string]any) { parameters["director_reference_images"] = []any{"dmliZQ=="} },
			"an override asks for transparency":    func(_, parameters map[string]any) { parameters["tag_hint_transparent_background"] = true },
			"an override moves the canvas past HD": func(_, parameters map[string]any) { parameters["width"], parameters["height"] = 1536, 1024 },
			"an override narrows the canvas":       func(_, parameters map[string]any) { parameters["width"], parameters["height"] = 448, 1024 },
		} {
			plan, reason := planHD(sent(change), generation)
			assert.Nil(t, plan, name)
			assert.NotEmpty(t, reason, name)
		}
		plan, reason = planHD(strings.NewReader(string(mustMarshal(t, generation.Body))), generation)
		assert.Nil(t, plan)
		assert.Equal(t, "body not replayable", reason, "the body before the override is not what NovelAI received")
		plan, _ = planHD(sent(func(_, parameters map[string]any) { parameters["padding"] = strings.Repeat("x", hdBodyLimit) }), generation)
		assert.Nil(t, plan, "a body past the limit")
	})
}

// An enhance source is the base image as NovelAI's client prepares it:
// stealth alpha cleaned (254 → 255, 1 → 0) on the colours as NovelAI sent
// them, flattened onto white, and scaled to exactly the HD canvas. A WebP base
// is read too; a base of another size than the canvas is refused.
func TestEnhanceSource(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{R: 10, G: 20, B: 30, A: 255}), image.Point{}, draw.Src)
	img.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 254})
	img.SetNRGBA(1, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 1})
	img.SetNRGBA(2, 0, color.NRGBA{A: 128})
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, img))
	base := buffer.Bytes()

	// At its own size the scaling keeps each pixel.
	source, err := enhanceSource(context.Background(), base, image.Pt(64, 64), 64, 64)
	require.NoError(t, err)
	flat := decodedBase64Image(t, source)
	pixel := func(x, y int) color.NRGBA { return color.NRGBAModel.Convert(flat.At(x, y)).(color.NRGBA) }
	assert.Equal(t, color.NRGBA{R: 200, G: 100, B: 50, A: 255}, pixel(0, 0), "alpha 254 is opaque, its colour as it was")
	assert.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, pixel(1, 0), "alpha 1 is transparent, white once flattened")
	assert.Equal(t, color.NRGBA{R: 127, G: 127, B: 127, A: 255}, pixel(2, 0), "half-transparent black on white")
	assert.Equal(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255}, pixel(3, 0))

	source, err = enhanceSource(context.Background(), base, image.Pt(64, 64), 96, 96)
	require.NoError(t, err)
	scaled := decodedBase64Image(t, source)
	assert.Equal(t, image.Pt(96, 96), scaled.Bounds().Size())
	opaque, ok := scaled.(interface{ Opaque() bool })
	assert.True(t, ok && opaque.Opaque())

	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	require.NoError(t, err)
	source, err = enhanceSource(context.Background(), webp, image.Pt(1, 1), 64, 64)
	require.NoError(t, err)
	assert.Equal(t, image.Pt(64, 64), decodedBase64Image(t, source).Bounds().Size(), "a WebP base")

	_, err = enhanceSource(context.Background(), base, image.Pt(128, 128), 192, 192)
	assert.EqualError(t, err, "the base image is 64x64, not the canvas")
}

// hdUpstream is NovelAI for the HD tests: it answers a generation with a zip
// of its n_samples PNGs at the canvas in hdBaseColor (or with base), and each
// enhance with the next queued answer, or a zip of one hdColor PNG at the
// enhance canvas. It records every request it got.
type hdUpstream struct {
	mu       sync.Mutex
	calls    []hdCall
	base     func(w http.ResponseWriter, r *http.Request, call hdCall)
	enhances []func(w http.ResponseWriter, r *http.Request, call hdCall)
}

type hdCall struct {
	path, contentType, authorization string
	acceptSent                       bool
	body                             map[string]any
}

func (c hdCall) parameter(key string) any {
	parameters, _ := c.body["parameters"].(map[string]any)
	return parameters[key]
}

func (c hdCall) canvas() (int, int) {
	width, _ := c.parameter("width").(float64)
	height, _ := c.parameter("height").(float64)
	return int(width), int(height)
}

func newHDUpstream(t *testing.T) (*hdUpstream, *httptest.Server) {
	upstream := &hdUpstream{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	return upstream, server
}

func (u *hdUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	call := hdCall{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), authorization: r.Header.Get("Authorization")}
	_, call.acceptSent = r.Header["Accept"]
	_ = common.Unmarshal(data, &call.body)
	u.mu.Lock()
	u.calls = append(u.calls, call)
	answer := u.base
	if call.body["action"] == ActionImg2Img {
		answer = nil
		if len(u.enhances) > 0 {
			answer, u.enhances = u.enhances[0], u.enhances[1:]
		}
	}
	u.mu.Unlock()
	if answer == nil {
		answer = hdAnswered
	}
	answer(w, r, call)
}

// hdAnswered is NovelAI's answer to call: its n_samples base images, or the
// HD image of an enhance.
func hdAnswered(w http.ResponseWriter, _ *http.Request, call hdCall) {
	width, height := call.canvas()
	var images [][]byte
	if call.body["action"] == ActionImg2Img {
		images = append(images, hdPNG(width, height, hdColor))
	} else {
		samples, _ := call.parameter("n_samples").(float64)
		for i := range int(samples) {
			images = append(images, hdPNG(width, height, hdBaseColor(i)))
		}
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(hdZip(images...))
}

// hdAnswer is a fixed answer.
func hdAnswer(status int, contentType string, body []byte) func(http.ResponseWriter, *http.Request, hdCall) {
	return func(w http.ResponseWriter, _ *http.Request, _ hdCall) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func (u *hdUpstream) enhance(answers ...func(http.ResponseWriter, *http.Request, hdCall)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.enhances = append(u.enhances, answers...)
}

func (u *hdUpstream) recorded() []hdCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.calls)
}

// hdRun is a request the adaptor answered (runHD).
type hdRun struct {
	c        *gin.Context
	recorder *httptest.ResponseRecorder
	info     *relaycommon.RelayInfo
	adaptor  *Adaptor
	resp     *http.Response
	usage    *dto.Usage
	err      error
	apiErr   *types.NewAPIError
}

// runHD sends an OpenAI images generation, or a chat completion on
// /v1/chat/completions, through the adaptor as the relays do: the converted
// request as the replayable outbound body, DoRequest, and DoResponse when the
// base answered 200. ctx is the client's request; setup may change the
// context (its writer) before the send.
func runHD(t *testing.T, ctx context.Context, meta relaycommon.ChannelMeta, path, body string, setup ...func(*gin.Context)) hdRun {
	t.Helper()
	savedTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = savedTimeout })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	meta.ChannelType = constant.ChannelTypeNovelAI
	run := hdRun{c: c, recorder: recorder, adaptor: &Adaptor{}}
	run.info = &relaycommon.RelayInfo{ChannelMeta: &meta, RelayFormat: types.RelayFormatOpenAIImage, RelayMode: relayconstant.RelayModeImagesGenerations}
	var converted any
	var err error
	if path == "/v1/chat/completions" {
		request := &dto.GeneralOpenAIRequest{}
		require.NoError(t, common.UnmarshalBodyReusable(c, request))
		run.info.RelayMode, run.info.RelayFormat = relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI
		run.info.IsStream = request.Stream != nil && *request.Stream
		run.info.ShouldIncludeUsage, run.info.UpstreamModelName = run.info.IsStream, request.Model
		converted, err = run.adaptor.ConvertOpenAIRequest(c, run.info, request)
	} else {
		request := &dto.ImageRequest{}
		require.NoError(t, common.UnmarshalBodyReusable(c, request))
		run.info.IsStream, run.info.UpstreamModelName = request.Stream != nil && *request.Stream, request.Model
		run.info.ImageRequestCount, err = request.ImageCount(false)
		require.NoError(t, err)
		converted, err = run.adaptor.ConvertImageRequest(c, run.info, *request)
	}
	require.NoError(t, err)
	outbound, closer, err := relaycommon.NewOutboundJSONBody(mustMarshal(t, converted))
	require.NoError(t, err)
	defer closer.Close()
	for _, change := range setup {
		change(c)
	}
	resp, err := run.adaptor.DoRequest(c, run.info, outbound)
	if run.err = err; err != nil {
		return run
	}
	run.resp = resp.(*http.Response)
	if run.resp.StatusCode == http.StatusOK {
		usage, apiErr := run.adaptor.DoResponse(c, run.resp, run.info)
		run.apiErr = apiErr
		if usage != nil {
			run.usage = usage.(*dto.Usage)
		}
	}
	return run
}

type hdImagesResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		Seed    int64  `json:"seed"`
		Size    string `json:"size"`
	} `json:"data"`
	Size         string `json:"size"`
	OutputFormat string `json:"output_format"`
	Usage        struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// images is the images response the client got, and each image decoded.
func (r hdRun) images(t *testing.T) (hdImagesResponse, [][]byte) {
	t.Helper()
	require.Nil(t, r.apiErr)
	require.Equal(t, http.StatusOK, r.recorder.Code, r.recorder.Body.String())
	var response hdImagesResponse
	require.NoError(t, common.Unmarshal(r.recorder.Body.Bytes(), &response))
	var images [][]byte
	for _, item := range response.Data {
		data, err := base64.StdEncoding.DecodeString(item.B64JSON)
		require.NoError(t, err)
		images = append(images, data)
	}
	return response, images
}

// sizeOf is the size of image data in OpenAI's WIDTHxHEIGHT form.
func sizeOf(t *testing.T, data []byte) string {
	t.Helper()
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	return fmt.Sprintf("%dx%d", config.Width, config.Height)
}

// resetHD restores HD's timeouts and forgets its cooldowns after a test.
func resetHD(t *testing.T) {
	t.Helper()
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	savedTimeout, savedBudget, savedCooldown := enhanceTimeout, hdBudget, hdCooldown
	t.Cleanup(func() {
		enhanceTimeout, hdBudget, hdCooldown = savedTimeout, savedBudget, savedCooldown
		hdCannotPay.Clear()
	})
}

// A text-to-image request is enhanced image by image once NovelAI answered
// it: one img2img per image to /ai/generate-image, as JSON with the key and
// no Accept, at the HD canvas, the response reporting each image's real size
// and its base seed, at the base canvas's usage (HD costs the user nothing
// more), and the consume log naming the HD canvas. Any failure delivers the
// base image after one call and ends HD for the rest of the request; NovelAI's
// 402 also skips HD on the key for the cooldown, which a balance of the Anlas
// the refused enhance needed lifts. A client that leaves during an enhance
// gets nothing more and keeps the reservation.
func TestImagesResponseHD(t *testing.T) {
	resetHD(t)
	const prompt = `{"model":"nai-diffusion-4-5-full","prompt":"1girl","size":"1216x832","seed":42`
	upstream, server := newHDUpstream(t)
	generate := func(t *testing.T, key, extra string) hdRun {
		t.Helper()
		return runHD(t, context.Background(), relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: key}, "/v1/images/generations", prompt+extra+"}")
	}
	unavailable := []*common.Message{common.NewMessage("HD unavailable, delivered at the standard size")}
	fellBack := func(t *testing.T, run hdRun, reason string) {
		t.Helper()
		assert.Equal(t, unavailable, run.adaptor.ConsumeLogContent())
		diagnostics := run.info.ConversionDiagnostics()
		require.Len(t, diagnostics, 1)
		assert.Equal(t, "novelai_hd_fallback", diagnostics[0].Code)
		assert.Contains(t, diagnostics[0].Message, reason)
	}

	t.Run("an image enhanced at the same price", func(t *testing.T) {
		before := len(upstream.recorded())
		run := generate(t, "pst-hd-ok", "")

		calls := upstream.recorded()[before:]
		require.Len(t, calls, 2)
		enhance := calls[1]
		assert.Equal(t, "/ai/generate-image", enhance.path)
		assert.Equal(t, "application/json", enhance.contentType)
		assert.False(t, enhance.acceptSent)
		assert.Equal(t, "Bearer pst-hd-ok", enhance.authorization)
		assert.Equal(t, ActionImg2Img, enhance.body["action"])
		assert.Equal(t, [3]any{1856.0, 1280.0, 1.0}, [3]any{enhance.parameter("width"), enhance.parameter("height"), enhance.parameter("n_samples")})
		assert.Equal(t, float64(hdSeed(42)), enhance.parameter("seed"))
		response, images := run.images(t)
		assert.Equal(t, "1856x1280", response.Size)
		assert.Equal(t, "1856x1280", response.Data[0].Size)
		assert.Equal(t, hdPNG(1856, 1280, hdColor), images[0])
		assert.Equal(t, int64(42), response.Data[0].Seed, "the base seed, from which the enhance seed follows")
		assert.Equal(t, 1245, response.Usage.OutputTokens, "the 1216x832 image the request is priced at")
		assert.Equal(t, 1, run.info.SettledImageCount())
		assert.Equal(t, []*common.Message{common.NewMessage("HD {{size}}", map[string]any{"size": "1856x1280"})}, run.adaptor.ConsumeLogContent())
		assert.Empty(t, run.info.ConversionDiagnostics())
	})

	t.Run("an account without Anlas gets the standard image and a cooldown a balance lifts", func(t *testing.T) {
		const key = "pst-hd-402"
		upstream.enhance(hdAnswer(http.StatusPaymentRequired, "application/json", []byte(`{"statusCode":402,"message":"Insufficient Anlas"}`)))
		before := len(upstream.recorded())
		run := generate(t, key, "")

		assert.Len(t, upstream.recorded(), before+2)
		response, images := run.images(t)
		assert.Equal(t, "1216x832", response.Size)
		assert.Equal(t, "1216x832", response.Data[0].Size)
		assert.Equal(t, hdPNG(1216, 832, hdBaseColor(0)), images[0])
		assert.Equal(t, 1245, response.Usage.OutputTokens)
		fellBack(t, run, "NovelAI answered 402 (insufficient Anlas); 0 of 1 images enhanced at 1856x1280 (about 20 Anlas each); HD off for this key until ")
		refusal, refused := hdRefused(key)
		require.True(t, refused)
		assert.Equal(t, 20, refusal.needed)

		before = len(upstream.recorded())
		run = generate(t, key, "")
		assert.Len(t, upstream.recorded(), before+1, "no enhance while the key cannot pay")
		fellBack(t, run, "marked the key out of Anlas until")

		NoteAnlas(key, 5)
		before = len(upstream.recorded())
		generate(t, key, "")
		assert.Len(t, upstream.recorded(), before+1, "5 Anlas do not pay a 20-Anlas enhance")

		NoteAnlas(key, 20)
		before = len(upstream.recorded())
		run = generate(t, key, "")
		assert.Len(t, upstream.recorded(), before+2)
		response, _ = run.images(t)
		assert.Equal(t, "1856x1280", response.Size)
	})

	for _, tc := range []struct {
		name        string
		answer      func(http.ResponseWriter, *http.Request, hdCall)
		reason      string
		outputExtra string
	}{
		{"a 500", hdAnswer(http.StatusInternalServerError, "application/json", []byte(`{"statusCode":500,"message":"Error generating image"}`)), "NovelAI answered HTTP 500: Error generating image", ""},
		{"a 400 with a message", hdAnswer(http.StatusBadRequest, "application/json", []byte(`{"statusCode":400,"message":"bad canvas"}`)), "NovelAI answered HTTP 400: bad canvas", ""},
		{"a 200 that is not a zip", hdAnswer(http.StatusOK, "text/plain", []byte("not an image")), "NovelAI's answer held no image", ""},
		{"an answer at another size", hdAnswer(http.StatusOK, "application/zip", hdZip(hdPNG(1216, 832, hdColor))), "NovelAI's answer is 1216x832, not the HD canvas 1856x1280", ""},
		// A well-headed PNG that does not decode cannot become a JPEG.
		{"an HD image that does not render", hdAnswer(http.StatusOK, "application/zip", hdZip(pngHeader(1856, 1280, 8, 0))), "the HD image could not be rendered", `,"output_format":"jpeg"`},
	} {
		t.Run(tc.name+" delivers the standard images after one call", func(t *testing.T) {
			upstream.enhance(tc.answer)
			before := len(upstream.recorded())
			run := generate(t, "pst-hd-failed", `,"n":2`+tc.outputExtra)

			assert.Len(t, upstream.recorded(), before+2, "one enhance, never sent again, and none for the second image")
			response, images := run.images(t)
			assert.Equal(t, "1216x832", response.Size)
			require.Len(t, images, 2)
			for i, data := range images {
				assert.Equal(t, "1216x832", response.Data[i].Size)
				assert.Equal(t, "1216x832", sizeOf(t, data))
			}
			assert.Equal(t, 2, run.info.SettledImageCount())
			fellBack(t, run, tc.reason)
			_, refused := hdRefused("pst-hd-failed")
			assert.False(t, refused, "only a 402 starts the cooldown")
		})
	}

	t.Run("a base of another size than the canvas is not sent", func(t *testing.T) {
		upstream.mu.Lock()
		upstream.base = hdAnswer(http.StatusOK, "application/zip", hdZip(hdPNG(512, 512, hdBaseColor(0))))
		upstream.mu.Unlock()
		t.Cleanup(func() {
			upstream.mu.Lock()
			upstream.base = nil
			upstream.mu.Unlock()
		})
		before := len(upstream.recorded())
		run := generate(t, "pst-hd-resized", "")

		assert.Len(t, upstream.recorded(), before+1)
		_, images := run.images(t)
		assert.Equal(t, "512x512", sizeOf(t, images[0]))
		fellBack(t, run, "the base image is 512x512, not the canvas")
	})

	t.Run("the images after a refused one are standard", func(t *testing.T) {
		upstream.enhance(hdAnswered, hdAnswer(http.StatusPaymentRequired, "application/json", []byte(`{"statusCode":402,"message":"Not enough Anlas"}`)))
		before := len(upstream.recorded())
		run := generate(t, "pst-hd-mixed", `,"n":3`)

		calls := upstream.recorded()[before:]
		require.Len(t, calls, 3, "the base and two enhances, the third image untried")
		assert.Equal(t, [2]any{float64(hdSeed(42)), float64(hdSeed(43))}, [2]any{calls[1].parameter("seed"), calls[2].parameter("seed")})
		response, images := run.images(t)
		assert.Equal(t, "1856x1280", response.Size, "the first image's size")
		assert.Equal(t, []string{"1856x1280", "1216x832", "1216x832"}, []string{response.Data[0].Size, response.Data[1].Size, response.Data[2].Size})
		assert.Equal(t, hdPNG(1856, 1280, hdColor), images[0])
		assert.Equal(t, hdPNG(1216, 832, hdBaseColor(1)), images[1])
		assert.Equal(t, hdPNG(1216, 832, hdBaseColor(2)), images[2])
		assert.Equal(t, []int64{42, 43, 44}, []int64{response.Data[0].Seed, response.Data[1].Seed, response.Data[2].Seed})
		assert.Equal(t, 3*1245, response.Usage.OutputTokens)
		assert.Equal(t, 3, run.info.SettledImageCount())
		assert.Equal(t, []*common.Message{common.NewMessage("HD {{size}} for {{count}} of {{total}} images", map[string]any{"size": "1856x1280", "count": 1, "total": 3})}, run.adaptor.ConsumeLogContent())
	})

	t.Run("an enhance that takes too long", func(t *testing.T) {
		enhanceTimeout = 50 * time.Millisecond
		t.Cleanup(func() { enhanceTimeout = 60 * time.Second })
		upstream.enhance(func(w http.ResponseWriter, r *http.Request, call hdCall) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		})
		run := generate(t, "pst-hd-slow", "")

		_, images := run.images(t)
		assert.Equal(t, "1216x832", sizeOf(t, images[0]))
		fellBack(t, run, "the enhance call timed out after 0.05s")
	})

	t.Run("no enhance once the budget is used up", func(t *testing.T) {
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "1girl", Size: "1216x832", N: 2})
		require.Nil(t, apiErr)
		outbound, closer, err := relaycommon.NewOutboundJSONBody(mustMarshal(t, generation.Body))
		require.NoError(t, err)
		defer closer.Close()
		plan, reason := planHD(outbound, generation)
		require.NotNil(t, plan, reason)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: server.URL, ApiKey: "pst-hd-budget"}}
		e := &enhancer{c: c, info: info, adaptor: &Adaptor{generation: generation}, plan: plan, release: func() {}}
		before := len(upstream.recorded())
		hd, err := e.image(0, hdPNG(1216, 832, hdBaseColor(0)))
		require.NoError(t, err)
		require.NotNil(t, hd)

		e.started = e.started.Add(-hdBudget)
		hd, err = e.image(1, hdPNG(1216, 832, hdBaseColor(1)))
		require.NoError(t, err)
		assert.Nil(t, hd)
		assert.Len(t, upstream.recorded(), before+1, "no call for the second image")
		assert.Equal(t, "the HD budget of 120s was used up", e.stopped)
	})

	t.Run("a client gone when the images are written", func(t *testing.T) {
		run := runHD(t, context.Background(), relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "pst-hd-gone"}, "/v1/images/generations", prompt+"}", func(c *gin.Context) {
			c.Writer = &failingWriter{ResponseWriter: c.Writer}
		})

		require.Nil(t, run.apiErr)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, run.info.StreamStatus.EndReason)
		assert.Nil(t, run.info.BillingImageCount, "the reservation is billed")
		assert.Empty(t, run.adaptor.ConsumeLogContent(), "the log names no HD the client did not get")
	})

	t.Run("a client that leaves during the enhance", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var cancelled atomic.Bool
		upstream.enhance(func(w http.ResponseWriter, r *http.Request, call hdCall) {
			cancel()
			select {
			case <-r.Context().Done():
				cancelled.Store(true)
			case <-time.After(5 * time.Second):
			}
		})
		run := runHD(t, ctx, relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "pst-hd-left"}, "/v1/images/generations", prompt+"}")

		require.Nil(t, run.apiErr)
		assert.Zero(t, run.recorder.Body.Len(), "nothing is written")
		assert.Nil(t, run.info.BillingImageCount, "the reservation is billed")
		assert.Equal(t, run.adaptor.generation.Tokens.Usage(1), run.usage)
		require.NotNil(t, run.info.StreamStatus)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, run.info.StreamStatus.EndReason)
		assert.Empty(t, run.adaptor.ConsumeLogContent())
		assert.Eventually(t, cancelled.Load, 5*time.Second, time.Millisecond, "NovelAI's request was cancelled")
	})
}

// eventLog is a client that keeps what it was sent, so that a test can
// compare what the client had when NovelAI got a call.
type eventLog struct {
	gin.ResponseWriter
	mu      sync.Mutex
	written strings.Builder
}

func (w *eventLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.written.Write(data)
	w.mu.Unlock()
	return w.ResponseWriter.Write(data)
}

func (w *eventLog) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *eventLog) count(needle string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Count(w.written.String(), needle)
}

// counting is an enhance answer that records how often needle had reached
// the client when the enhance came, then answers as NovelAI does.
func counting(log *eventLog, needle string, seen *[]int, mu *sync.Mutex) func(http.ResponseWriter, *http.Request, hdCall) {
	return func(w http.ResponseWriter, r *http.Request, call hdCall) {
		mu.Lock()
		*seen = append(*seen, log.count(needle))
		mu.Unlock()
		hdAnswered(w, r, call)
	}
}

// An images stream with HD relays the base pass's previews as they come,
// then each image's completed event right after its enhance, with its own
// size, the usage on the last one: no completed event before the base
// stream ended, and the second enhance only once the first image went out. A
// base stream that broke, or a final that is no image, gets no HD and ends
// as today. A client that leaves during an enhance keeps the reservation,
// and a whole answer to a stream request is enhanced too.
func TestImagesStreamHD(t *testing.T) {
	resetHD(t)
	upstream, server := newHDUpstream(t)
	preview := base64.StdEncoding.EncodeToString(testImage(8, 8, solid(color.Gray{Y: 9})))
	final := func(sample int, data []byte) map[string]any {
		return map[string]any{"event_type": "final", "samp_ix": sample, "image": base64.StdEncoding.EncodeToString(data)}
	}
	answerWith := func(t *testing.T, contentType, body string) {
		upstream.mu.Lock()
		upstream.base = hdAnswer(http.StatusOK, contentType, []byte(body))
		upstream.mu.Unlock()
		t.Cleanup(func() {
			upstream.mu.Lock()
			upstream.base = nil
			upstream.mu.Unlock()
		})
	}
	meta := func(key string) relaycommon.ChannelMeta {
		return relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: key}
	}
	const request = `{"model":"nai-diffusion-4-5-full","prompt":"1girl","size":"512x512","n":2,"stream":true,"partial_images":2}`

	t.Run("previews first, then each image after its enhance", func(t *testing.T) {
		answerWith(t, "text/event-stream", sseEvents(t,
			map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": 7, "image": preview},
			map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": 15, "image": preview},
			final(0, hdPNG(512, 512, hdBaseColor(0))), final(1, hdPNG(512, 512, hdBaseColor(1)))))
		log := &eventLog{}
		var mu sync.Mutex
		var seen []int
		upstream.enhance(counting(log, "event: image_generation.completed", &seen, &mu), counting(log, "event: image_generation.completed", &seen, &mu))
		run := runHD(t, context.Background(), meta("pst-hd-stream"), "/v1/images/generations", request, func(c *gin.Context) {
			log.ResponseWriter = c.Writer
			c.Writer = log
		})

		require.Nil(t, run.apiErr)
		events := streamedEvents(t, run.recorder.Body.String())
		require.Len(t, events, 4)
		names := []string{events[0].name, events[1].name, events[2].name, events[3].name}
		assert.Equal(t, []string{"image_generation.partial_image", "image_generation.partial_image", "image_generation.completed", "image_generation.completed"}, names)
		assert.Equal(t, "8x8", events[0].data["size"], "a preview has its own size")
		for i, event := range events[2:] {
			assert.Equal(t, "768x768", event.data["size"])
			data, err := base64.StdEncoding.DecodeString(event.data["b64_json"].(string))
			require.NoError(t, err)
			assert.Equal(t, hdPNG(768, 768, hdColor), data, "image %d", i)
		}
		assert.Equal(t, float64(0), events[2].data["usage"].(map[string]any)["output_tokens"])
		assert.Equal(t, float64(2*323), events[3].data["usage"].(map[string]any)["output_tokens"], "the base canvas's usage of both images")
		mu.Lock()
		assert.Equal(t, []int{0, 1}, seen, "no image before the base stream ended, and the second enhance after the first image")
		mu.Unlock()
		assert.Equal(t, 2, *run.info.BillingImageCount)
		assert.Equal(t, []*common.Message{common.NewMessage("HD {{size}}", map[string]any{"size": "768x768"})}, run.adaptor.ConsumeLogContent())
	})

	t.Run("a base stream that broke gets no HD", func(t *testing.T) {
		answerWith(t, "text/event-stream", sseEvents(t, final(0, hdPNG(512, 512, hdBaseColor(0))), map[string]any{"event_type": "error", "message": "Error generating image"}))
		before := len(upstream.recorded())
		run := runHD(t, context.Background(), meta("pst-hd-broken"), "/v1/images/generations", request)

		require.Nil(t, run.apiErr)
		assert.Len(t, upstream.recorded(), before+1)
		events := streamedEvents(t, run.recorder.Body.String())
		require.Len(t, events, 2)
		assert.Equal(t, "image_generation.completed", events[0].name)
		assert.Equal(t, "512x512", events[0].data["size"])
		assert.Contains(t, events[1].data["error"].(map[string]any)["message"], "Error generating image")
		assert.Equal(t, 1, *run.info.BillingImageCount)
		assert.Empty(t, run.adaptor.ConsumeLogContent(), "HD was not tried")
	})

	t.Run("a final that is no image ends the stream when it arrives", func(t *testing.T) {
		answerWith(t, "text/event-stream", sseEvents(t, final(0, []byte("not an image")), final(1, hdPNG(512, 512, hdBaseColor(1)))))
		before := len(upstream.recorded())
		run := runHD(t, context.Background(), meta("pst-hd-bad-final"), "/v1/images/generations", request)

		require.NotNil(t, run.apiErr)
		assert.Contains(t, run.apiErr.Error(), "a final event without an image")
		assert.Len(t, upstream.recorded(), before+1, "no enhance")
	})

	t.Run("a client that leaves during the enhance", func(t *testing.T) {
		answerWith(t, "text/event-stream", sseEvents(t, final(0, hdPNG(512, 512, hdBaseColor(0))), final(1, hdPNG(512, 512, hdBaseColor(1)))))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		upstream.enhance(func(w http.ResponseWriter, r *http.Request, call hdCall) {
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		})
		before := len(upstream.recorded())
		run := runHD(t, ctx, meta("pst-hd-stream-left"), "/v1/images/generations", request)

		require.Nil(t, run.apiErr)
		assert.Len(t, upstream.recorded(), before+2)
		assert.Equal(t, relaycommon.StreamEndReasonClientGone, run.info.StreamStatus.EndReason)
		assert.Nil(t, run.info.BillingImageCount, "the reservation is billed")
		assert.Equal(t, 2, run.info.SettledImageCount())
		assert.Empty(t, run.adaptor.ConsumeLogContent())
	})

	t.Run("a whole answer to a stream request", func(t *testing.T) {
		run := runHD(t, context.Background(), meta("pst-hd-whole"), "/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"1girl","size":"512x512","stream":true}`)

		require.Nil(t, run.apiErr)
		events := streamedEvents(t, run.recorder.Body.String())
		require.Len(t, events, 1)
		assert.Equal(t, "768x768", events[0].data["size"])
		assert.Equal(t, float64(323), events[0].data["usage"].(map[string]any)["output_tokens"])
		assert.True(t, run.info.PerformanceWholeOutput)
	})
}

// A chat gets HD as the images routes do: the Markdown images are the HD
// ones, a stream sends each image's chunk right after its enhance, and the
// usage is the base canvas's. A refused enhance delivers the base PNG.
func TestChatResponseHD(t *testing.T) {
	resetHD(t)
	upstream, server := newHDUpstream(t)
	meta := func(key string) relaycommon.ChannelMeta {
		return relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: key}
	}
	markdownSizes := func(t *testing.T, content string) []string {
		t.Helper()
		var sizes []string
		for _, part := range strings.Split(content, "![image](data:image/png;base64,")[1:] {
			encoded, _, ok := strings.Cut(part, ")")
			require.True(t, ok)
			data, err := base64.StdEncoding.DecodeString(encoded)
			require.NoError(t, err)
			sizes = append(sizes, sizeOf(t, data))
		}
		return sizes
	}
	const messages = `"model":"nai-diffusion-4-5-full","size":"512x512","messages":[{"role":"user","content":"1girl"}]`

	t.Run("one message of HD images", func(t *testing.T) {
		run := runHD(t, context.Background(), meta("pst-hd-chat"), "/v1/chat/completions", `{`+messages+`,"n":2}`)

		require.Nil(t, run.apiErr)
		var response dto.OpenAITextResponse
		require.NoError(t, common.Unmarshal(run.recorder.Body.Bytes(), &response))
		assert.Equal(t, []string{"768x768", "768x768"}, markdownSizes(t, response.Choices[0].Message.StringContent()))
		assert.Equal(t, 2*323, response.Usage.CompletionTokens)
		assert.Equal(t, []*common.Message{common.NewMessage("HD {{size}}", map[string]any{"size": "768x768"})}, run.adaptor.ConsumeLogContent())
	})

	t.Run("a stream sends each image once enhanced", func(t *testing.T) {
		log := &eventLog{}
		var mu sync.Mutex
		var seen []int
		upstream.enhance(counting(log, "![image]", &seen, &mu), counting(log, "![image]", &seen, &mu))
		run := runHD(t, context.Background(), meta("pst-hd-chat-stream"), "/v1/chat/completions", `{`+messages+`,"n":2,"stream":true}`, func(c *gin.Context) {
			log.ResponseWriter = c.Writer
			c.Writer = log
		})

		require.Nil(t, run.apiErr)
		events := streamedEvents(t, run.recorder.Body.String())
		require.Len(t, events, 6, "role, two images, stop, usage, [DONE]")
		delta := func(i int) map[string]any {
			return events[i].data["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		}
		assert.Equal(t, "assistant", delta(0)["role"])
		assert.Equal(t, []string{"768x768"}, markdownSizes(t, delta(1)["content"].(string)))
		assert.Equal(t, []string{"768x768"}, markdownSizes(t, delta(2)["content"].(string)))
		assert.Equal(t, "stop", events[3].data["choices"].([]any)[0].(map[string]any)["finish_reason"])
		assert.Equal(t, float64(2*323), events[4].data["usage"].(map[string]any)["completion_tokens"])
		assert.Equal(t, true, events[5].data["[DONE]"])
		mu.Lock()
		assert.Equal(t, []int{0, 1}, seen, "the first image went out before the second enhance")
		mu.Unlock()
	})

	t.Run("a refused enhance delivers the standard PNG", func(t *testing.T) {
		upstream.enhance(hdAnswer(http.StatusPaymentRequired, "application/json", []byte(`{"statusCode":402,"message":"Insufficient Anlas"}`)))
		run := runHD(t, context.Background(), meta("pst-hd-chat-402"), "/v1/chat/completions", `{`+messages+`}`)

		require.Nil(t, run.apiErr)
		var response dto.OpenAITextResponse
		require.NoError(t, common.Unmarshal(run.recorder.Body.Bytes(), &response))
		assert.Equal(t, []string{"512x512"}, markdownSizes(t, response.Choices[0].Message.StringContent()))
		assert.Equal(t, []*common.Message{common.NewMessage("HD unavailable, delivered at the standard size")}, run.adaptor.ConsumeLogContent())
	})
}

// On NovelAI's own host a request with HD keeps its key from the base send
// through the last enhance answer, as one job in the key's queue: another
// request waits behind both passes, and n images are one base call and n
// enhances in order on the same key. The key is free once NovelAI's last
// answer arrived, before the client got anything; after a failed enhance;
// after a base answer the relay does not take (any status but 200, a 202
// included, so a retry on the key does not wait behind its own attempt); and
// when the client leaves. A refused enhance marks the key as a refused base
// pass does.
func TestAdaptorHoldsTheSlotAcrossHD(t *testing.T) {
	resetGenerationQueue(t)
	resetHD(t)
	upstream, server := newHDUpstream(t)
	official := func(key string, limit int) relaycommon.ChannelMeta {
		return relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNovelAI, ChannelBaseUrl: "http://image.novelai.net", ApiKey: key,
			ChannelSetting: dto.ChannelSettings{Proxy: server.URL}, ChannelOtherSettings: dto.ChannelOtherSettings{NovelAIQueueLimit: &limit}}
	}
	const request = `{"model":"nai-diffusion-4-5-full","prompt":"1girl","size":"512x512","seed":7`
	runs := make(chan hdRun, 2)
	send := func(key string, limit int, extra string) {
		go func() {
			runs <- runHD(t, context.Background(), official(key, limit), "/v1/images/generations", request+extra+"}")
		}()
	}
	awaitRun := func(t *testing.T) hdRun {
		t.Helper()
		select {
		case run := <-runs:
			return run
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the request did not end")
			return hdRun{}
		}
	}
	gate := func(entered chan<- struct{}, open <-chan struct{}) func(http.ResponseWriter, *http.Request, hdCall) {
		return func(w http.ResponseWriter, r *http.Request, call hdCall) {
			entered <- struct{}{}
			<-open
			hdAnswered(w, r, call)
		}
	}

	t.Run("a request waits behind both passes of the one before", func(t *testing.T) {
		const key = "pst-hd-hold"
		entered, open := make(chan struct{}, 1), make(chan struct{})
		// A failed check must not leave the enhance waiting: the server
		// would never close.
		release := sync.OnceFunc(func() { close(open) })
		t.Cleanup(release)
		upstream.enhance(gate(entered, open))
		before := len(upstream.recorded())
		send(key, 1, "")
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the enhance never came")
		}
		assert.False(t, keyIsFree(key), "the key is held during the enhance")
		probe := official(key, 0)
		apiErr := rejectedAtOnce(t, &relaycommon.RelayInfo{ChannelMeta: &probe})
		assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode, "another request finds the key busy")
		send(key, 1, "")
		require.Eventually(t, func() bool { return waitingFor(key) == 1 }, 5*time.Second, time.Millisecond)

		release()
		for range 2 {
			run := awaitRun(t)
			require.NoError(t, run.err)
			response, _ := run.images(t)
			assert.Equal(t, "768x768", response.Size)
		}
		calls := upstream.recorded()[before:]
		require.Len(t, calls, 4)
		assert.Equal(t, []any{ActionGenerate, ActionImg2Img, ActionGenerate, ActionImg2Img}, []any{calls[0].body["action"], calls[1].body["action"], calls[2].body["action"], calls[3].body["action"]},
			"the second request reached NovelAI after the first one's enhance answered")
		assert.True(t, keyIsFree(key))
	})

	t.Run("n images are one base and n enhances in order on the key", func(t *testing.T) {
		before := len(upstream.recorded())
		send("pst-hd-batch", 0, `,"n":2`)
		run := awaitRun(t)
		require.NoError(t, run.err)

		calls := upstream.recorded()[before:]
		require.Len(t, calls, 3)
		assert.Equal(t, []any{ActionGenerate, ActionImg2Img, ActionImg2Img}, []any{calls[0].body["action"], calls[1].body["action"], calls[2].body["action"]})
		assert.Equal(t, []any{float64(hdSeed(7)), float64(hdSeed(8))}, []any{calls[1].parameter("seed"), calls[2].parameter("seed")})
		for _, call := range calls {
			assert.Equal(t, "Bearer pst-hd-batch", call.authorization)
		}
	})

	t.Run("the key is free once NovelAI's last answer arrived", func(t *testing.T) {
		const key = "pst-hd-stalled"
		// A client that reads nothing: the response write waits.
		client := &stalledClient{entered: make(chan struct{}), unblock: make(chan struct{})}
		go func() {
			runs <- runHD(t, context.Background(), official(key, 0), "/v1/images/generations", request+"}", func(c *gin.Context) {
				client.ResponseWriter = c.Writer
				c.Writer = client
			})
		}()
		select {
		case <-client.entered:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the relay never wrote to its client")
		}
		assert.True(t, keyIsFree(key), "free while the client has read nothing")
		close(client.unblock)
		awaitRun(t)
	})

	t.Run("a failed enhance frees the key", func(t *testing.T) {
		upstream.enhance(hdAnswer(http.StatusInternalServerError, "application/json", []byte(`{"statusCode":500,"message":"Error generating image"}`)))
		send("pst-hd-free-failed", 0, `,"n":2`)
		run := awaitRun(t)
		require.NoError(t, run.err)
		assert.True(t, keyIsFree("pst-hd-free-failed"))
	})

	t.Run("a key HD skips for its 402 is free once the base answer ended", func(t *testing.T) {
		const key = "pst-hd-cooling"
		now := time.Now()
		hdCannotPay.Store(sha256.Sum256([]byte(key)), hdRefusal{at: now, until: now.Add(time.Minute), needed: 5})
		generation, apiErr := BuildGeneration(nil, GenerationIntent{Model: "nai-diffusion-4-5-full", Prompt: "1girl", Size: "512x512", N: 1})
		require.Nil(t, apiErr)
		outbound, closer, err := relaycommon.NewOutboundJSONBody(mustMarshal(t, generation.Body))
		require.NoError(t, err)
		defer closer.Close()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		meta := official(key, 0)
		info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations, RelayFormat: types.RelayFormatOpenAIImage, ChannelMeta: &meta}
		adaptor := &Adaptor{generation: generation, output: ImageOutput{ResponseFormat: "b64_json", OutputFormat: formatPNG}}
		resp, err := adaptor.DoRequest(c, info, outbound)
		require.NoError(t, err)

		// The relay has not taken the answer yet.
		assert.Eventually(t, func() bool { return keyIsFree(key) }, 5*time.Second, time.Millisecond, "as without HD")
		_, apiErr = adaptor.DoResponse(c, resp.(*http.Response), info)
		require.Nil(t, apiErr)
		assert.Contains(t, recorder.Body.String(), `"size":"512x512"`)
		assert.Equal(t, []*common.Message{common.NewMessage("HD unavailable, delivered at the standard size")}, adaptor.ConsumeLogContent())
	})

	t.Run("a refused enhance marks the key", func(t *testing.T) {
		const key = "pst-hd-refused"
		upstream.enhance(hdAnswer(http.StatusPaymentRequired, "application/json", []byte(`{"statusCode":402,"message":"Insufficient Anlas"}`)))
		send(key, 0, "")
		awaitRun(t)
		generationLinesMu.Lock()
		_, refused := generationRefused[sha256.Sum256([]byte(key))]
		generationLinesMu.Unlock()
		assert.True(t, refused)
	})

	for _, status := range []int{http.StatusInternalServerError, http.StatusAccepted} {
		t.Run(fmt.Sprintf("a %d base frees the key at once", status), func(t *testing.T) {
			key := fmt.Sprintf("pst-hd-base-%d", status)
			upstream.mu.Lock()
			upstream.base = hdAnswer(status, "application/json", []byte(`{"statusCode":500,"message":"Error generating image"}`))
			upstream.mu.Unlock()
			t.Cleanup(func() {
				upstream.mu.Lock()
				upstream.base = nil
				upstream.mu.Unlock()
			})
			run := runHD(t, context.Background(), official(key, 0), "/v1/images/generations", request+"}")
			require.NoError(t, run.err)
			assert.Equal(t, status, run.resp.StatusCode)
			// The retry, on the same key and client request: a held key would
			// fail it with a 429 at once.
			resp, err := run.adaptor.DoRequest(run.c, run.info, strings.NewReader(`{}`))
			require.NoError(t, err, "the attempt left the key free")
			require.NoError(t, resp.(*http.Response).Body.Close())
		})
	}

	t.Run("a client that leaves mid-HD frees the key", func(t *testing.T) {
		const key = "pst-hd-free-left"
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		upstream.enhance(func(w http.ResponseWriter, r *http.Request, call hdCall) {
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		})
		run := runHD(t, ctx, official(key, 0), "/v1/images/generations", request+"}")
		require.NoError(t, run.err)
		assert.Eventually(t, func() bool { return keyIsFree(key) }, 5*time.Second, time.Millisecond)
	})
}

// An enhance's preparation takes its memory of imageWork, exactly the
// formula's weight for the decode, the flattened base, the resize's columns,
// the canvas and its encoding, and waits while other work holds it: past the
// HD budget the image is delivered without a call.
func TestEnhanceBoundsImageWork(t *testing.T) {
	resetHD(t)
	upstream, server := newHDUpstream(t)
	// A 512x512 base (an RGB PNG decodes at 4 bytes a pixel) to 768x768.
	const w, h, width, height = 512, 512, 768, 768
	weight := int64(4*w*h + 4*w*h + 4*width*(h+3*height))
	for _, tc := range []struct {
		name     string
		held     int64
		budget   time.Duration
		enhanced bool
	}{
		{"one byte short", imageWorkBudget - weight + 1, 200 * time.Millisecond, false},
		{"just enough", imageWorkBudget - weight, time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hdBudget = tc.budget
			require.True(t, imageWork.TryAcquire(tc.held))
			defer imageWork.Release(tc.held)
			before := len(upstream.recorded())
			run := runHD(t, context.Background(), relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "pst-hd-work"}, "/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"1girl","size":"512x512"}`)

			response, _ := run.images(t)
			if tc.enhanced {
				assert.Len(t, upstream.recorded(), before+2)
				assert.Equal(t, "768x768", response.Size)
				return
			}
			assert.Len(t, upstream.recorded(), before+1, "no call without the memory")
			assert.Equal(t, "512x512", response.Size)
			assert.Equal(t, "the image work did not start in time", run.info.ConversionDiagnostics()[0].Message[:len("the image work did not start in time")])
		})
	}
}
