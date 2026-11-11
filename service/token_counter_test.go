package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripInlineImages(t *testing.T) {
	// 2,048 base64 characters, the shortest payload counted as an image.
	payload := strings.Repeat("iVBORw0K", 256)
	pngURL := "data:image/png;base64," + payload
	webpURL := "data:image/webp;base64," + payload[4:] + "UklGRg=="
	svgURL := "data:image/svg+xml;charset=utf-8;base64," + payload
	shortURL := "data:image/png;base64," + payload[1:]
	for _, tc := range []struct {
		name     string
		text     string
		wantText string
		wantURLs []string
	}{
		{name: "Markdown image", text: "Here:\n![image](" + pngURL + ")\nDone", wantText: "Here:\n\nDone", wantURLs: []string{pngURL}},
		{
			name:     "two Markdown images with spaces inside the parentheses",
			text:     "![a]( " + webpURL + " )\n![b](" + pngURL + ")",
			wantText: "\n",
			wantURLs: []string{webpURL, pngURL},
		},
		{name: "bare data URL", text: "url: " + pngURL + "=, ok", wantText: "url: , ok", wantURLs: []string{pngURL + "="}},
		{name: "media type with a parameter", text: svgURL, wantText: "", wantURLs: []string{svgURL}},
		{
			name:     "a bracket in the alt text leaves the Markdown around the image",
			text:     "![a]b](" + pngURL + ")",
			wantText: "![a]b]()",
			wantURLs: []string{pngURL},
		},
		// An icon in generated HTML or CSS costs less as text than as an image.
		{name: "a short payload stays text", text: "![icon](" + shortURL + ")", wantText: "![icon](" + shortURL + ")"},
		{name: "URL-encoded data stays text", text: "![x](data:image/svg+xml,%3Csvg%3E)", wantText: "![x](data:image/svg+xml,%3Csvg%3E)"},
		{name: "no image", text: "plain text", wantText: "plain text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, urls := StripInlineImages(tc.text)
			assert.Equal(t, tc.wantText, text)
			assert.Equal(t, tc.wantURLs, urls)
		})
	}
}

// A chat history that carries an earlier image reply as Markdown base64 used
// to be estimated as base64 text: hundreds of thousands of prompt tokens,
// reserved before the request and billed when upstream reports no usage. The
// image now costs what the same image costs as an image_url part.
func TestCountRequestTokenCountsHistoryImagesAsImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalGetMediaToken := constant.GetMediaToken
	constant.GetMediaToken = true
	t.Cleanup(func() { constant.GetMediaToken = originalGetMediaToken })

	rng := rand.New(rand.NewPCG(1, 2))
	noise := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for i := range noise.Pix {
		noise.Pix[i] = byte(rng.UintN(256))
	}
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, noise))
	notAnImage := make([]byte, 405_900) // the size the research forwarder returns
	for i := range notAnImage {
		notAnImage[i] = byte(rng.UintN(256))
	}

	const model = "gpt-4o"
	history := func(assistantContent string) *types.TokenCountMeta {
		request := &dto.GeneralOpenAIRequest{Model: model, Messages: []dto.Message{
			{Role: "user", Content: "draw a cat"},
			{Role: "assistant", Content: assistantContent},
			{Role: "user", Content: "make it blue"},
		}}
		return request.GetTokenCountMeta()
	}
	count := func(meta *types.TokenCountMeta) int {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		common.SetContextKey(c, constant.ContextKeyOriginalModel, model)
		t.Cleanup(func() { CleanupFileSources(c) })
		tokens, err := CountRequestToken(c, meta, &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, IsStream: true})
		require.NoError(t, err)
		return tokens
	}
	textOnlyMeta := history("")
	textOnly := count(textOnlyMeta)

	for _, tc := range []struct {
		name        string
		payload     []byte
		imageTokens int
	}{
		// 256x256 on gpt-4o: 85 + 170 per 512px tile.
		{name: "a PNG is measured", payload: encoded.Bytes(), imageTokens: 255},
		// Bytes that do not decode are priced as an image of unknown size.
		{name: "an unreadable image does not fail the request", payload: notAnImage, imageTokens: tokenkit.ImageTokens(model, tokenkit.Image{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := history("![image](data:image/png;base64," + base64.StdEncoding.EncodeToString(tc.payload) + ")")
			// The same count with the whole text, base64 included, tokenized.
			before := textOnly - tokenkit.Count(model, textOnlyMeta.CombineText) + tokenkit.Count(model, meta.CombineText)

			after := count(meta)

			assert.Equal(t, textOnly+tc.imageTokens, after)
			assert.Greater(t, before, 100_000, "the base64 counted as text")
		})
	}
}
