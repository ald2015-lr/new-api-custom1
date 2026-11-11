package helper

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyDalleValidationAndPricesRemainCompatible(t *testing.T) {
	for _, tc := range []struct {
		body, size, quality string
		ratio               float64
		invalid             bool
	}{
		{body: `{"model":"dall-e-2"}`, size: "1024x1024", ratio: 1},
		{body: `{"model":"dall-e"}`, size: "1024x1024", ratio: 1},
		{body: `{"model":"dall-e-3"}`, size: "1024x1024", quality: "standard", ratio: 1},
		{body: `{"model":"dall-e-2","size":"256x256"}`, size: "256x256", ratio: 0.4},
		{body: `{"model":"dall-e-2","size":"512x512","quality":"hd"}`, size: "512x512", quality: "hd", ratio: 0.45},
		{body: `{"model":"dall-e-3","quality":"hd"}`, size: "1024x1024", quality: "hd", ratio: 2},
		{body: `{"model":"dall-e-3","size":"1024x1792","quality":"hd"}`, size: "1024x1792", quality: "hd", ratio: 3},
		{body: `{"model":"dall-e-3","size":"1792x1024"}`, size: "1792x1024", quality: "standard", ratio: 2},
		{body: `{"model":"dall-e-3","size":"256x256"}`, invalid: true},
		{body: `{"model":"dall-e-2","size":"1024x1792"}`, invalid: true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			request, err := GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.size, request.Size)
			assert.Equal(t, tc.quality, request.Quality)
			assert.Equal(t, tc.ratio, request.GetTokenCountMeta().ImagePriceRatio)
		})
	}
}

// TestGetAndValidOpenAIImageRequestMultipartStream verifies multipart image
// edit parsing: the stream field is parsed and validated, and the request body
// stays replayable for the upstream request.
func TestGetAndValidOpenAIImageRequestMultipartStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(t *testing.T, streamValue string, withImage bool) (*gin.Context, string) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-1"))
		require.NoError(t, writer.WriteField("prompt", "edit this image"))
		require.NoError(t, writer.WriteField("stream", streamValue))
		if withImage {
			part, err := writer.CreateFormFile("image", "input.png")
			require.NoError(t, err)
			_, err = part.Write([]byte("fake image"))
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		originalBody := body.String()

		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return c, originalBody
	}

	t.Run("valid stream value keeps body replayable", func(t *testing.T) {
		c, originalBody := newContext(t, "true", true)

		req, err := GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
		require.NoError(t, err)
		require.NotNil(t, req.Stream)
		require.True(t, *req.Stream)
		require.True(t, req.IsStream(c.Request))

		bodyAfterValidation, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, originalBody, string(bodyAfterValidation))

		form, err := common.ParseMultipartFormReusable(c)
		require.NoError(t, err)
		require.Equal(t, "true", url.Values(form.Value).Get("stream"))
		require.Len(t, form.File["image"], 1)
		billing, err := ResolveImageBillingRequestInput(c, &relaycommon.RelayInfo{Request: req}, billingexpr.RequestInput{})
		require.NoError(t, err)
		require.Equal(t, 1, *billing.ImageCount)
		require.NotContains(t, string(billing.Body), "fake image")
		require.NotContains(t, string(billing.Body), "edit this image")
	})

	t.Run("invalid stream value is rejected", func(t *testing.T) {
		c, _ := newContext(t, "notabool", false)

		_, err := GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid stream value")
	})
}

// Provider `parameters.n` is still bounded at ingress so a malformed multiplier
// is a client error, but the billing quantity always follows the top-level n:
// Ali image requests are served by the alibaba task plugin, which owns the
// DashScope parameters.n semantics.
func TestImageBillingRequestValidatesProviderCountWithoutOverriding(t *testing.T) {
	for _, tc := range []struct {
		body    string
		count   int
		invalid bool
	}{
		{`{"model":"z-image","n":2,"parameters":{"n":3,"prompt_extend":true}}`, 2, false},
		{`{"model":"gpt-image-2","n":2,"parameters":{"n":3}}`, 2, false},
		{`{"model":"z-image","n":2,"parameters":{"n":129}}`, 0, true},
		{`{"model":"z-image","parameters":{"n":-1}}`, 0, true},
		{`{"model":"z-image","n":2,"parameters":{}}`, 2, false},
		{`{"model":"z-image","n":2,"parameters":{"n":null}}`, 2, false},
		{`{"model":"z-image","n":2,"parameters":{"n":0}}`, 2, false},
		{`{"model":"z-image","parameters":{"n":1.5}}`, 0, true},
		{`{"model":"z-image","parameters":{"n":18446744073686646784}}`, 0, true},
		{`{"model":"z-image","parameters":{"n":128}}`, 1, false},
		{`{"model":"z-image","n":0}`, 1, false},
		{`{"model":"gpt-image-2","n":2,"parameters":{"n":0}}`, 2, false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
		request, err := GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
		if tc.invalid {
			require.Error(t, err, tc.body)
			continue
		}
		require.NoError(t, err, tc.body)
		input, err := ResolveImageBillingRequestInput(c, &relaycommon.RelayInfo{Request: request}, billingexpr.RequestInput{})
		require.NoError(t, err)
		require.Equal(t, tc.count, *input.ImageCount, tc.body)
		cost, _, err := billingexpr.RunExprWithRequest(`tier("image", fixed(0.04)) * image_count`, billingexpr.TokenParams{}, input)
		require.NoError(t, err)
		require.Equal(t, float64(tc.count)*40000, cost)
	}
}

// TestGetAndValidOpenAIImageRequestNBounds guards the billing invariant that
// the image generation count can never reach quota calculation with a value
// large enough to overflow int64 into a negative charge.
func TestGetAndValidOpenAIImageRequestNBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newJSONContext := func(t *testing.T, body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}

	boundErr := fmt.Sprintf("n must be an integer between 1 and %d", dto.MaxImageN)

	tests := []struct {
		name    string
		body    string
		wantErr string
		wantN   uint
	}{
		{
			name:    "overflowed uint64 n is rejected",
			body:    `{"model":"gpt-image-1","prompt":"a cat","n":18446744073686646784}`,
			wantErr: boundErr,
		},
		{
			name:    "n above max is rejected",
			body:    fmt.Sprintf(`{"model":"gpt-image-1","prompt":"a cat","n":%d}`, dto.MaxImageN+1),
			wantErr: boundErr,
		},
		{
			name:  "n at max is accepted",
			body:  fmt.Sprintf(`{"model":"gpt-image-1","prompt":"a cat","n":%d}`, dto.MaxImageN),
			wantN: dto.MaxImageN,
		},
		{
			name:  "explicit n is accepted",
			body:  `{"model":"gpt-image-1","prompt":"a cat","n":3}`,
			wantN: 3,
		},
		{
			name:  "zero n defaults to 1",
			body:  `{"model":"gpt-image-1","prompt":"a cat","n":0}`,
			wantN: 1,
		},
		{
			name:  "absent n defaults to 1",
			body:  `{"model":"gpt-image-1","prompt":"a cat"}`,
			wantN: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newJSONContext(t, tt.body)
			req, err := GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, req.N)
			require.Equal(t, tt.wantN, *req.N)
			require.Equal(t, float64(tt.wantN), req.GetTokenCountMeta().BillingRatios["n"])
		})
	}

	t.Run("negative multipart n is rejected", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-1"))
		require.NoError(t, writer.WriteField("prompt", "edit this image"))
		require.NoError(t, writer.WriteField("n", "-22904832"))
		require.NoError(t, writer.Close())

		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())

		_, err := GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
		require.Error(t, err)
		require.Contains(t, err.Error(), boundErr)
	})
}

// NovelAI's parameters.n_samples is the image count a native generate-image
// call reserves and bills. It is bounded the same way whether the request JSON
// is the body or the multipart field request (a form value or a file part,
// as the NovelAI web client sends it), and the body stays in place for
// byte-for-byte forwarding.
func TestGetAndValidateNovelAIImageRequestBoundsNSamples(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	boundErr := i18n.Translate("", i18n.MsgNovelAINSamplesInvalid, map[string]any{"Max": dto.MaxImageN})

	newContext := func(t *testing.T, transport, requestJSON string) (*gin.Context, []byte) {
		var body bytes.Buffer
		contentType := "application/json"
		switch transport {
		case "json":
			body.WriteString(requestJSON)
		default:
			writer := multipart.NewWriter(&body)
			if transport == "multipart value" {
				require.NoError(t, writer.WriteField("request", requestJSON))
			} else {
				part, err := writer.CreateFormFile("request", "blob")
				require.NoError(t, err)
				_, err = part.Write([]byte(requestJSON))
				require.NoError(t, err)
			}
			image, err := writer.CreateFormFile("image", "input.png")
			require.NoError(t, err)
			_, err = image.Write([]byte("\x89PNG fake"))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			contentType = writer.FormDataContentType()
		}
		original := bytes.Clone(body.Bytes())
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/ai/generate-image", &body)
		c.Request.Header.Set("Content-Type", contentType)
		return c, original
	}

	for _, tc := range []struct {
		name, request string
		count         int
		wantErr       string
	}{
		{name: "n_samples absent", request: `{"input":"a cat","model":"nai-diffusion-4-5-full","action":"generate","parameters":{"width":832,"height":1216}}`, count: 1},
		{name: "parameters absent", request: `{"input":"a cat","model":"nai-diffusion-4-5-full"}`, count: 1},
		{name: "n_samples null", request: `{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":null}}`, count: 1},
		{name: "n_samples 4", request: `{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":4}}`, count: 4},
		{name: "n_samples at max", request: fmt.Sprintf(`{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":%d}}`, dto.MaxImageN), count: dto.MaxImageN},
		{name: "n_samples 0", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":0}}`, wantErr: boundErr},
		{name: "n_samples negative", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":-1}}`, wantErr: boundErr},
		{name: "n_samples above max", request: fmt.Sprintf(`{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":%d}}`, dto.MaxImageN+1), wantErr: boundErr},
		{name: "n_samples fractional", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1.5}}`, wantErr: boundErr},
		{name: "n_samples string", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":"2"}}`, wantErr: boundErr},
		{name: "n_samples exponent", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1e10}}`, wantErr: boundErr},
		{name: "n_samples wrapped negative", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":18446744073686646784}}`, wantErr: boundErr},
		{name: "n_samples repeated in another case", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1,"N_Samples":8}}`,
			wantErr: i18n.Translate("", i18n.MsgNovelAIFieldRepeated, map[string]any{"Field": "n_samples"})},
		{name: "parameters repeated", request: `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1},"parameters":{"n_samples":8}}`,
			wantErr: i18n.Translate("", i18n.MsgNovelAIFieldRepeated, map[string]any{"Field": "parameters"})},
		{name: "parameters not an object", request: `{"model":"nai-diffusion-4-5-full","parameters":[4]}`, wantErr: i18n.Translate("", i18n.MsgNovelAIParametersInvalid)},
		{name: "model missing", request: `{"input":"a cat","parameters":{"n_samples":1}}`, wantErr: i18n.Translate("", i18n.MsgDistributorModelNameRequired)},
		{name: "not a JSON object", request: `["nai-diffusion-4-5-full"]`, wantErr: i18n.Translate("", i18n.MsgNovelAIRequestInvalid)},
	} {
		for _, transport := range []string{"json", "multipart value", "multipart file"} {
			t.Run(tc.name+"/"+transport, func(t *testing.T) {
				c, original := newContext(t, transport, tc.request)
				request, err := GetAndValidateRequest(c, relayconstant.RelayFormatNovelAI)
				if tc.wantErr != "" {
					require.Error(t, err)
					assert.Equal(t, tc.wantErr, err.Error())
					return
				}
				require.NoError(t, err)
				imageRequest, ok := request.(*dto.ImageRequest)
				require.True(t, ok)
				assert.Equal(t, "nai-diffusion-4-5-full", imageRequest.Model)
				assert.Equal(t, "a cat", imageRequest.Prompt)
				count, err := imageRequest.ImageCount(false)
				require.NoError(t, err)
				assert.Equal(t, tc.count, count)
				assert.Equal(t, float64(tc.count), imageRequest.GetTokenCountMeta().BillingRatios["n"])
				forwarded, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				assert.Equal(t, original, forwarded)
			})
		}
	}

	missing := i18n.Translate("", i18n.MsgNovelAIRequestFieldMissing)
	for name, fields := range map[string][]string{
		"multipart without request":   {"prompt"},
		"multipart with two requests": {"request", "request"},
	} {
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for _, field := range fields {
				require.NoError(t, writer.WriteField(field, `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1}}`))
			}
			require.NoError(t, writer.Close())
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", &body)
			c.Request.Header.Set("Content-Type", writer.FormDataContentType())
			_, err := GetAndValidateNovelAIImageRequest(c)
			require.Error(t, err)
			assert.Equal(t, missing, err.Error())
		})
	}

	// The form is forwarded byte for byte and parsed again by the upstream.
	// A form whose request field another parser (python-multipart and
	// Starlette, for one) reads from another part or as other JSON is
	// rejected: the billed model and n_samples would not be the generated
	// ones. A form every parser reads alike is accepted.
	const billed, hidden = `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1}}`, `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":128}}`
	invalidForm := i18n.Translate("", i18n.MsgNovelAIMultipartInvalid, map[string]any{"Error": ""})
	for _, tc := range []struct {
		name, contentType string
		// raw part headers and content, CRLF-joined into the body
		parts   [][2]string
		invalid bool
	}{
		{name: "name* overrides the plain name", parts: [][2]string{
			{`Content-Disposition: form-data; name="request"; name*=UTF-8''ignored`, hidden},
			{`Content-Disposition: form-data; name="unused"; name*=UTF-8''request`, billed},
		}, invalid: true},
		{name: "continued name", parts: [][2]string{{`Content-Disposition: form-data; name*0="req"; name*1="uest"`, billed}}, invalid: true},
		{name: "second Content-Disposition header", parts: [][2]string{
			{`Content-Disposition: form-data; name="request"`, billed},
			{"Content-Disposition: form-data; name=\"unused\"\r\nContent-Disposition: form-data; name=\"request\"", hidden},
		}, invalid: true},
		{name: "attachment disposition", parts: [][2]string{
			{`Content-Disposition: attachment; name="request"`, hidden},
			{`Content-Disposition: form-data; name="request"`, billed},
		}, invalid: true},
		{name: "unparseable disposition parameter", parts: [][2]string{
			{`Content-Disposition: form-data; name="request"`, billed},
			{`Content-Disposition: form-data; name="request"; junk`, hidden},
		}, invalid: true},
		{name: "repeated name parameter", parts: [][2]string{
			{`Content-Disposition: form-data; name="request"`, billed},
			{`Content-Disposition: form-data; name="x"; name="request"`, hidden},
		}, invalid: true},
		{name: "part without Content-Disposition", parts: [][2]string{
			{`Content-Type: application/json`, hidden},
			{`Content-Disposition: form-data; name="request"`, billed},
		}, invalid: true},
		{name: "quoted-printable request", parts: [][2]string{{"Content-Disposition: form-data; name=\"request\"\r\nContent-Transfer-Encoding: quoted-printable", billed}}, invalid: true},
		{name: "extended boundary", contentType: "multipart/form-data; boundary=xyz; boundary*=UTF-8''xyz", parts: [][2]string{{`Content-Disposition: form-data; name="request"`, billed}}, invalid: true},
		{name: "form charset other than UTF-8", contentType: "multipart/form-data; boundary=xyz; charset=utf-7", parts: [][2]string{{`Content-Disposition: form-data; name="request"`, billed}}, invalid: true},
		{name: "request field charset other than UTF-8", parts: [][2]string{{"Content-Disposition: form-data; name=\"request\"\r\nContent-Type: text/plain; charset=utf-7", billed}}, invalid: true},
		{name: "identity encoding, UTF-8 charset and a file name", contentType: "multipart/form-data; boundary=xyz; charset=UTF-8", parts: [][2]string{
			{"Content-Disposition: form-data; name=\"request\"; filename=\"blob\"\r\nContent-Type: application/json; charset=utf-8\r\nContent-Transfer-Encoding: 8bit", billed},
			{"Content-Disposition: form-data; name=\"image\"; filename=\"a*b;name*=request.png\"\r\nContent-Type: image/png", "\x89PNG fake"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body strings.Builder
			for _, part := range tc.parts {
				body.WriteString("--xyz\r\n" + part[0] + "\r\n\r\n" + part[1] + "\r\n")
			}
			body.WriteString("--xyz--\r\n")
			contentType := tc.contentType
			if contentType == "" {
				contentType = "multipart/form-data; boundary=xyz"
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", strings.NewReader(body.String()))
			c.Request.Header.Set("Content-Type", contentType)

			request, err := GetAndValidateNovelAIImageRequest(c)

			if tc.invalid {
				require.Error(t, err)
				assert.True(t, strings.HasPrefix(err.Error(), invalidForm), err.Error())
				return
			}
			require.NoError(t, err)
			count, err := request.ImageCount(false)
			require.NoError(t, err)
			assert.Equal(t, 1, count)
			forwarded, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			assert.Equal(t, body.String(), string(forwarded))
		})
	}
}

// A JSON request is billed as gjson reads its bytes, and the upstream decodes
// the same bytes by their Content-Type. Only a body without one, or typed
// application/json with unambiguous parameters and a UTF-8 charset, reads the
// same on both sides; any other type is rejected before a channel is chosen.
func TestGetAndValidateNovelAIImageRequestRequiresPlainJSONContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	invalid := i18n.Translate("", i18n.MsgNovelAIContentTypeInvalid)
	const requestJSON = `{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":2}}`

	for _, tc := range []struct {
		contentType string
		accepted    bool
	}{
		{contentType: "", accepted: true},
		{contentType: "application/json", accepted: true},
		{contentType: "application/json; charset=UTF-8", accepted: true},
		{contentType: "application/x-www-form-urlencoded"},
		{contentType: "text/plain"},
		{contentType: "application/json; charset=utf-7"},
		{contentType: "application/json; charset"},
		{contentType: "application/json; charset*=utf-8''x"},
	} {
		t.Run(tc.contentType, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/ai/generate-image", strings.NewReader(requestJSON))
			if tc.contentType != "" {
				c.Request.Header.Set("Content-Type", tc.contentType)
			}

			request, err := GetAndValidateRequest(c, relayconstant.RelayFormatNovelAI)

			if !tc.accepted {
				require.Error(t, err)
				assert.Equal(t, invalid, err.Error())
				return
			}
			require.NoError(t, err)
			count, err := request.(*dto.ImageRequest).ImageCount(false)
			require.NoError(t, err)
			assert.Equal(t, 2, count)
		})
	}
}
