package router

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel/novelai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

var relayRouterTestUsers atomic.Int64

// The relay routes run the per-model limit after Distribute, on the model the
// request names: once a user has used up a model's rule, the next request for
// that model is rejected with the rule's message before it reaches the relay.
// The NovelAI image routes, whose root-path chain is built apart from /v1,
// run it and the user-level limit too.
func TestRelayRoutesApplyThePerModelRateLimitAfterDistribute(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	require.NoError(t, i18n.Init())
	previousEnabled, previousMinutes := setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes
	previousTotal, previousSuccess := setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount
	previousModels := setting.ModelRequestRateLimitModel2JSONString()
	t.Cleanup(func() {
		setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes = previousEnabled, previousMinutes
		setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount = previousTotal, previousSuccess
		require.NoError(t, setting.UpdateModelRequestRateLimitModelByJSONString(previousModels))
	})
	setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes = true, 1
	setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount = 0, 1000
	require.NoError(t, setting.UpdateModelRequestRateLimitModelByJSONString(`{"ruled-model": {"duration_minutes": 1, "total_count": 1, "success_count": 0}}`))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"ruled-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)
	// A user id of its own keeps the in-memory counts apart under -count.
	user := model.User{Id: 6300000 + int(relayRouterTestUsers.Add(1)), Username: "model-rule-user", Status: common.UserStatusEnabled, Group: "default", Quota: 1000000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "modelruletestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	channel := model.Channel{Name: "model-rule-upstream", Key: "sk-upstream", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "ruled-model", BaseURL: &upstream.URL}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: "ruled-model", Group: "default", Enabled: true}).Error)
	engine := gin.New()
	SetRelayRouter(engine)
	chat := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"ruled-model","messages":[{"role":"user","content":"hi"}]}`))
		request.Header.Set("Authorization", "Bearer modelruletestkey")
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	first := chat()
	require.NotEqual(t, http.StatusTooManyRequests, first.Code, first.Body.String())
	limited := chat()
	require.Equal(t, http.StatusTooManyRequests, limited.Code, limited.Body.String())
	ruleMessage := i18n.Translate("", i18n.MsgModelRateLimitTotalReached, map[string]any{"Model": "ruled-model", "Minutes": 1, "Max": 1})
	assert.Contains(t, limited.Body.String(), ruleMessage)

	novelAI := func(path, key, modelName string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+modelName+`","parameters":{"n_samples":1}}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	for _, path := range []string{"/ai/generate-image", "/v1/ai/generate-image-stream"} {
		recorder := novelAI(path, "modelruletestkey", "ruled-model")
		require.Equal(t, http.StatusTooManyRequests, recorder.Code, path)
		assert.Contains(t, recorder.Body.String(), ruleMessage, path)
	}

	// The user-level limit counts every request before Distribute, so a model
	// with no channel is enough: the second request of a user is rejected.
	setting.ModelRequestRateLimitCount = 1
	userMessage := i18n.Translate("", i18n.MsgRateLimitTotalReached, map[string]any{"Minutes": 1, "Max": 1})
	for _, path := range []string{"/ai/generate-image", "/v1/ai/generate-image"} {
		id := 6300000 + int(relayRouterTestUsers.Add(1))
		name := fmt.Sprintf("userlimit%d", id)
		key := name + "key"
		limitedUser := model.User{Id: id, Username: name, AffCode: name, Status: common.UserStatusEnabled, Group: "default", Quota: 1000000}
		require.NoError(t, model.DB.Create(&limitedUser).Error)
		require.NoError(t, model.DB.Create(&model.Token{UserId: limitedUser.Id, Key: key, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

		first := novelAI(path, key, "unrouted-model")
		require.NotEqual(t, http.StatusTooManyRequests, first.Code, first.Body.String())
		second := novelAI(path, key, "unrouted-model")
		require.Equal(t, http.StatusTooManyRequests, second.Code, path)
		assert.Contains(t, second.Body.String(), userMessage, path)
	}
}

// novelAIUpstream is a NovelAI forwarder that records each request and
// answers with the configured response.
type novelAIUpstream struct {
	mu                                       sync.Mutex
	calls                                    int
	path, authorization, contentType, accept string
	// acceptSent tells an absent Accept from an empty one.
	acceptSent                        bool
	body                              []byte
	status                            int
	responseContentType, responseBody string
}

func (u *novelAIUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls++
	u.path, u.authorization, u.contentType, u.accept, u.body = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("Accept"), body
	_, u.acceptSent = r.Header["Accept"]
	status, contentType, response := u.status, u.responseContentType, u.responseBody
	u.mu.Unlock()
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, response)
}

func (u *novelAIUpstream) respond(status int, contentType, body string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status, u.responseContentType, u.responseBody = status, contentType, body
}

// NovelAI clients use new-api as their NovelAI host: the native image routes,
// with or without /v1, authenticate the new-api token, pick the channel by the
// model of the request JSON (the body or the multipart field request), forward
// the exact bytes and the client's Accept to the channel's /ai path with the
// channel key, and return the upstream response, which /ai/generate-image
// converts to the format NovelAI clients expect (a zip, or JSON when Accept
// names application/json) when upstream chose the other one. Each call is
// billed per image at the model's per-call price or tiered expression:
// parameters.n_samples is bounded and reserved, a success settles at the
// images the client received, and an upstream failure charges nothing and
// reaches the client as upstream sent it. A NovelAI channel (NovelAI's
// official API) and an OpenAI-type channel in front of a NovelAI-compatible
// forwarder relay alike.
func TestNovelAIImageRoutesRelayAndBillPerImage(t *testing.T) {
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeNovelAI} {
		t.Run(constant.GetChannelTypeName(channelType), func(t *testing.T) {
			testNovelAIImageRoutes(t, channelType)
		})
	}
}

func testNovelAIImageRoutes(t *testing.T, channelType int) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}))
	require.NoError(t, i18n.Init())
	logConsume, batchUpdate := common.LogConsumeEnabled, common.BatchUpdateEnabled
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedRatios, savedCompletionRatios := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.BatchUpdateEnabled = logConsume, batchUpdate
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(savedCompletionRatios))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	// $0.02 per image, and $0.06 per image as an expression; V5 Full is
	// priced by tokens.
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"nai-diffusion-4-5-full":0.02,"nai-retry-0":0.02,"nai-retry-1":0.02,"nai-retry-2":0.02,"nai-retry-3":0.02,"nai-retry-4":0.02,"nai-retry-5":0.02}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"nai-diffusion-5-full":2}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"nai-diffusion-5-full":4}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"nai-diffusion-3":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"nai-diffusion-3":"tier(\"image\", fixed(0.06)) * image_count"}`,
		// controller.Relay records perf metrics on a goroutine that can outlive
		// a subtest and race with the fixture restoring globals.
		"perf_metrics_setting.enabled": "false",
	}))

	upstream := &novelAIUpstream{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	baseURL := server.URL + "/"
	user := model.User{Id: 6400000 + int(relayRouterTestUsers.Add(1)), Username: "novelai-user", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "novelaitestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	channel := model.Channel{Name: "novelai-upstream", Key: "sk-novelai", Status: common.ChannelStatusEnabled, Type: channelType, Group: "default", Models: "nai-diffusion-4-5-full,nai-diffusion-3,nai-diffusion-5-full", BaseURL: &baseURL}
	require.NoError(t, model.DB.Create(&channel).Error)
	for _, name := range []string{"nai-diffusion-4-5-full", "nai-diffusion-3", "nai-diffusion-5-full"} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true}).Error)
	}
	engine := gin.New()
	SetRelayRouter(engine)

	send := func(path, contentType, accept string, body []byte, authorized bool) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		if accept != "" {
			request.Header.Set("Accept", accept)
		}
		if authorized {
			request.Header.Set("Authorization", "Bearer novelaitestkey")
		}
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	multipartRequest := func(requestJSON string) (string, []byte) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("request", "blob")
		require.NoError(t, err)
		_, err = io.WriteString(part, requestJSON)
		require.NoError(t, err)
		image, err := writer.CreateFormFile("image", "source.png")
		require.NoError(t, err)
		_, err = image.Write([]byte("\x89PNG\r\n\x1a\nsource"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return writer.FormDataContentType(), body.Bytes()
	}
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	for _, name := range []string{"image_0.png", "image_1.png"} {
		entry, err := zipWriter.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte("\x89PNG\r\n\x1a\n" + name))
		require.NoError(t, err)
	}
	require.NoError(t, zipWriter.Close())
	walletQuota := func() int {
		quota, err := model.GetUserQuota(user.Id, true)
		require.NoError(t, err)
		return quota
	}
	consumeLogs := func() []model.Log {
		var logs []model.Log
		require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, model.LogTypeConsume).Order("id").Find(&logs).Error)
		return logs
	}

	for _, path := range []string{"/ai/generate-images", "/v1/ai/annotate-image", "/ai/upscale"} {
		recorder := send(path, "application/json", "", []byte(`{"model":"nai-diffusion-4-5-full"}`), true)
		assert.Equal(t, http.StatusNotFound, recorder.Code, path)
	}
	// new-api's own rejections come in NovelAI's error shape, before anything
	// is reserved or sent: a missing token, an out-of-range n_samples (JSON or
	// multipart), a form without its request field, a form whose request
	// field another multipart parser reads from another part, and a JSON body
	// under another type or charset, which the distributor rejects before it
	// looks for a channel (unrouted-model has none).
	contentType, outOfRange := multipartRequest(`{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":129}}`)
	var withoutRequest bytes.Buffer
	formWriter := multipart.NewWriter(&withoutRequest)
	require.NoError(t, formWriter.WriteField("prompt", `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1}}`))
	require.NoError(t, formWriter.Close())
	shadowed := "--xyz\r\nContent-Disposition: form-data; name=\"request\"; name*=UTF-8''ignored\r\n\r\n" +
		`{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":128}}` +
		"\r\n--xyz\r\nContent-Disposition: form-data; name=\"unused\"; name*=UTF-8''request\r\n\r\n" +
		`{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1}}` + "\r\n--xyz--\r\n"
	nSamplesMessage := i18n.Translate("", i18n.MsgNovelAINSamplesInvalid, map[string]any{"Max": dto.MaxImageN})
	canvasMessage := i18n.Translate("", i18n.MsgNovelAIDimensionsInvalid, map[string]any{"Max": relayconstant.NovelAIMaxPixels})
	contentTypeMessage := i18n.Translate("", i18n.MsgNovelAIContentTypeInvalid)
	for _, rejected := range []struct {
		path, contentType, body string
		authorized              bool
		status                  int
		message                 string
	}{
		{"/ai/generate-image", "application/json", `{"model":"nai-diffusion-4-5-full"}`, false, http.StatusUnauthorized, i18n.Translate("", i18n.MsgTokenInvalid)},
		{"/ai/generate-image-stream", "application/json", `{"model":"nai-diffusion-4-5-full"}`, false, http.StatusUnauthorized, i18n.Translate("", i18n.MsgTokenInvalid)},
		{"/v1/ai/generate-image", "application/json", `{"model":"nai-diffusion-4-5-full"}`, false, http.StatusUnauthorized, i18n.Translate("", i18n.MsgTokenInvalid)},
		{"/v1/ai/generate-image-stream", "application/json", `{"model":"nai-diffusion-4-5-full"}`, false, http.StatusUnauthorized, i18n.Translate("", i18n.MsgTokenInvalid)},
		{"/ai/generate-image", "application/json", `{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":0}}`, true, http.StatusBadRequest, nSamplesMessage},
		{"/ai/generate-image", contentType, string(outOfRange), true, http.StatusBadRequest, nSamplesMessage},
		{"/ai/generate-image-stream", "application/json", `{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"width":"1536","height":2048}}`, true, http.StatusBadRequest, canvasMessage},
		{"/v1/ai/generate-image", formWriter.FormDataContentType(), withoutRequest.String(), true, http.StatusBadRequest, i18n.Translate("", i18n.MsgNovelAIRequestFieldMissing)},
		{"/ai/generate-image-stream", "multipart/form-data; boundary=xyz", shadowed, true, http.StatusBadRequest, i18n.Translate("", i18n.MsgNovelAIMultipartInvalid, map[string]any{"Error": ""})},
		{"/ai/generate-image", "text/plain", `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1}}`, true, http.StatusBadRequest, contentTypeMessage},
		{"/v1/ai/generate-image-stream", "application/json; charset=utf-7", `{"model":"nai-diffusion-4-5-full","parameters":{"n_samples":1}}`, true, http.StatusBadRequest, contentTypeMessage},
		{"/ai/generate-image", "text/plain", `{"model":"unrouted-model","parameters":{"n_samples":1}}`, true, http.StatusBadRequest, contentTypeMessage},
	} {
		recorder := send(rejected.path, rejected.contentType, "", []byte(rejected.body), rejected.authorized)
		require.Equal(t, rejected.status, recorder.Code, recorder.Body.String())
		var payload map[string]any
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		assert.Equal(t, float64(rejected.status), payload["statusCode"], recorder.Body.String())
		assert.Contains(t, payload["message"], rejected.message)
	}
	require.Zero(t, upstream.calls)
	require.Empty(t, consumeLogs())
	require.Equal(t, 10_000_000, walletQuota())

	t.Run("JSON request settles at the images in the zip", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/zip", archive.String())
		requestJSON := []byte(`{"input":"a cat","model":"nai-diffusion-4-5-full","action":"generate","parameters":{"width":832,"height":1216,"n_samples":4}}`)
		before := walletQuota()

		recorder := send("/v1/ai/generate-image", "application/json", "", requestJSON, true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "application/zip", recorder.Header().Get("Content-Type"))
		assert.Equal(t, archive.Bytes(), recorder.Body.Bytes())
		assert.Equal(t, "/ai/generate-image", upstream.path)
		assert.Equal(t, "Bearer sk-novelai", upstream.authorization)
		assert.Equal(t, "application/json", upstream.contentType)
		assert.Equal(t, requestJSON, upstream.body)
		logs := consumeLogs()
		require.NotEmpty(t, logs)
		last := logs[len(logs)-1]
		assert.Equal(t, "nai-diffusion-4-5-full", last.ModelName)
		assert.Equal(t, 20_000, last.Quota, "two images at $0.02, not the four requested")
		assert.Contains(t, last.Content, "Image count 2")
		// NovelAI reports no usage: the prompt's estimate, and 1245 tokens
		// for each 832x1216 image delivered.
		assert.Equal(t, tokenkit.Estimate("nai-diffusion-4-5-full", "a cat"), last.PromptTokens)
		assert.Equal(t, 2*1245, last.CompletionTokens)
		assert.False(t, last.IsStream)
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("JSON request without a Content-Type reaches upstream typed as JSON", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/zip", archive.String())
		requestJSON := []byte(`{"input":"a cat","model":"nai-diffusion-4-5-full","action":"generate","parameters":{"n_samples":2}}`)
		before := walletQuota()

		recorder := send("/ai/generate-image", "", "", requestJSON, true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "application/json", upstream.contentType, "the forwarder must parse the body as the JSON billing read")
		assert.Equal(t, requestJSON, upstream.body)
		assert.Equal(t, archive.Bytes(), recorder.Body.Bytes())
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("multipart request is forwarded with its boundary and settles by expression", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/json", `{"images":[{"image":"aGk="},{"image":""},{"image":"aGk="}]}`)
		contentType, body := multipartRequest(`{"input":"a fox","model":"nai-diffusion-3","action":"img2img","parameters":{"n_samples":3}}`)
		before := walletQuota()

		recorder := send("/ai/generate-image", contentType, "application/json", body, true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, `{"images":[{"image":"aGk="},{"image":""},{"image":"aGk="}]}`, recorder.Body.String())
		assert.Equal(t, "application/json", upstream.accept)
		assert.Equal(t, contentType, upstream.contentType)
		assert.Equal(t, body, upstream.body)
		logs := consumeLogs()
		last := logs[len(logs)-1]
		assert.Equal(t, "nai-diffusion-3", last.ModelName)
		assert.Equal(t, 60_000, last.Quota, "two images with a payload at $0.06")
		// The img2img source counts as an input image of NovelAI's default
		// 832x1216 canvas.
		assert.Equal(t, tokenkit.Estimate("nai-diffusion-3", "a fox")+1245, last.PromptTokens)
		assert.Equal(t, 2*1245, last.CompletionTokens)
		var other map[string]any
		require.NoError(t, common.Unmarshal([]byte(last.Other), &other))
		assert.Equal(t, float64(2), other["image_count"])
		assert.Equal(t, "tiered_expr", other["billing_mode"])
		assert.Equal(t, before-60_000, walletQuota())
	})

	// A model priced by tokens now bills the images too: (prompt +
	// completion x completion ratio) x model ratio x group ratio, the input
	// image at the default image ratio of 1.
	t.Run("a ratio price bills the prompts and the images as tokens", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/zip", archive.String())
		requestJSON := `{"input":"1girl","model":"nai-diffusion-5-full","action":"img2img","parameters":{"width":1024,"height":1024,"n_samples":2,"image":"aW1n",` +
			`"v4_prompt":{"caption":{"base_caption":"1girl, very aesthetic","char_captions":[{"char_caption":"red hair"}]}},"v4_negative_prompt":{"caption":{"base_caption":"lowres","char_captions":[]}}}}`
		before := walletQuota()

		recorder := send("/ai/generate-image", "application/json", "", []byte(requestJSON), true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		text := tokenkit.Estimate("nai-diffusion-5-full", "1girl, very aesthetic") + tokenkit.Estimate("nai-diffusion-5-full", "red hair") + tokenkit.Estimate("nai-diffusion-5-full", "lowres")
		logs := consumeLogs()
		last := logs[len(logs)-1]
		assert.Equal(t, text+1290, last.PromptTokens, "the captions, not input again, and the source image")
		assert.Equal(t, 2*1290, last.CompletionTokens)
		assert.Equal(t, (text+1290+2*1290*4)*2, last.Quota)
		var other map[string]any
		require.NoError(t, common.Unmarshal([]byte(last.Other), &other))
		assert.Equal(t, float64(1290), other["image_output"], "the input image tokens the log shows")
		assert.Equal(t, float64(1), other["image_ratio"])
		assert.Equal(t, before-last.Quota, walletQuota())
	})

	t.Run("stream that cannot be counted keeps the reserved count", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/x-msgpack", "\x82\xaaevent_type\xa5final")
		before := walletQuota()

		recorder := send("/ai/generate-image-stream", "application/json", "", []byte(`{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":2}}`), true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "application/x-msgpack", recorder.Header().Get("Content-Type"))
		assert.Equal(t, "\x82\xaaevent_type\xa5final", recorder.Body.String())
		assert.Equal(t, "/ai/generate-image-stream", upstream.path)
		logs := consumeLogs()
		last := logs[len(logs)-1]
		assert.Equal(t, 20_000, last.Quota)
		assert.Equal(t, 2*1245, last.CompletionTokens, "the two reserved images")
		assert.True(t, last.IsStream)
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("upstream failure is returned as sent and charges nothing", func(t *testing.T) {
		const failure = `{"statusCode":400,"message":"Prompt contains disallowed content"}`
		// Not the JSON type new-api's own errors use, to see it is the upstream one.
		upstream.respond(http.StatusBadRequest, "application/json", failure)
		before, loggedBefore := walletQuota(), len(consumeLogs())

		recorder := send("/ai/generate-image", "application/json", "", []byte(`{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":2}}`), true)

		require.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
		assert.Equal(t, failure, recorder.Body.String())
		assert.Len(t, consumeLogs(), loggedBefore)
		// The reservation is refunded in the background.
		assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("Accept application/json gets the images of a zip as JSON", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/zip", archive.String())
		before := walletQuota()

		recorder := send("/ai/generate-image", "application/json", "application/json", []byte(`{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":4}}`), true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.True(t, upstream.acceptSent)
		assert.Equal(t, "application/json", upstream.accept)
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
		var payload struct {
			Images []struct {
				Image string `json:"image"`
			} `json:"images"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Len(t, payload.Images, 2)
		for i, image := range payload.Images {
			png, err := base64.StdEncoding.DecodeString(image.Image)
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("\x89PNG\r\n\x1a\nimage_%d.png", i), string(png))
		}
		logs := consumeLogs()
		assert.Equal(t, 20_000, logs[len(logs)-1].Quota, "the two PNGs at $0.02")
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("no Accept gets the images of JSON as a zip", func(t *testing.T) {
		upstream.respond(http.StatusOK, "application/json", `{"images":[{"image":"aGk="},{"image":""},{"image":"aGk="}]}`)
		before := walletQuota()

		recorder := send("/ai/generate-image", "application/json", "", []byte(`{"input":"a cat","model":"nai-diffusion-4-5-full","parameters":{"n_samples":3}}`), true)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.False(t, upstream.acceptSent, "a client without an Accept accepts any type; an empty Accept would name none")
		assert.Equal(t, "application/x-zip-compressed", recorder.Header().Get("Content-Type"))
		images, err := zip.NewReader(bytes.NewReader(recorder.Body.Bytes()), int64(recorder.Body.Len()))
		require.NoError(t, err)
		require.Len(t, images.File, 2)
		for i, file := range images.File {
			assert.Equal(t, fmt.Sprintf("image_%d.png", i), file.Name)
			entry, err := file.Open()
			require.NoError(t, err)
			png, err := io.ReadAll(entry)
			require.NoError(t, err)
			assert.Equal(t, "hi", string(png))
		}
		logs := consumeLogs()
		assert.Equal(t, 20_000, logs[len(logs)-1].Quota, "the two images with a payload at $0.02")
		assert.Equal(t, before-20_000, walletQuota())
	})

	// NovelAI may spend Anlas on any request that reached it, so the next
	// channel is tried only when the first provably received nothing (it, or
	// the HTTP or SOCKS proxy it goes through, could not be dialed) or
	// answered with an error status. A request it accepted and left
	// unanswered, or answered 2xx with a body that broke before the client
	// got a byte, is final and charges nothing.
	t.Run("another channel is tried only when the first did not take the request", func(t *testing.T) {
		retryTimes := common.RetryTimes
		common.RetryTimes = 2
		t.Cleanup(func() { common.RetryTimes = retryTimes })
		var firstCalls atomic.Int32
		// firstChannel reads the request, writes raw straight to the
		// connection and closes it; "" closes it without an answer.
		firstChannel := func(raw string) string {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstCalls.Add(1)
				_, _ = io.ReadAll(r.Body)
				conn, buffered, err := http.NewResponseController(w).Hijack()
				if err != nil {
					return
				}
				_, _ = buffered.WriteString(raw)
				_ = buffered.Flush()
				_ = conn.Close()
			}))
			t.Cleanup(server.Close)
			return server.URL
		}
		// Nothing listens on port 1 (tcpmux), so a dial there is refused at
		// once. A freshly closed ephemeral port is not safe: another process
		// can be handed it before the dial and then answer or stall it.
		const refusedAddr = "127.0.0.1:1"
		unreachable := "http://" + refusedAddr
		// Behind a proxy that cannot be dialed, a channel that would take the
		// request and leave it unanswered never sees it.
		const failure = `{"statusCode":500,"message":"overloaded"}`

		for i, tc := range []struct {
			name, firstURL, firstProxy string
			retried                    bool
		}{
			{name: "accepted and left unanswered", firstURL: firstChannel("")},
			{name: "answered 2xx and broke before the client got a byte", firstURL: firstChannel("HTTP/1.1 200 OK\r\nContent-Type: application/zip\r\nContent-Length: 1000\r\n\r\nPK")},
			{name: "could not be dialed", firstURL: unreachable, retried: true},
			{name: "its SOCKS proxy could not be dialed", firstURL: firstChannel(""), firstProxy: "socks5://" + refusedAddr, retried: true},
			{name: "its HTTP proxy could not be dialed", firstURL: firstChannel(""), firstProxy: "http://" + refusedAddr, retried: true},
			{name: "answered 5xx", firstURL: firstChannel(fmt.Sprintf("HTTP/1.1 500 Internal Server Error\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(failure), failure)), retried: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				modelName := fmt.Sprintf("nai-retry-%d", i)
				second := &novelAIUpstream{}
				second.respond(http.StatusOK, "application/zip", archive.String())
				secondServer := httptest.NewServer(second)
				t.Cleanup(secondServer.Close)
				for priority, baseURL := range map[int64]string{10: tc.firstURL, 0: secondServer.URL} {
					channel := model.Channel{Name: modelName, Key: "sk-novelai", Status: common.ChannelStatusEnabled, Type: channelType, Group: "default", Models: modelName, BaseURL: &baseURL, Priority: &priority, AutoBan: common.GetPointer(0)}
					if priority == 10 && tc.firstProxy != "" {
						channel.Setting = common.GetPointer(`{"proxy":"` + tc.firstProxy + `"}`)
					}
					require.NoError(t, model.DB.Create(&channel).Error)
					require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: modelName, Group: "default", Enabled: true, Priority: &priority}).Error)
				}
				calledBefore, before, loggedBefore := firstCalls.Load(), walletQuota(), len(consumeLogs())

				recorder := send("/ai/generate-image", "application/json", "", []byte(`{"input":"a cat","model":"`+modelName+`","parameters":{"n_samples":2}}`), true)

				switch {
				case tc.firstProxy != "":
					assert.Equal(t, calledBefore, firstCalls.Load(), "nothing got past the proxy")
				case tc.firstURL != unreachable:
					assert.Equal(t, calledBefore+1, firstCalls.Load(), "the first channel got the request")
				}
				if !tc.retried {
					require.Equal(t, http.StatusInternalServerError, recorder.Code, recorder.Body.String())
					var payload map[string]any
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
					assert.Equal(t, float64(http.StatusInternalServerError), payload["statusCode"])
					assert.Contains(t, payload["message"], "(request id: ", "new-api's own NovelAI errors carry the request id")
					assert.Zero(t, second.calls, "the request must not be sent a second time")
					assert.Len(t, consumeLogs(), loggedBefore)
					assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
					return
				}
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				assert.Equal(t, archive.Bytes(), recorder.Body.Bytes())
				assert.Equal(t, 1, second.calls)
				logs := consumeLogs()
				require.Len(t, logs, loggedBefore+1, "billed once")
				assert.Equal(t, 20_000, logs[len(logs)-1].Quota)
				assert.Equal(t, before-20_000, walletQuota())
			})
		}
	})
}

// A NovelAI channel serves image generation only: on the NovelAI native
// routes NovelAI's own 201 zip passes through as sent, while a search on a
// model the channel lists, or a chat request on a channel that passes
// bodies through (which the conversion needs), is rejected with a 400
// before anything reaches NovelAI, and charges nothing
// (TestNovelAIChannelConvertsOpenAIImages covers the OpenAI routes).
func TestNovelAIChannelServesOnlyTheNativeRoutes(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}))
	require.NoError(t, i18n.Init())
	logConsume, batchUpdate := common.LogConsumeEnabled, common.BatchUpdateEnabled
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.BatchUpdateEnabled = logConsume, batchUpdate
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"nai-diffusion-4-5-full":0.02,"nai-diffusion-3":0.02,"deepseek-search":0.02}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))

	upstream := &novelAIUpstream{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	user := model.User{Id: 6400000 + int(relayRouterTestUsers.Add(1)), Username: "novelai-official", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "novelaiofficialkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	// nai-diffusion-3 is served by a channel that passes request bodies
	// through; deepseek-search, the /v1/tools/search routing name, by a
	// channel that lists it by mistake.
	for modelName, setting := range map[string]string{"nai-diffusion-4-5-full": "", "nai-diffusion-3": `{"pass_through_body_enabled":true}`, constant.DeepSeekSearchModel: ""} {
		channel := model.Channel{Name: "novelai-" + modelName, Key: "pst-novelai", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default", Models: modelName, BaseURL: common.GetPointer(server.URL), Setting: common.GetPointer(setting)}
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: modelName, Group: "default", Enabled: true}).Error)
	}
	engine := gin.New()
	SetRelayRouter(engine)
	// /v1/images/generations is a host protocol route.
	SetTaskPluginProtocolRouter(engine)
	send := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer novelaiofficialkey")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	walletQuota := func() int {
		quota, err := model.GetUserQuota(user.Id, true)
		require.NoError(t, err)
		return quota
	}
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, err := zipWriter.Create("image_0.png")
	require.NoError(t, err)
	_, err = entry.Write([]byte("\x89PNG\r\n\x1a\nimage"))
	require.NoError(t, err)
	require.NoError(t, zipWriter.Close())

	upstream.respond(http.StatusCreated, "application/x-zip-compressed", archive.String())
	recorder := send("/ai/generate-image", `{"input":"a cat","model":"nai-diffusion-4-5-full","action":"generate","parameters":{"n_samples":1}}`)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	assert.Equal(t, "application/x-zip-compressed", recorder.Header().Get("Content-Type"))
	assert.Equal(t, archive.Bytes(), recorder.Body.Bytes())
	assert.Equal(t, "/ai/generate-image", upstream.path)
	assert.Equal(t, "Bearer pst-novelai", upstream.authorization)
	assert.Equal(t, 10_000_000-10_000, walletQuota(), "one image at $0.02")

	nativeOnly := i18n.Translate("", i18n.MsgNovelAIChannelNativeOnly)
	for _, rejected := range []struct{ path, body, message string }{
		{"/v1/chat/completions", `{"model":"nai-diffusion-3","messages":[{"role":"user","content":"a cat"}]}`, i18n.Translate("", i18n.MsgNovelAIPassThroughUnsupported)},
		{"/v1/alpha/search", `{"model":"nai-diffusion-4-5-full","query":"a cat"}`, nativeOnly},
		{"/v1/tools/search", `{"query":"a cat"}`, nativeOnly},
	} {
		t.Run(rejected.path+" "+rejected.body, func(t *testing.T) {
			calls, before := upstream.calls, walletQuota()

			recorder := send(rejected.path, rejected.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			var payload map[string]map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload["error"]["message"], rejected.message)
			assert.Equal(t, calls, upstream.calls, "nothing reaches NovelAI")
			assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
		})
	}
}

// novelAIImagesUpstream is a fake NovelAI image host: it records every
// generation request and answers with the next queued response.
type novelAIImagesUpstream struct {
	mu        sync.Mutex
	requests  []novelAIRecordedRequest
	responses []novelAIQueuedResponse
}

type novelAIRecordedRequest struct {
	path, authorization, contentType, accept string
	acceptSent                               bool
	body                                     map[string]any
}

type novelAIQueuedResponse struct {
	status      int
	contentType string
	body        []byte
}

func (u *novelAIImagesUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = common.Unmarshal(body, &decoded)
	u.mu.Lock()
	_, acceptSent := r.Header["Accept"]
	u.requests = append(u.requests, novelAIRecordedRequest{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("Accept"), acceptSent, decoded})
	response := novelAIQueuedResponse{status: http.StatusInternalServerError, contentType: "application/json", body: []byte(`{"statusCode":500,"message":"no response queued"}`)}
	if len(u.responses) > 0 {
		response, u.responses = u.responses[0], u.responses[1:]
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", response.contentType)
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}

func (u *novelAIImagesUpstream) queue(status int, contentType string, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.responses = append(u.responses, novelAIQueuedResponse{status, contentType, body})
}

func (u *novelAIImagesUpstream) last() novelAIRecordedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[len(u.requests)-1]
}

func (u *novelAIImagesUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

// novelAIPNG is a real PNG of width x height in one colour.
func novelAIPNG(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(fill), image.Point{}, draw.Src)
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, img))
	return buffer.Bytes()
}

// novelAIZip is NovelAI's answer: a zip of image_0.png, image_1.png, ...
func novelAIZip(t *testing.T, images ...[]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for i, data := range images {
		entry, err := writer.Create(fmt.Sprintf("image_%d.png", i))
		require.NoError(t, err)
		_, err = entry.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return archive.Bytes()
}

// OpenAI images clients use a NovelAI channel through /v1/images/generations
// and /v1/images/edits: the request becomes NovelAI's (presets, the model's
// defaults, an edit's image and mask at the canvas size, the inpainting model
// for a mask) and goes to /ai/generate-image as JSON with the channel key and
// no Accept; NovelAI's zip, whether it answered 200 or 201, comes back as an
// OpenAI images response in the requested format with the real size and
// format and the final prompt's estimate as usage. Each call is billed per
// image the client received, at the per-call price or the tiered expression,
// with the prompt estimate and no completion tokens in the consume log. A
// request NovelAI cannot serve is rejected before anything is sent, an
// upstream failure keeps its status in an OpenAI error and charges nothing,
// and an answer without images after a 2xx is final.
func TestNovelAIChannelConvertsOpenAIImages(t *testing.T) {
	setupRelayRouterTestDB(t)
	// Refunds and quota cache updates finish on gopool workers, which read
	// globals the fixture restores: wait for them before it does.
	t.Cleanup(func() {
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}))
	require.NoError(t, i18n.Init())
	logConsume, batchUpdate := common.LogConsumeEnabled, common.BatchUpdateEnabled
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.BatchUpdateEnabled = logConsume, batchUpdate
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	// The streams' idle timeout, which the server's startup sets.
	streamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = streamingTimeout })
	// $0.02 per image, and $0.06 per image as an expression.
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"nai-diffusion-4-5-full":0.02,"nai-diffusion-5-curated":0.02,"nai-diffusion-3":0.02,"nai-diffusion-4-full":0.02,"nai-diffusion-furry-3":0.02,"nai-diffusion-4-curated-preview":0.02,"nai-diffusion-4-5-curated":0.02}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"nai-diffusion-5-full":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"nai-diffusion-5-full":"tier(\"image\", fixed(0.06)) * image_count"}`,
		"perf_metrics_setting.enabled": "false",
	}))

	upstream := &novelAIImagesUpstream{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	user := model.User{Id: 6400000 + int(relayRouterTestUsers.Add(1)), Username: "novelai-openai", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "novelaiopenaikey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	// The channels here describe the base pass, one call a request: their
	// HD pass is off (the HD subtests at the end use a channel of their own).
	channel := model.Channel{Name: "novelai-official", Key: "pst-openai", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-4-5-full,nai-diffusion-5-full,nai-diffusion-5-curated,nai-diffusion-furry-3", BaseURL: common.GetPointer(server.URL), OtherSettings: `{"novelai_hd_disabled":true}`}
	require.NoError(t, model.DB.Create(&channel).Error)
	for _, name := range []string{"nai-diffusion-4-5-full", "nai-diffusion-5-full", "nai-diffusion-5-curated", "nai-diffusion-furry-3"} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true}).Error)
	}
	// This channel makes NovelAI's prompt presets opt-in.
	withoutPresets := model.Channel{Name: "novelai-without-presets", Key: "pst-plain", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-4-full", BaseURL: common.GetPointer(server.URL), OtherSettings: `{"novelai_presets_disabled":true,"novelai_hd_disabled":true}`}
	require.NoError(t, model.DB.Create(&withoutPresets).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: withoutPresets.Id, Model: "nai-diffusion-4-full", Group: "default", Enabled: true}).Error)
	passThrough := model.Channel{Name: "novelai-pass-through", Key: "pst-pass", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-3", BaseURL: common.GetPointer(server.URL), Setting: common.GetPointer(`{"pass_through_body_enabled":true}`)}
	require.NoError(t, model.DB.Create(&passThrough).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: passThrough.Id, Model: "nai-diffusion-3", Group: "default", Enabled: true}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	// /v1/images/generations and /v1/images/edits are host protocol routes.
	SetTaskPluginProtocolRouter(engine)
	send := func(path, contentType string, body []byte) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		// The OpenAI SDKs send this; NovelAI must still answer with its zip.
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer novelaiopenaikey")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	walletQuota := func() int {
		quota, err := model.GetUserQuota(user.Id, true)
		require.NoError(t, err)
		return quota
	}
	lastLog := func() model.Log {
		var logs []model.Log
		require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, model.LogTypeConsume).Order("id").Find(&logs).Error)
		require.NotEmpty(t, logs)
		return logs[len(logs)-1]
	}
	type imagesResponse struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
			Seed    int64  `json:"seed"`
			Size    string `json:"size"`
		} `json:"data"`
		Background   string `json:"background"`
		OutputFormat string `json:"output_format"`
		Size         string `json:"size"`
		Usage        struct {
			InputTokens        int `json:"input_tokens"`
			InputTokensDetails struct {
				TextTokens  int `json:"text_tokens"`
				ImageTokens int `json:"image_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	// decode reads an images response, whose usage is the consume log's.
	decode := func(recorder *httptest.ResponseRecorder) imagesResponse {
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
		var response imagesResponse
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.NotZero(t, response.Created)
		usage := response.Usage
		assert.Positive(t, usage.InputTokensDetails.TextTokens)
		assert.Equal(t, usage.InputTokensDetails.TextTokens+usage.InputTokensDetails.ImageTokens, usage.InputTokens)
		assert.Equal(t, usage.InputTokens+usage.OutputTokens, usage.TotalTokens)
		log := lastLog()
		assert.Equal(t, [2]int{log.PromptTokens, log.CompletionTokens}, [2]int{usage.InputTokens, usage.OutputTokens})
		return response
	}
	// promptText is the estimate of the prompts a NovelAI request sent.
	promptText := func(body map[string]any) int {
		model := body["model"].(string)
		negative := body["parameters"].(map[string]any)["negative_prompt"].(string)
		return tokenkit.Estimate(model, body["input"].(string)) + tokenkit.Estimate(model, negative)
	}
	red, blue := novelAIPNG(t, 16, 16, color.NRGBA{R: 255, A: 255}), novelAIPNG(t, 16, 16, color.NRGBA{B: 255, A: 128})

	t.Run("generation in b64_json, billed per image at the per-call price", func(t *testing.T) {
		upstream.queue(http.StatusCreated, "application/x-zip-compressed", novelAIZip(t, red, blue))
		before := walletQuota()

		recorder := send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"1girl, cat ears","n":2,"size":"1024x1024","negative_prompt":"bad hands","seed":42,"quality":"high","user":"u-1","parameters":{"steps":28}}`))

		response := decode(recorder)
		require.Len(t, response.Data, 2)
		got, err := base64.StdEncoding.DecodeString(response.Data[1].B64JSON)
		require.NoError(t, err)
		assert.Equal(t, blue, got, "NovelAI's PNG as it sent it")
		assert.Empty(t, response.Data[0].URL)
		assert.Equal(t, []int64{42, 43}, []int64{response.Data[0].Seed, response.Data[1].Seed})
		assert.Equal(t, "png", response.OutputFormat)
		assert.Equal(t, "1024x1024", response.Size)
		assert.Equal(t, "opaque", response.Background)
		sent := upstream.last()
		assert.Equal(t, "/ai/generate-image", sent.path)
		assert.Equal(t, "Bearer pst-openai", sent.authorization)
		assert.Equal(t, "application/json", sent.contentType)
		assert.False(t, sent.acceptSent, "NovelAI answers its default zip")
		assert.Equal(t, "generate", sent.body["action"])
		assert.Equal(t, "nai-diffusion-4-5-full", sent.body["model"])
		assert.Equal(t, "1girl, cat ears, very aesthetic, masterpiece, no text", sent.body["input"])
		parameters := sent.body["parameters"].(map[string]any)
		assert.Equal(t, float64(2), parameters["n_samples"])
		assert.Equal(t, float64(28), parameters["steps"])
		assert.Equal(t, float64(42), parameters["seed"])
		assert.Contains(t, parameters["negative_prompt"], ", bad hands")
		assert.NotContains(t, sent.body, "n", "no top-level n reaches NovelAI")
		log := lastLog()
		assert.Equal(t, "nai-diffusion-4-5-full", log.ModelName)
		assert.Equal(t, 20_000, log.Quota, "two images at $0.02")
		assert.Contains(t, log.Content, "Image count 2")
		assert.Equal(t, promptText(sent.body), log.PromptTokens, "the final prompts' estimate, presets included")
		assert.Equal(t, 2*1290, log.CompletionTokens, "two 1024x1024 images")
		assert.Equal(t, 2*1290, response.Usage.OutputTokens)
		assert.Zero(t, response.Usage.InputTokensDetails.ImageTokens)
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("fewer images than reserved settle at the images received", func(t *testing.T) {
		upstream.queue(http.StatusOK, "binary/octet-stream", novelAIZip(t, red))
		before := walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":3}`)))

		assert.Len(t, response.Data, 1)
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
		assert.Equal(t, 1290, lastLog().CompletionTokens, "the image received, not the 3 reserved")
		assert.Equal(t, before-10_000, walletQuota())
	})

	// Other clients' names for the steps, the scale, the negative prompt and
	// the canvas reach NovelAI from the top level, extra_fields or
	// extra_body; presets the client wrote are sent once; a request without
	// steps gets 28. The price stays the per-call price.
	t.Run("other clients' field names and presets they wrote, at the per-call price", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		before := walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"1girl, Very Aesthetic, masterpiece, no text",`+
			`"size":"1024x1024","width":832,"height":1216,"extra_body":{"num_inference_steps":30,"cfg_scale":6,"negativePrompt":"nsfw, bad hands"}}`)))

		assert.Equal(t, "832x1216", response.Size)
		sent := upstream.last()
		assert.Equal(t, "1girl, Very Aesthetic, masterpiece, no text", sent.body["input"], "the quality tags the prompt holds are not added again")
		parameters := sent.body["parameters"].(map[string]any)
		assert.Equal(t, [4]any{float64(832), float64(1216), float64(30), float64(6)}, [4]any{parameters["width"], parameters["height"], parameters["steps"], parameters["scale"]})
		negative := parameters["negative_prompt"].(string)
		assert.True(t, strings.HasPrefix(negative, "lowres, ") && strings.HasSuffix(negative, ", nsfw, bad hands"), negative)
		assert.Equal(t, 1, strings.Count(negative, "nsfw"), "the client's nsfw is not added again")
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
		assert.Equal(t, 1245, lastLog().CompletionTokens, "one 832x1216 image")
		assert.Equal(t, promptText(sent.body), lastLog().PromptTokens)
		assert.Equal(t, before-10_000, walletQuota())

		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		recorder := send("/v1/chat/completions", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"extra_fields":{"guidance_scale":4,"uc":"bad hands"}}`))

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		parameters = upstream.last().body["parameters"].(map[string]any)
		assert.Equal(t, [2]any{float64(28), float64(4)}, [2]any{parameters["steps"], parameters["scale"]}, "the default 28 steps")
		assert.True(t, strings.HasSuffix(parameters["negative_prompt"].(string), ", bad hands"))
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Equal(t, before-20_000, walletQuota())

		// Another client's canvas, guidance and steps past NovelAI's range
		// are fitted into it, as size is, and the quality tags it wrote are
		// not added again, whatever their order.
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		response = decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"masterpiece, 1girl, very aesthetic",`+
			`"width":1000,"height":1000,"cfg_scale":12,"num_inference_steps":60}`)))

		assert.Equal(t, "1024x1024", response.Size)
		sent = upstream.last()
		assert.Equal(t, "masterpiece, 1girl, very aesthetic, no text", sent.body["input"])
		parameters = sent.body["parameters"].(map[string]any)
		assert.Equal(t, [4]any{float64(1024), float64(1024), float64(50), float64(10)}, [4]any{parameters["width"], parameters["height"], parameters["steps"], parameters["scale"]})
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
		assert.Equal(t, before-30_000, walletQuota())
	})

	t.Run("url and jpeg, settled by expression", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, blue, red))
		before := walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-5-full","prompt":"a fox","n":2,"response_format":"url","output_format":"jpeg","output_compression":80}`)))

		require.Len(t, response.Data, 2)
		encoded, ok := strings.CutPrefix(response.Data[0].URL, "data:image/jpeg;base64,")
		require.True(t, ok, response.Data[0].URL[:min(40, len(response.Data[0].URL))])
		data, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		_, err = jpeg.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		assert.Empty(t, response.Data[0].B64JSON)
		assert.Equal(t, "jpeg", response.OutputFormat)
		assert.Equal(t, "png", upstream.last().body["parameters"].(map[string]any)["image_format"], "NovelAI makes a PNG the relay transcodes")
		log := lastLog()
		assert.Equal(t, 60_000, log.Quota, "two images at $0.06")
		var other map[string]any
		require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
		assert.Equal(t, float64(2), other["image_count"])
		assert.Equal(t, "tiered_expr", other["billing_mode"])
		assert.Equal(t, before-60_000, walletQuota())
	})

	t.Run("transparent webp on V5", func(t *testing.T) {
		webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
		require.NoError(t, err)
		upstream.queue(http.StatusOK, "image/webp", webp)

		response := decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-5-full","prompt":"a fox","background":"transparent","output_format":"webp"}`)))

		require.Len(t, response.Data, 1)
		assert.Equal(t, "webp", response.OutputFormat)
		assert.Equal(t, "transparent", response.Background)
		parameters := upstream.last().body["parameters"].(map[string]any)
		assert.Equal(t, "webp", parameters["image_format"])
		assert.Equal(t, true, parameters["tag_hint_transparent_background"])
		assert.Contains(t, upstream.last().body["input"], "a fox, transparent background, ")
	})

	t.Run("a channel can make the prompt presets opt-in", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))

		decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-full","prompt":"a fox","negative_prompt":"blurry"}`)))

		sent := upstream.last()
		assert.Equal(t, "Bearer pst-plain", sent.authorization)
		assert.Equal(t, "a fox", sent.body["input"])
		assert.Equal(t, "blurry", sent.body["parameters"].(map[string]any)["negative_prompt"])
	})

	source := novelAIPNG(t, 300, 200, color.NRGBA{G: 200, A: 255})
	mask := novelAIPNG(t, 300, 200, color.NRGBA{})
	editForm := func(withMask bool, extra map[string]string) (string, []byte) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for key, value := range map[string]string{"model": "nai-diffusion-5-curated", "prompt": "a red scarf", "n": "1"} {
			require.NoError(t, writer.WriteField(key, value))
		}
		for key, value := range extra {
			require.NoError(t, writer.WriteField(key, value))
		}
		part, err := writer.CreateFormFile("image[]", "source.png")
		require.NoError(t, err)
		_, err = part.Write(source)
		require.NoError(t, err)
		if withMask {
			part, err = writer.CreateFormFile("mask", "mask.png")
			require.NoError(t, err)
			_, err = part.Write(mask)
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		return writer.FormDataContentType(), body.Bytes()
	}

	t.Run("multipart edit with a mask inpaints", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		before := walletQuota()
		contentType, body := editForm(true, map[string]string{"strength": "0.6", "parameters[steps]": "20", "response_format": "b64_json"})

		response := decode(send("/v1/images/edits", contentType, body))

		require.Len(t, response.Data, 1)
		assert.Equal(t, "1216x832", response.Size, "the source's aspect at about 1024x1024")
		sent := upstream.last()
		assert.Equal(t, "infill", sent.body["action"])
		assert.Equal(t, "nai-diffusion-4-5-curated-inpainting", sent.body["model"])
		assert.Equal(t, "application/json", sent.contentType, "JSON, not the client's form")
		parameters := sent.body["parameters"].(map[string]any)
		assert.Equal(t, float64(20), parameters["steps"])
		assert.Equal(t, 0.6, parameters["inpaintImg2ImgStrength"])
		assert.Equal(t, 0.6, parameters["img2img"].(map[string]any)["strength"])
		for _, key := range []string{"image", "mask"} {
			data, err := base64.StdEncoding.DecodeString(parameters[key].(string))
			require.NoError(t, err, key)
			config, err := png.DecodeConfig(bytes.NewReader(data))
			require.NoError(t, err, key)
			assert.Equal(t, [2]int{1216, 832}, [2]int{config.Width, config.Height}, key)
		}
		assert.Equal(t, 10_000, lastLog().Quota)
		// The source counts as one 1216x832 input image; the mask does not.
		assert.Equal(t, 1245, response.Usage.InputTokensDetails.ImageTokens)
		assert.Equal(t, promptText(sent.body)+1245, lastLog().PromptTokens)
		assert.Equal(t, 1245, lastLog().CompletionTokens)
		assert.Equal(t, before-10_000, walletQuota())
	})

	t.Run("multipart edit without a mask is img2img", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		contentType, body := editForm(false, map[string]string{"size": "512x512"})

		response := decode(send("/v1/images/edits", contentType, body))

		assert.Equal(t, "512x512", response.Size)
		assert.Equal(t, "img2img", upstream.last().body["action"])
		assert.Equal(t, "nai-diffusion-5-curated", upstream.last().body["model"])
		assert.Equal(t, 0.7, upstream.last().body["parameters"].(map[string]any)["strength"])
		// A 512x512 image is a quarter of 1024x1024: 322.5 tokens, rounded up.
		assert.Equal(t, 323, response.Usage.InputTokensDetails.ImageTokens)
		assert.Equal(t, 323, response.Usage.OutputTokens)
	})

	t.Run("JSON edit with a data URL", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		body := `{"model":"nai-diffusion-4-5-full","prompt":"a red scarf","images":[{"image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(source) + `"}],"input_fidelity":"high"}`

		decode(send("/v1/images/edits", "application/json", []byte(body)))

		assert.Equal(t, "img2img", upstream.last().body["action"])
	})

	encode := base64.StdEncoding.EncodeToString
	// sse is NovelAI's /ai/generate-image-stream answer.
	sse := func(events ...map[string]any) []byte {
		var body strings.Builder
		for _, event := range events {
			data, err := common.Marshal(event)
			require.NoError(t, err)
			fmt.Fprintf(&body, "event: %s\ndata: %s\n\n", event["event_type"], data)
		}
		return []byte(body.String())
	}
	type streamedEvent struct {
		name string
		data map[string]any
	}
	// streamEvents reads an SSE response; [DONE] reads as {"[DONE]": true}.
	streamEvents := func(recorder *httptest.ResponseRecorder) []streamedEvent {
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
		var events []streamedEvent
		for block := range strings.SplitSeq(recorder.Body.String(), "\n\n") {
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

	t.Run("stream generation relays NovelAI's events, billed per completed image", func(t *testing.T) {
		// One partial of the default 28 steps previews after step 14.
		upstream.queue(http.StatusOK, "text/event-stream", sse(
			map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": 12, "image": encode(blue)},
			map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": 13, "image": encode(blue)},
			map[string]any{"event_type": "final", "samp_ix": 0, "image": encode(red)},
			map[string]any{"event_type": "final", "samp_ix": 1, "image": encode(blue)},
		))
		before := walletQuota()

		events := streamEvents(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":2,"stream":true,"partial_images":1,"quality":"high"}`)))

		require.Len(t, events, 3)
		assert.Equal(t, []string{"image_generation.partial_image", "image_generation.completed", "image_generation.completed"}, []string{events[0].name, events[1].name, events[2].name})
		assert.Equal(t, float64(0), events[0].data["partial_image_index"])
		assert.Equal(t, encode(red), events[1].data["b64_json"])
		assert.Equal(t, "high", events[1].data["quality"])
		assert.Equal(t, "1024x1024", events[1].data["size"])
		sent := upstream.last()
		assert.Equal(t, "/ai/generate-image-stream", sent.path)
		assert.Equal(t, "text/event-stream", sent.accept)
		assert.Equal(t, "sse", sent.body["parameters"].(map[string]any)["stream"])
		log := lastLog()
		assert.Equal(t, 20_000, log.Quota, "two images at $0.02")
		assert.Contains(t, log.Content, "Image count 2")
		assert.Equal(t, promptText(sent.body), log.PromptTokens)
		assert.Equal(t, 2*1290, log.CompletionTokens)
		usage := events[2].data["usage"].(map[string]any)
		assert.Equal(t, []float64{float64(log.PromptTokens), 2 * 1290, float64(log.PromptTokens + 2*1290)}, []float64{usage["input_tokens"].(float64), usage["output_tokens"].(float64), usage["total_tokens"].(float64)}, "the usage on the last image")
		assert.Zero(t, events[1].data["usage"].(map[string]any)["input_tokens"])
		assert.Zero(t, events[1].data["usage"].(map[string]any)["output_tokens"])
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("a stream error after 200 bills the images delivered, before any image nothing", func(t *testing.T) {
		failure := map[string]any{"event_type": "error", "code": 500, "message": "Error generating image, an internal error occurred"}
		upstream.queue(http.StatusOK, "text/event-stream", sse(map[string]any{"event_type": "final", "samp_ix": 0, "image": encode(red)}, failure))
		before := walletQuota()

		events := streamEvents(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":2,"stream":true}`)))

		require.Len(t, events, 2, "OpenAI's image streams end on the error event, without [DONE]")
		assert.Equal(t, "image_generation.completed", events[0].name)
		assert.Equal(t, "error", events[1].name)
		assert.Equal(t, "error", events[1].data["type"])
		assert.Contains(t, events[1].data["error"].(map[string]any)["message"], "Error generating image, an internal error occurred")
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
		assert.Equal(t, 1290, lastLog().CompletionTokens, "the image delivered")
		assert.Equal(t, before-10_000, walletQuota())

		upstream.queue(http.StatusOK, "text/event-stream", sse(failure))
		before, logged := walletQuota(), lastLog().Id

		recorder := send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat","stream":true}`))

		require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "Error generating image, an internal error occurred")
		assert.Equal(t, logged, lastLog().Id, "nothing is billed")
		assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("a V3 stream edit answers whole, as image_edit events", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for key, value := range map[string]string{"model": "nai-diffusion-furry-3", "prompt": "a fox", "stream": "true", "partial_images": "2"} {
			require.NoError(t, writer.WriteField(key, value))
		}
		part, err := writer.CreateFormFile("image", "source.png")
		require.NoError(t, err)
		_, err = part.Write(novelAIPNG(t, 64, 64, color.NRGBA{G: 200, A: 255}))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		events := streamEvents(send("/v1/images/edits", writer.FormDataContentType(), body.Bytes()))

		require.Len(t, events, 1, "V3 has no previews")
		assert.Equal(t, "image_edit.completed", events[0].name)
		assert.Equal(t, "image_edit.completed", events[0].data["type"])
		assert.Equal(t, encode(red), events[0].data["b64_json"])
		sent := upstream.last()
		assert.Equal(t, "/ai/generate-image", sent.path, "V3 cannot stream")
		assert.Equal(t, "img2img", sent.body["action"])
		assert.NotContains(t, sent.body["parameters"], "stream")
		assert.False(t, sent.acceptSent)
		// The 64x64 source makes a 1024x1024 canvas.
		log := lastLog()
		assert.Equal(t, promptText(sent.body)+1290, log.PromptTokens)
		assert.Equal(t, 1290, log.CompletionTokens)
		usage := events[0].data["usage"].(map[string]any)
		assert.Equal(t, []float64{float64(log.PromptTokens), 1290, 1290}, []float64{usage["input_tokens"].(float64), usage["output_tokens"].(float64), usage["input_tokens_details"].(map[string]any)["image_tokens"].(float64)})
		assert.Equal(t, 10_000, log.Quota)
		assert.Contains(t, log.Content, "Image count 1")
	})

	// chatContent reads the Markdown PNGs of an assistant message.
	chatContent := func(content string) [][]byte {
		var images [][]byte
		for part := range strings.SplitSeq(strings.TrimPrefix(content, "\n\n"), "\n\n") {
			encoded, ok := strings.CutPrefix(part, "![image](data:image/png;base64,")
			require.True(t, ok, part[:min(len(part), 40)])
			data, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(encoded, ")"))
			require.NoError(t, err)
			images = append(images, data)
		}
		return images
	}

	t.Run("chat generates from the last user message and its image, Markdown PNGs billed per image", func(t *testing.T) {
		upstream.queue(http.StatusCreated, "application/zip", novelAIZip(t, red, blue))
		before := walletQuota()
		// The history carries an earlier image as Markdown base64.
		body := `{"model":"nai-diffusion-4-5-full","n":2,"temperature":0.7,"negative_prompt":"bad hands","messages":[` +
			`{"role":"system","content":"You draw."},{"role":"user","content":"a dog"},{"role":"assistant","content":"![image](data:image/png;base64,` + encode(source) + `)"},` +
			`{"role":"user","content":[{"type":"text","text":"1girl, cat ears"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + encode(source) + `"}}]}]}`

		recorder := send("/v1/chat/completions", "application/json", []byte(body))

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var response struct {
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
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
		assert.Equal(t, "chat.completion", response.Object)
		assert.Equal(t, "nai-diffusion-4-5-full", response.Model)
		require.Len(t, response.Choices, 1)
		assert.Equal(t, "assistant", response.Choices[0].Message.Role)
		assert.Equal(t, "stop", response.Choices[0].FinishReason)
		assert.Equal(t, [][]byte{red, blue}, chatContent(response.Choices[0].Message.Content))
		sent := upstream.last()
		assert.Equal(t, "/ai/generate-image", sent.path)
		assert.False(t, sent.acceptSent)
		assert.Equal(t, "img2img", sent.body["action"])
		assert.Equal(t, "1girl, cat ears, very aesthetic, masterpiece, no text", sent.body["input"], "the last user message, not the history")
		parameters := sent.body["parameters"].(map[string]any)
		assert.Equal(t, float64(2), parameters["n_samples"])
		assert.Contains(t, parameters["negative_prompt"], ", bad hands")
		assert.NotEmpty(t, parameters["image"])
		log := lastLog()
		assert.Equal(t, "nai-diffusion-4-5-full", log.ModelName)
		assert.Equal(t, 20_000, log.Quota, "two images at $0.02: the n multiplier counts once")
		assert.Contains(t, log.Content, "Image count 2")
		// The 300x200 source makes a 1216x832 canvas and counts as one of
		// its images; the history counts nothing.
		assert.Equal(t, promptText(sent.body)+1245, log.PromptTokens, "the final prompts' estimate and the source, not the history")
		assert.Equal(t, 2*1245, log.CompletionTokens, "the images by their area, not as text")
		assert.Equal(t, [3]int{log.PromptTokens, 2 * 1245, log.PromptTokens + 2*1245}, [3]int{response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens})
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("chat stream settles by expression, one chunk per image", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red, blue))
		before := walletQuota()

		events := streamEvents(send("/v1/chat/completions", "application/json", []byte(`{"model":"nai-diffusion-5-full","stream":true,"stream_options":{"include_usage":true},"n":2,"messages":[{"role":"user","content":"a fox"}]}`)))

		require.Len(t, events, 6)
		delta := func(i int) map[string]any {
			return events[i].data["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		}
		assert.Equal(t, "assistant", delta(0)["role"])
		assert.Equal(t, [][]byte{red}, chatContent(delta(1)["content"].(string)))
		assert.Equal(t, [][]byte{blue}, chatContent(delta(2)["content"].(string)))
		assert.Equal(t, "stop", events[3].data["choices"].([]any)[0].(map[string]any)["finish_reason"])
		log := lastLog()
		assert.Equal(t, promptText(upstream.last().body), log.PromptTokens)
		assert.Equal(t, float64(log.PromptTokens), events[4].data["usage"].(map[string]any)["prompt_tokens"])
		assert.Equal(t, 2*1290.0, events[4].data["usage"].(map[string]any)["completion_tokens"])
		assert.Equal(t, map[string]any{"[DONE]": true}, events[5].data)
		assert.Equal(t, "/ai/generate-image", upstream.last().path, "chat asks NovelAI for the whole answer")
		assert.Equal(t, 60_000, log.Quota, "two images at $0.06")
		var other map[string]any
		require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
		assert.Equal(t, float64(2), other["image_count"])
		assert.Equal(t, "tiered_expr", other["billing_mode"])
		assert.Contains(t, log.Content, "Image count 2")
		assert.Equal(t, 2*1290, log.CompletionTokens)
		assert.Equal(t, before-60_000, walletQuota())
	})

	t.Run("a one-image chat stream without usage costs one call", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		before := walletQuota()

		events := streamEvents(send("/v1/chat/completions", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"a fox"}]}`)))

		require.Len(t, events, 4, "role, image, stop, [DONE]")
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
		assert.Equal(t, before-10_000, walletQuota())
	})

	// A chat's images are reserved before NovelAI is asked for them, so a
	// wallet that covers one image cannot be charged for two afterwards.
	t.Run("a chat reserves its images before the send", func(t *testing.T) {
		poor := model.User{Id: 6400000 + int(relayRouterTestUsers.Add(1)), Username: "novelai-poor", AffCode: "novelai-poor", Status: common.UserStatusEnabled, Group: "default", Quota: 15_000}
		require.NoError(t, model.DB.Create(&poor).Error)
		require.NoError(t, model.DB.Create(&model.Token{UserId: poor.Id, Key: "novelaipoorkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
		chat := func(n int) *httptest.ResponseRecorder {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"nai-diffusion-4-5-full","n":%d,"messages":[{"role":"user","content":"a fox"}]}`, n)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer novelaipoorkey")
			engine.ServeHTTP(recorder, request)
			return recorder
		}
		poorQuota := func() int {
			quota, err := model.GetUserQuota(poor.Id, true)
			require.NoError(t, err)
			return quota
		}
		calls := upstream.calls()

		recorder := chat(2)

		assert.NotEqual(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, calls, upstream.calls(), "NovelAI is not asked for images the wallet cannot pay")
		assert.Eventually(t, func() bool { return poorQuota() == 15_000 }, 5*time.Second, 10*time.Millisecond)

		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, red))
		require.Equal(t, http.StatusOK, chat(1).Code)
		assert.Eventually(t, func() bool { return poorQuota() == 5_000 }, 5*time.Second, 10*time.Millisecond)
	})

	// A chat whose NovelAI attempt reserved its images and failed before
	// NovelAI took it is retried on another channel as a plain chat call.
	t.Run("a chat retried on another channel is billed as one call there", func(t *testing.T) {
		retryTimes := common.RetryTimes
		common.RetryTimes = 1
		t.Cleanup(func() { common.RetryTimes = retryTimes })
		forwarder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"nai-diffusion-4-curated-preview","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
		}))
		t.Cleanup(forwarder.Close)
		for priority, channelType := range map[int64]int{10: constant.ChannelTypeNovelAI, 0: constant.ChannelTypeOpenAI} {
			baseURL := server.URL
			if channelType == constant.ChannelTypeOpenAI {
				baseURL = forwarder.URL
			}
			retried := model.Channel{Name: "novelai-retry", Key: "pst-retry", Status: common.ChannelStatusEnabled, Type: channelType, Group: "default", Models: "nai-diffusion-4-curated-preview", BaseURL: &baseURL, Priority: &priority, AutoBan: common.GetPointer(0), OtherSettings: `{"novelai_hd_disabled":true}`}
			require.NoError(t, model.DB.Create(&retried).Error)
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: retried.Id, Model: "nai-diffusion-4-curated-preview", Group: "default", Enabled: true, Priority: &priority}).Error)
		}
		upstream.queue(http.StatusTooManyRequests, "application/json", []byte(`{"statusCode":429,"message":"Concurrent generation is locked"}`))
		calls, before := upstream.calls(), walletQuota()

		recorder := send("/v1/chat/completions", "application/json", []byte(`{"model":"nai-diffusion-4-curated-preview","n":2,"messages":[{"role":"user","content":"a fox"}]}`))

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), `"content":"done"`)
		assert.Equal(t, calls+1, upstream.calls(), "NovelAI was tried first")
		log := lastLog()
		assert.Equal(t, 10_000, log.Quota, "one call at $0.02, not the two images NovelAI was asked for")
		assert.NotContains(t, log.Content, "Image count")
		assert.Eventually(t, func() bool { return walletQuota() == before-10_000 }, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("requests NovelAI cannot serve are rejected before sending and charge nothing", func(t *testing.T) {
		calls, before := upstream.calls(), walletQuota()
		for _, tc := range []struct {
			path, body string
			message    string
		}{
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":5,"size":"1024x1024"}`, "n must be at most 4 for 1024x1024 images"},
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","background":"transparent"}`, "needs a NovelAI V5 model"},
			{"/v1/images/generations", `{"model":"nai-diffusion-5-full","prompt":"a cat","background":"transparent","output_format":"jpeg"}`, "needs a NovelAI V5 model"},
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","output_format":"gif"}`, "output_format gif"},
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","svg":{"model":"x"}}`, "svg is not supported"},
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","cfg":5}`, "cfg is not supported"},
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","parameters":{"n_samples":2}}`, "parameters.n_samples must equal n (1)"},
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","stream":true,"partial_images":4}`, "partial_images must be between 0 and 3"},
			{"/v1/images/edits", `{"model":"nai-diffusion-4-5-full","prompt":"a cat"}`, "no image to edit"},
			{"/v1/images/edits", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","images":[{"file_id":"file-1"}]}`, "file_id is not supported"},
			{"/v1/images/edits", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","images":[{"image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(source) + `"},{"image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(source) + `"}]}`, "the request carries 2"},
			{"/v1/images/generations", `{"model":"nai-diffusion-3","prompt":"a cat"}`, i18n.Translate("", i18n.MsgNovelAIPassThroughUnsupported)},
			{"/v1/chat/completions", `{"model":"nai-diffusion-3","messages":[{"role":"user","content":"a cat"}]}`, i18n.Translate("", i18n.MsgNovelAIPassThroughUnsupported)},
			{"/v1/chat/completions", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"system","content":"a cat"}]}`, i18n.Translate("", i18n.MsgNovelAIChatPromptMissing)},
			{"/v1/chat/completions", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"n":5}`, "n must be at most 4 for 1024x1024 images"},
			{"/v1/chat/completions", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}],"extra_body":{"SMEA":true}}`, "SMEA is not supported"},
		} {
			recorder := send(tc.path, "application/json", []byte(tc.body))
			require.Equal(t, http.StatusBadRequest, recorder.Code, tc.body+" -> "+recorder.Body.String())
			var payload map[string]map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload["error"]["message"], tc.message, tc.body)
		}
		assert.Equal(t, calls, upstream.calls(), "nothing reaches NovelAI")
		assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
	})

	// A channel's parameter override applies after the images were reserved:
	// one that changes parameters.n_samples, the count NovelAI generates, is
	// refused before the send, on images and chat; any other override
	// applies.
	t.Run("a parameter override cannot change the image count", func(t *testing.T) {
		override := `{"operations":[{"path":"parameters.n_samples","mode":"set","value":3}]}`
		overridden := model.Channel{Name: "novelai-override", Key: "pst-override", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
			Models: "nai-diffusion-4-5-curated", BaseURL: common.GetPointer(server.URL), ParamOverride: &override, AutoBan: common.GetPointer(0), OtherSettings: `{"novelai_hd_disabled":true}`}
		require.NoError(t, model.DB.Create(&overridden).Error)
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: overridden.Id, Model: "nai-diffusion-4-5-curated", Group: "default", Enabled: true}).Error)
		calls, before := upstream.calls(), walletQuota()

		for _, tc := range []struct{ path, body string }{
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-curated","prompt":"a cat","n":1}`},
			{"/v1/chat/completions", `{"model":"nai-diffusion-4-5-curated","n":1,"messages":[{"role":"user","content":"a cat"}]}`},
		} {
			recorder := send(tc.path, "application/json", []byte(tc.body))

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "sets parameters.n_samples to 3; it must stay 1")
		}
		assert.Equal(t, calls, upstream.calls(), "NovelAI is not asked for images nobody reserved")
		assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)

		// The tokens are those of the body NovelAI received: a 1536x1024
		// canvas (1935 tokens an image) and the overridden negative caption.
		const negative = "lowres, bad anatomy, bad hands, text, error, missing fingers, extra digit, fewer digits, cropped, worst quality, low quality, normal quality, jpeg artifacts, signature, watermark, username, blurry, artist name"
		require.NoError(t, model.DB.Model(&overridden).Update("param_override", `{"operations":[{"path":"parameters.steps","mode":"set","value":28},`+
			`{"path":"parameters.width","mode":"set","value":1536},{"path":"parameters.height","mode":"set","value":1024},`+
			`{"path":"parameters.v4_negative_prompt.caption.base_caption","mode":"set","value":"`+negative+`"}]}`).Error)
		for _, tc := range []struct {
			path, body string
			n          int
		}{
			{"/v1/images/generations", `{"model":"nai-diffusion-4-5-curated","prompt":"a cat","n":1,"size":"1024x1024"}`, 1},
			{"/v1/chat/completions", `{"model":"nai-diffusion-4-5-curated","n":2,"messages":[{"role":"user","content":"a cat"}]}`, 2},
		} {
			images := [][]byte{red, blue}[:tc.n]
			upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, images...))
			before := walletQuota()

			recorder := send(tc.path, "application/json", []byte(tc.body))

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			if tc.n == 1 {
				decode(recorder)
			}
			sent := upstream.last().body
			parameters := sent["parameters"].(map[string]any)
			assert.Equal(t, float64(28), parameters["steps"])
			assert.Equal(t, float64(tc.n), parameters["n_samples"])
			assert.Equal(t, [2]any{1536.0, 1024.0}, [2]any{parameters["width"], parameters["height"]})
			log := lastLog()
			text := tokenkit.Estimate("nai-diffusion-4-5-curated", sent["input"].(string)) + tokenkit.Estimate("nai-diffusion-4-5-curated", negative)
			assert.Equal(t, text, log.PromptTokens, tc.path+": the overridden negative caption")
			assert.Equal(t, tc.n*1935, log.CompletionTokens, tc.path+": the 1536x1024 images NovelAI made")
			assert.Equal(t, tc.n*10_000, log.Quota, tc.path+": the per-image price is unchanged")
			assert.Equal(t, before-tc.n*10_000, walletQuota())
		}
	})

	t.Run("NovelAI errors keep their status in an OpenAI error and charge nothing", func(t *testing.T) {
		for _, tc := range []struct {
			status  int
			message string
		}{
			{http.StatusBadRequest, "A validation error occurred."},
			{http.StatusUnauthorized, "Access Token is incorrect."},
			{http.StatusPaymentRequired, "Insufficient Anlas"},
			{http.StatusTooManyRequests, "Concurrent generation is locked"},
			{http.StatusInternalServerError, "Error generating image"},
		} {
			upstream.queue(tc.status, "application/json", []byte(fmt.Sprintf(`{"statusCode":%d,"message":%q}`, tc.status, tc.message)))
			before := walletQuota()

			recorder := send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat"}`))

			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			var payload map[string]map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload["error"]["message"], tc.message)
			assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
		}
	})

	t.Run("a 2xx without images is final and charges nothing", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/json", []byte(`{"statusCode":500,"message":"Error generating image"}`))
		calls, before := upstream.calls(), walletQuota()

		recorder := send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat"}`))

		require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), i18n.Translate("", i18n.MsgNovelAINoImage))
		assert.Equal(t, calls+1, upstream.calls())
		assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
	})

	// HD, NovelAI's 1.5x Enhance, is on by default: novelai-hd serves V4.5
	// Full and V5 Full ahead of novelai-official, which would take a retry.
	// Each HD request is one more call to NovelAI and costs the user exactly
	// what it costs without HD, in quota and in usage tokens, whatever HD
	// manages; HD never fails or retries a request.
	hdPriority := int64(5)
	hdChannel := model.Channel{Name: "novelai-hd", Key: "pst-hd", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-4-5-full,nai-diffusion-5-full", BaseURL: common.GetPointer(server.URL), Priority: &hdPriority}
	require.NoError(t, model.DB.Create(&hdChannel).Error)
	for _, name := range []string{"nai-diffusion-4-5-full", "nai-diffusion-5-full"} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: hdChannel.Id, Model: name, Group: "default", Enabled: true, Priority: &hdPriority}).Error)
	}
	base, hd := novelAIPNG(t, 1216, 832, color.NRGBA{G: 255, A: 255}), novelAIPNG(t, 1856, 1280, color.NRGBA{R: 255, B: 255, A: 255})
	enhanced := novelAIZip(t, hd)
	failed := []byte(`{"statusCode":500,"message":"Error generating image"}`)
	// requested is what NovelAI got since calls.
	requested := func(calls int) []novelAIRecordedRequest {
		upstream.mu.Lock()
		defer upstream.mu.Unlock()
		return slices.Clone(upstream.requests[calls:])
	}
	const hdPrompt = `{"model":"nai-diffusion-4-5-full","prompt":"1girl","size":"1216x832"`

	t.Run("HD images cost what standard ones do", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, base))
		upstream.queue(http.StatusOK, "application/zip", enhanced)
		calls, before := upstream.calls(), walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(hdPrompt+`}`)))

		sent := requested(calls)
		require.Len(t, sent, 2)
		assert.Equal(t, "generate", sent[0].body["action"])
		assert.Equal(t, "img2img", sent[1].body["action"])
		parameters := sent[1].body["parameters"].(map[string]any)
		assert.Equal(t, [2]any{1856.0, 1280.0}, [2]any{parameters["width"], parameters["height"]})
		assert.Equal(t, "Bearer pst-hd", sent[1].authorization)
		assert.Equal(t, "1856x1280", response.Size)
		assert.Equal(t, "1856x1280", response.Data[0].Size)
		got, err := base64.StdEncoding.DecodeString(response.Data[0].B64JSON)
		require.NoError(t, err)
		assert.Equal(t, hd, got)
		log := lastLog()
		assert.Contains(t, log.Content, "HD 1856x1280")
		assert.Equal(t, 10_000, log.Quota, "one image at $0.02, as without HD")
		assert.Equal(t, 1245, log.CompletionTokens, "the 1216x832 image the request is priced at")
		assert.Equal(t, before-10_000, walletQuota())
	})

	t.Run("an account without Anlas gets the standard image at the same price", func(t *testing.T) {
		t.Cleanup(func() { novelai.NoteAnlas("pst-hd", 1_000_000) })
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, base))
		upstream.queue(http.StatusPaymentRequired, "application/json", []byte(`{"statusCode":402,"message":"Insufficient Anlas"}`))
		calls, before := upstream.calls(), walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(hdPrompt+`}`)))

		assert.Equal(t, calls+2, upstream.calls())
		assert.Equal(t, "1216x832", response.Size)
		got, err := base64.StdEncoding.DecodeString(response.Data[0].B64JSON)
		require.NoError(t, err)
		assert.Equal(t, base, got)
		log := lastLog()
		assert.Contains(t, log.Content, "HD unavailable, delivered at the standard size")
		assert.Equal(t, 10_000, log.Quota)
		assert.Equal(t, 1245, log.CompletionTokens)
		assert.Equal(t, before-10_000, walletQuota())
		var other map[string]any
		require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
		adminInfo, ok := other["admin_info"].(map[string]any)
		require.True(t, ok, log.Other)
		diagnostics := adminInfo["conversion_diagnostics"].([]any)
		require.Len(t, diagnostics, 1)
		assert.Equal(t, "novelai_hd_fallback", diagnostics[0].(map[string]any)["code"])

		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, base))
		calls = upstream.calls()
		decode(send("/v1/images/generations", "application/json", []byte(hdPrompt+`}`)))
		assert.Equal(t, calls+1, upstream.calls(), "the key skips HD after its 402")
		assert.Contains(t, lastLog().Content, "HD unavailable, delivered at the standard size")
	})

	t.Run("a failed enhance is not retried on another channel", func(t *testing.T) {
		retryTimes := common.RetryTimes
		common.RetryTimes = 1
		t.Cleanup(func() { common.RetryTimes = retryTimes })
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, base))
		upstream.queue(http.StatusInternalServerError, "application/json", failed)
		calls, before := upstream.calls(), walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(hdPrompt+`}`)))

		sent := requested(calls)
		require.Len(t, sent, 2, "the base and its one enhance")
		for _, request := range sent {
			assert.Equal(t, "Bearer pst-hd", request.authorization, "novelai-official is never asked")
		}
		got, err := base64.StdEncoding.DecodeString(response.Data[0].B64JSON)
		require.NoError(t, err)
		assert.Equal(t, base, got)
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Equal(t, before-10_000, walletQuota())
	})

	t.Run("an image_count expression settles the same whatever HD does", func(t *testing.T) {
		two := novelAIZip(t, base, base)
		for _, tc := range []struct {
			name     string
			answers  [][]byte
			statuses []int
			images   int
		}{
			{"both enhanced", [][]byte{two, enhanced, enhanced}, []int{200, 200, 200}, 2},
			{"none enhanced", [][]byte{two, failed}, []int{200, 500}, 2},
			{"one enhanced", [][]byte{two, enhanced, failed}, []int{200, 200, 500}, 2},
			{"fewer images than reserved", [][]byte{novelAIZip(t, base), failed}, []int{200, 500}, 1},
		} {
			for i, answer := range tc.answers {
				contentType := "application/zip"
				if tc.statuses[i] != http.StatusOK {
					contentType = "application/json"
				}
				upstream.queue(tc.statuses[i], contentType, answer)
			}
			calls, before := upstream.calls(), walletQuota()

			response := decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-5-full","prompt":"1girl","size":"1216x832","n":2}`)))

			assert.Equal(t, calls+len(tc.answers), upstream.calls(), tc.name)
			assert.Len(t, response.Data, tc.images, tc.name)
			log := lastLog()
			assert.Equal(t, tc.images*30_000, log.Quota, tc.name+": $0.06 an image delivered")
			assert.Equal(t, tc.images*1245, log.CompletionTokens, tc.name)
			assert.Equal(t, before-tc.images*30_000, walletQuota(), tc.name)
		}
	})

	t.Run("hd false and quality low skip HD", func(t *testing.T) {
		for _, extra := range []string{`,"hd":false`, `,"quality":"low"`} {
			upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, base))
			calls := upstream.calls()

			decode(send("/v1/images/generations", "application/json", []byte(hdPrompt+extra+`}`)))

			assert.Equal(t, calls+1, upstream.calls(), extra)
			assert.NotContains(t, lastLog().Content, "HD", extra)
		}
	})

	t.Run("a chat stream gets HD images at the base usage", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, base))
		upstream.queue(http.StatusOK, "application/zip", enhanced)
		before := walletQuota()

		events := streamEvents(send("/v1/chat/completions", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","size":"1216x832","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"1girl"}]}`)))

		require.Len(t, events, 5, "role, image, stop, usage, [DONE]")
		content := events[1].data["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"].(string)
		assert.Equal(t, [][]byte{hd}, chatContent(content))
		assert.Equal(t, 1245.0, events[3].data["usage"].(map[string]any)["completion_tokens"])
		log := lastLog()
		assert.Contains(t, log.Content, "HD 1856x1280")
		assert.Equal(t, 10_000, log.Quota)
		assert.Equal(t, before-10_000, walletQuota())
	})

	// The upscale, NovelAI's 2x upscaler, is a channel's opt-in: novelai-upscale
	// serves V4.5 Curated ahead of novelai-override, with HD on as by
	// default. Each upscale is one
	// more call to NovelAI after the base (and HD), and the user pays exactly
	// what a request without it costs, in quota and in usage tokens; the
	// consume log notes what became of each image.
	upscaleChannel := model.Channel{Name: "novelai-upscale", Key: "pst-up", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-4-5-curated", BaseURL: common.GetPointer(server.URL), Priority: &hdPriority, OtherSettings: `{"novelai_upscale_enabled":true}`}
	require.NoError(t, model.DB.Create(&upscaleChannel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: upscaleChannel.Id, Model: "nai-diffusion-4-5-curated", Group: "default", Enabled: true, Priority: &hdPriority}).Error)
	square, doubled := novelAIPNG(t, 1024, 1024, color.NRGBA{G: 255, A: 255}), novelAIPNG(t, 2048, 2048, color.NRGBA{R: 255, G: 200, A: 255})
	const upscalePrompt = `{"model":"nai-diffusion-4-5-curated","prompt":"1girl","size":"1024x1024"`
	paths := func(sent []novelAIRecordedRequest) []string {
		var paths []string
		for _, request := range sent {
			paths = append(paths, request.path)
		}
		return paths
	}

	t.Run("an upscaled image costs what a standard one does", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		upstream.queue(http.StatusOK, "application/x-zip-compressed", novelAIZip(t, doubled))
		calls, before := upstream.calls(), walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(upscalePrompt+`,"hd":false}`)))

		sent := requested(calls)
		assert.Equal(t, []string{"/ai/generate-image", "/ai/upscale"}, paths(sent))
		assert.Equal(t, map[string]any{"image": base64.StdEncoding.EncodeToString(square), "model": "nai-diffusion-5-curated", "declared_blur_sigma": 0.0}, sent[1].body)
		assert.Equal(t, "Bearer pst-up", sent[1].authorization)
		assert.False(t, sent[1].acceptSent)
		assert.Equal(t, "2048x2048", response.Size)
		assert.Equal(t, "2048x2048", response.Data[0].Size)
		got, err := base64.StdEncoding.DecodeString(response.Data[0].B64JSON)
		require.NoError(t, err)
		assert.Equal(t, doubled, got)
		log := lastLog()
		assert.Contains(t, log.Content, "Upscaled 2x: 1")
		assert.Contains(t, log.Content, "Image count 1")
		assert.NotContains(t, log.Content, "HD")
		assert.Equal(t, 10_000, log.Quota, "one image at $0.02, as without the upscale")
		assert.Equal(t, novelai.ImageTokens(1024, 1024), log.CompletionTokens, "the 1024x1024 image the request is priced at")
		assert.Equal(t, before-10_000, walletQuota())
	})

	t.Run("HD, then the upscale, at the same price", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, novelAIPNG(t, 1536, 1536, color.NRGBA{B: 255, A: 255})))
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, novelAIPNG(t, 3072, 3072, color.NRGBA{R: 255, A: 255})))
		calls, before := upstream.calls(), walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(upscalePrompt+`}`)))

		sent := requested(calls)
		assert.Equal(t, []string{"/ai/generate-image", "/ai/generate-image", "/ai/upscale"}, paths(sent))
		assert.Equal(t, "img2img", sent[1].body["action"])
		assert.Equal(t, "3072x3072", response.Size)
		log := lastLog()
		assert.Contains(t, log.Content, "HD 1536x1536")
		assert.Contains(t, log.Content, "Upscaled 2x: 1")
		assert.Equal(t, 10_000, log.Quota)
		assert.Equal(t, novelai.ImageTokens(1024, 1024), log.CompletionTokens)
		assert.Equal(t, before-10_000, walletQuota())
	})

	t.Run("an account without Anlas gets the image before the upscale at the same price", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		upstream.queue(http.StatusPaymentRequired, "application/json", []byte(`{"statusCode":402,"message":"Insufficient Anlas"}`))
		calls, before := upstream.calls(), walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(upscalePrompt+`,"hd":false}`)))

		assert.Equal(t, calls+2, upstream.calls())
		assert.Equal(t, "1024x1024", response.Size)
		got, err := base64.StdEncoding.DecodeString(response.Data[0].B64JSON)
		require.NoError(t, err)
		assert.Equal(t, square, got)
		log := lastLog()
		assert.Contains(t, log.Content, "Not upscaled, not enough Anlas: 1")
		assert.Equal(t, 10_000, log.Quota)
		assert.Equal(t, before-10_000, walletQuota())
	})

	t.Run("upscale false skips it, as a JSON field and as a form field", func(t *testing.T) {
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		calls := upstream.calls()
		decode(send("/v1/images/generations", "application/json", []byte(upscalePrompt+`,"hd":false,"upscale":false}`)))
		assert.Equal(t, calls+1, upstream.calls())
		assert.NotContains(t, lastLog().Content, "pscale")

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for key, value := range map[string]string{"model": "nai-diffusion-4-5-curated", "prompt": "a red scarf", "upscale": "false"} {
			require.NoError(t, writer.WriteField(key, value))
		}
		part, err := writer.CreateFormFile("image", "source.png")
		require.NoError(t, err)
		_, err = part.Write(square)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		calls = upstream.calls()
		decode(send("/v1/images/edits", writer.FormDataContentType(), body.Bytes()))
		assert.Equal(t, calls+1, upstream.calls())
		assert.Equal(t, "img2img", upstream.last().body["action"])
		assert.NotContains(t, upstream.last().body["parameters"], "upscale")
		assert.NotContains(t, lastLog().Content, "pscale")
	})

	t.Run("an edit is upscaled, a masked one noted not applicable", func(t *testing.T) {
		edit := func(mask bool) (string, []byte) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for key, value := range map[string]string{"model": "nai-diffusion-4-5-curated", "prompt": "a red scarf"} {
				require.NoError(t, writer.WriteField(key, value))
			}
			for _, name := range []string{"image", "mask"}[:map[bool]int{false: 1, true: 2}[mask]] {
				part, err := writer.CreateFormFile(name, name+".png")
				require.NoError(t, err)
				_, err = part.Write(square)
				require.NoError(t, err)
			}
			require.NoError(t, writer.Close())
			return writer.FormDataContentType(), body.Bytes()
		}
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, doubled))
		calls := upstream.calls()
		contentType, body := edit(false)
		response := decode(send("/v1/images/edits", contentType, body))
		assert.Equal(t, []string{"/ai/generate-image", "/ai/upscale"}, paths(requested(calls)))
		assert.Equal(t, "img2img", requested(calls)[0].body["action"])
		assert.Equal(t, "2048x2048", response.Size)
		assert.Contains(t, lastLog().Content, "Upscaled 2x: 1")

		upstream.queue(http.StatusOK, "application/zip", novelAIZip(t, square))
		calls = upstream.calls()
		contentType, body = edit(true)
		decode(send("/v1/images/edits", contentType, body))
		assert.Equal(t, []string{"/ai/generate-image"}, paths(requested(calls)))
		assert.Equal(t, "infill", requested(calls)[0].body["action"])
		assert.Contains(t, lastLog().Content, "Not upscaled, not applicable: 1")
	})
}

// novelAIQueueUpstream is NovelAI's image host for the queue test, reached
// through a channel's proxy: it records the key of every generation, holds
// the requests of a closed key until the test opens it, and answers a key's
// queued statuses before its default answer, a zip of one PNG.
type novelAIQueueUpstream struct {
	mu      sync.Mutex
	seen    map[string]int
	held    map[string]int
	gates   map[string]chan struct{}
	answers map[string][]int
	zip     []byte
}

func (u *novelAIQueueUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.seen[key]++
	status := http.StatusOK
	if queued := u.answers[key]; len(queued) > 0 {
		status, u.answers[key] = queued[0], queued[1:]
	}
	gate := u.gates[key]
	if gate != nil {
		u.held[key]++
	}
	u.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
		}
		u.mu.Lock()
		u.held[key]--
		u.mu.Unlock()
	}
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"statusCode":%d,"message":"refused"}`, status)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(u.zip)
}

// close holds the next requests of keys until open.
func (u *novelAIQueueUpstream) close(keys ...string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, key := range keys {
		if u.gates[key] == nil {
			u.gates[key] = make(chan struct{})
		}
	}
}

// open answers the held requests of keys, and their next ones at once.
func (u *novelAIQueueUpstream) open(keys ...string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, key := range keys {
		if gate := u.gates[key]; gate != nil {
			close(gate)
			delete(u.gates, key)
		}
	}
}

func (u *novelAIQueueUpstream) answer(key string, statuses ...int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.answers[key] = append(u.answers[key], statuses...)
}

// requests is how many generations reached NovelAI with key.
func (u *novelAIQueueUpstream) requests(key string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.seen[key]
}

// holding is how many generations with key NovelAI holds right now.
func (u *novelAIQueueUpstream) holding(key string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.held[key]
}

// A NovelAI channel on NovelAI's host generates one request at a time per
// key and queues the others, up to the channel's queue limit per key, on the
// least loaded enabled key of a multi-key channel. A request past every
// key's limit fails at once with a 429 that disables neither the channel nor
// a key, charges nothing, and goes to the next channel when retries are on.
// The key the queue chose is the attempt's key: NovelAI gets it, and an
// automatic disable and the error log name it. A retry after NovelAI's error
// answer does not wait behind its own attempt. The queue works alike with
// the channels read from the database and from the memory cache.
func TestNovelAIChannelQueuesGenerationsPerKey(t *testing.T) {
	for _, run := range []struct {
		name        string
		memoryCache bool
	}{{"database", false}, {"memory cache", true}} {
		t.Run(run.name, func(t *testing.T) {
			testNovelAIQueue(t, strings.ReplaceAll(run.name, " ", "-"), run.memoryCache)
		})
	}
}

func testNovelAIQueue(t *testing.T, tag string, memoryCache bool) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}))
	require.NoError(t, i18n.Init())
	logConsume, batchUpdate, cacheEnabled := common.LogConsumeEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled
	autoDisable, errorLog, retryTimes := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes
	disableCodes := operation_setting.AutomaticDisableStatusCodesToString()
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled = logConsume, batchUpdate, cacheEnabled
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = autoDisable, errorLog, retryTimes
		require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(disableCodes))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = true, true
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401,429"))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"nai-diffusion-4-5-full":0.02,"nai-queue-retry":0.02,"nai-queue-c":0.02,"nai-queue-self":0.02,"nai-diffusion-3":0.02}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))

	upstream := &novelAIQueueUpstream{seen: map[string]int{}, held: map[string]int{}, gates: map[string]chan struct{}{}, answers: map[string][]int{},
		zip: novelAIZip(t, novelAIPNG(t, 64, 64, color.RGBA{R: 200, A: 255}))}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	// Refunds, quota updates and automatic disables finish on gopool
	// workers, and a failed test may leave a generation held: both end
	// before the fixture goes.
	var keys []string
	t.Cleanup(func() {
		upstream.open(keys...)
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
	})
	q0, q1, b, r0, r1, d := "pst-q0-"+tag, "pst-q1-"+tag, "pst-b-"+tag, "pst-r0-"+tag, "pst-r1-"+tag, "pst-d-"+tag
	keys = []string{q0, q1, b, r0, r1, d}

	user := model.User{Id: 6400000 + int(relayRouterTestUsers.Add(1)), Username: "novelai-queue", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "novelaiqueuekey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	// Every channel is on NovelAI's host, which the proxy plays.
	host, proxy := "http://image.novelai.net", `{"proxy":"`+server.URL+`"}`
	create := func(channel model.Channel, priority int64) model.Channel {
		channel.Type, channel.Status, channel.Group = constant.ChannelTypeNovelAI, common.ChannelStatusEnabled, "default"
		channel.BaseURL, channel.Setting, channel.Priority, channel.AutoBan = &host, &proxy, &priority, common.GetPointer(1)
		require.NoError(t, model.DB.Create(&channel).Error)
		for _, name := range channel.GetModels() {
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true, Priority: &priority}).Error)
		}
		return channel
	}
	multiKey := model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}
	channelA := create(model.Channel{Name: "novelai-queue-a", Key: q0 + "\n" + q1, Models: "nai-diffusion-4-5-full,nai-queue-retry", OtherSettings: `{"novelai_queue_limit":1}`, ChannelInfo: multiKey}, 10)
	create(model.Channel{Name: "novelai-queue-b", Key: b, Models: "nai-queue-retry"}, 0)
	withDisabledKey := multiKey
	withDisabledKey.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
	create(model.Channel{Name: "novelai-queue-c", Key: r0 + "\n" + r1, Models: "nai-queue-c", OtherSettings: `{"novelai_queue_limit":1}`, ChannelInfo: withDisabledKey}, 0)
	create(model.Channel{Name: "novelai-queue-d", Key: d, Models: "nai-queue-self,nai-diffusion-3"}, 0)
	if memoryCache {
		common.MemoryCacheEnabled = true
		model.InitChannelCache()
	}

	// pointPollingAt makes channelID's next polling pick its key index.
	pointPollingAt := func(channelID, index int) {
		lock := model.GetChannelPollingLock(channelID)
		lock.Lock()
		defer lock.Unlock()
		if memoryCache {
			channel, err := model.CacheGetChannel(channelID)
			require.NoError(t, err)
			channel.ChannelInfo.MultiKeyPollingIndex = index
			return
		}
		channel, err := model.GetChannelById(channelID, true)
		require.NoError(t, err)
		channel.ChannelInfo.MultiKeyPollingIndex = index
		require.NoError(t, channel.SaveChannelInfo())
	}
	// keyStatus is channelID's status and its keys' status list.
	keyStatus := func(channelID int) (int, map[int]int) {
		if !memoryCache {
			channel, err := model.GetChannelById(channelID, true)
			require.NoError(t, err)
			return channel.Status, channel.ChannelInfo.MultiKeyStatusList
		}
		channel, err := model.CacheGetChannel(channelID)
		require.NoError(t, err)
		lock := model.GetChannelPollingLock(channelID)
		lock.Lock()
		defer lock.Unlock()
		return channel.Status, maps.Clone(channel.ChannelInfo.MultiKeyStatusList)
	}

	engine := gin.New()
	SetRelayRouter(engine)
	// /v1/images/generations is a host protocol route.
	SetTaskPluginProtocolRouter(engine)
	send := func(path, body string, responses chan<- *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer novelaiqueuekey")
		engine.ServeHTTP(recorder, request)
		responses <- recorder
	}
	native := func(modelName string) string {
		return `{"input":"a cat","model":"` + modelName + `","action":"generate","parameters":{"n_samples":1}}`
	}
	// awaitResponse is the next response, failing the test after 5 s.
	awaitResponse := func(responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case recorder := <-responses:
			return recorder
		case <-time.After(5 * time.Second):
			require.FailNow(t, "no response")
			return nil
		}
	}
	walletQuota := func() int {
		quota, err := model.GetUserQuota(user.Id, true)
		require.NoError(t, err)
		return quota
	}
	setRetryTimes := func(t *testing.T, times int) {
		common.RetryTimes = times
		t.Cleanup(func() { common.RetryTimes = retryTimes })
	}

	t.Run("a full queue answers 429 and disables nothing", func(t *testing.T) {
		setRetryTimes(t, 0)
		upstream.close(q0, q1)
		before := walletQuota()
		responses := make(chan *httptest.ResponseRecorder, 5)
		for range 5 {
			go send("/ai/generate-image", native("nai-diffusion-4-5-full"), responses)
		}

		rejected := awaitResponse(responses)
		require.Equal(t, http.StatusTooManyRequests, rejected.Code, rejected.Body.String())
		var payload map[string]any
		require.NoError(t, common.Unmarshal(rejected.Body.Bytes(), &payload))
		assert.Equal(t, float64(http.StatusTooManyRequests), payload["statusCode"])
		assert.Contains(t, payload["message"], i18n.Translate("", i18n.MsgNovelAIQueueFull))
		require.Eventually(t, func() bool { return upstream.holding(q0) == 1 && upstream.holding(q1) == 1 }, 5*time.Second, 5*time.Millisecond, "each key generates one request")
		images := make(chan *httptest.ResponseRecorder, 1)
		send("/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":1,"size":"1024x1024"}`, images)
		openAI := <-images
		require.Equal(t, http.StatusTooManyRequests, openAI.Code, openAI.Body.String())
		var openAIPayload map[string]map[string]any
		require.NoError(t, common.Unmarshal(openAI.Body.Bytes(), &openAIPayload))
		assert.Equal(t, "novelai_key_busy", openAIPayload["error"]["code"])

		upstream.open(q0, q1)
		for range 4 {
			recorder := awaitResponse(responses)
			assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		}
		assert.Equal(t, 2, upstream.requests(q0))
		assert.Equal(t, 2, upstream.requests(q1))
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		status, keys := keyStatus(channelA.Id)
		assert.Equal(t, common.ChannelStatusEnabled, status, "the queue's 429 is not the channel's")
		assert.Empty(t, keys)
		assert.Eventually(t, func() bool { return walletQuota() == before-4*10_000 }, 5*time.Second, 10*time.Millisecond, "four images at $0.02, the rejected requests refunded")
	})

	t.Run("a request past the limit goes to the next channel", func(t *testing.T) {
		setRetryTimes(t, 1)
		upstream.close(q0, q1)
		responses := make(chan *httptest.ResponseRecorder, 5)
		for range 5 {
			go send("/ai/generate-image", native("nai-queue-retry"), responses)
		}

		retried := awaitResponse(responses)
		require.Equal(t, http.StatusOK, retried.Code, retried.Body.String())
		assert.Equal(t, 1, upstream.requests(b), "the one request that found the channel full")
		upstream.open(q0, q1)
		for range 4 {
			recorder := awaitResponse(responses)
			assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		}
		assert.Equal(t, 1, upstream.requests(b))
		assert.Equal(t, 4, upstream.requests(q0))
		assert.Equal(t, 4, upstream.requests(q1))
	})

	t.Run("the key the queue chose is the attempt's key", func(t *testing.T) {
		setRetryTimes(t, 0)
		upstream.close(q0)
		pointPollingAt(channelA.Id, 0)
		held := make(chan *httptest.ResponseRecorder, 1)
		go send("/ai/generate-image", native("nai-diffusion-4-5-full"), held)
		require.Eventually(t, func() bool { return upstream.holding(q0) == 1 }, 5*time.Second, 5*time.Millisecond)

		// The distributor picks the busy q0 again; the queue sends to q1.
		pointPollingAt(channelA.Id, 0)
		upstream.answer(q1, http.StatusUnauthorized)
		sentToQ1 := upstream.requests(q1)
		refused := make(chan *httptest.ResponseRecorder, 1)
		go send("/ai/generate-image", native("nai-diffusion-4-5-full"), refused)
		recorder := awaitResponse(refused)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
		assert.Equal(t, sentToQ1+1, upstream.requests(q1))
		assert.Eventually(t, func() bool {
			status, keys := keyStatus(channelA.Id)
			_, q0Disabled := keys[0]
			return status == common.ChannelStatusEnabled && keys[1] == common.ChannelStatusAutoDisabled && !q0Disabled
		}, 5*time.Second, 10*time.Millisecond, "q1 is disabled, q0 and the channel stay enabled")
		var errorLog model.Log
		require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, model.LogTypeError).Order("id desc").First(&errorLog).Error)
		var other map[string]any
		require.NoError(t, common.UnmarshalJsonStr(errorLog.Other, &other))
		adminInfo, ok := other["admin_info"].(map[string]any)
		require.True(t, ok, errorLog.Other)
		assert.Equal(t, float64(1), adminInfo["multi_key_index"])

		upstream.open(q0)
		recorder = awaitResponse(held)
		assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	})

	t.Run("a disabled key gets no requests", func(t *testing.T) {
		setRetryTimes(t, 0)
		upstream.close(r0)
		held := make(chan *httptest.ResponseRecorder, 1)
		go send("/ai/generate-image", native("nai-queue-c"), held)
		require.Eventually(t, func() bool { return upstream.holding(r0) == 1 }, 5*time.Second, 5*time.Millisecond)

		// One of two more requests waits for r0, the other finds its line
		// full: r1 is not a candidate.
		responses := make(chan *httptest.ResponseRecorder, 2)
		go send("/ai/generate-image", native("nai-queue-c"), responses)
		go send("/ai/generate-image", native("nai-queue-c"), responses)
		rejected := awaitResponse(responses)
		assert.Equal(t, http.StatusTooManyRequests, rejected.Code, rejected.Body.String())
		assert.Equal(t, 1, upstream.requests(r0), "the waiting request has not reached NovelAI")

		upstream.open(r0)
		for _, recorder := range []*httptest.ResponseRecorder{awaitResponse(held), awaitResponse(responses)} {
			assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		}
		assert.Equal(t, 2, upstream.requests(r0))
		assert.Zero(t, upstream.requests(r1))
	})

	// The generation wait is not shortened: an attempt that kept its key
	// would keep the retry waiting past the test's 5 s.
	t.Run("a retry does not wait behind its own attempt", func(t *testing.T) {
		setRetryTimes(t, 1)
		for i, request := range []struct{ path, body string }{
			{"/ai/generate-image", native("nai-queue-self")},
			{"/v1/images/generations", `{"model":"nai-diffusion-3","prompt":"a cat","n":1,"size":"1024x1024"}`},
		} {
			upstream.answer(d, http.StatusInternalServerError)
			responses := make(chan *httptest.ResponseRecorder, 1)
			go send(request.path, request.body, responses)
			recorder := awaitResponse(responses)
			assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, 2*(i+1), upstream.requests(d), "the failed attempt and its retry")
		}
	})
}

// NovelAI clients read the subscription before generating. new-api answers
// for the calling token without an upstream: Opus (tier 3) with unlimited
// image generation, and the quota the token can still spend as Anlas at
// 1 Anlas = 0.01 USD, capped by the wallet. A request without a valid token
// gets NovelAI's error shape there, while other token routes keep their own.
func TestNovelAIAccountRoutesDescribeTheCallingToken(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	// $3 in the wallet.
	user := model.User{Username: "novelai-account", Status: common.UserStatusEnabled, Group: "default", Quota: 1_500_000}
	require.NoError(t, model.DB.Create(&user).Error)
	expiry := time.Now().Add(time.Hour).Unix()
	tokens := []struct {
		token     model.Token
		anlas     float64
		expiresAt float64
	}{
		{model.Token{Key: "novelailimited", RemainQuota: 1_000_000, ExpiredTime: expiry, CreatedTime: 1_700_000_000}, 200, float64(expiry)},
		{model.Token{Key: "novelaiwalletcapped", RemainQuota: 5_000_000, ExpiredTime: -1, CreatedTime: 1_700_000_001}, 300, math.MaxInt32},
		{model.Token{Key: "novelaiunlimited", UnlimitedQuota: true, ExpiredTime: -1, CreatedTime: 1_700_000_002}, 300, math.MaxInt32},
	}
	for i := range tokens {
		tokens[i].token.UserId, tokens[i].token.Status = user.Id, common.TokenStatusEnabled
		require.NoError(t, model.DB.Create(&tokens[i].token).Error)
	}
	engine := gin.New()
	SetRelayRouter(engine)
	SetDashboardRouter(engine)
	SetApiRouter(engine)
	get := func(path, key string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	for _, tc := range tokens {
		for _, path := range []string{"/user/subscription", "/v1/user/subscription", "/user/data", "/v1/user/data"} {
			t.Run(tc.token.Key+path, func(t *testing.T) {
				recorder := get(path, tc.token.Key)
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				var payload map[string]any
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
				subscription := payload
				if strings.HasSuffix(path, "/data") {
					subscription = payload["subscription"].(map[string]any)
					assert.Contains(t, payload, "priority")
					assert.Contains(t, payload, "keystore")
					information := payload["information"].(map[string]any)
					assert.Equal(t, float64(tc.token.CreatedTime), information["accountCreatedAt"])
				}
				assert.Equal(t, float64(3), subscription["tier"])
				assert.Equal(t, true, subscription["active"])
				assert.Equal(t, tc.expiresAt, subscription["expiresAt"])
				perks := subscription["perks"].(map[string]any)
				assert.Equal(t, true, perks["unlimitedImageGeneration"])
				assert.Equal(t, true, perks["imageGeneration"])
				steps := subscription["trainingStepsLeft"].(map[string]any)
				assert.Equal(t, tc.anlas, steps["fixedTrainingStepsLeft"])
			})
		}
	}

	for _, path := range []string{"/user/subscription", "/v1/user/subscription", "/user/data", "/v1/user/data", "/v1/dashboard/billing/subscription", "/api/user/self"} {
		recorder := get(path, "")
		require.Equal(t, http.StatusUnauthorized, recorder.Code, path)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload), path)
		if strings.HasPrefix(path, "/user/") || strings.HasPrefix(path, "/v1/user/") {
			assert.Equal(t, map[string]any{"statusCode": float64(http.StatusUnauthorized), "message": payload["message"]}, payload, path)
			assert.Contains(t, payload["message"], i18n.Translate("", i18n.MsgTokenInvalid), path)
			continue
		}
		assert.NotContains(t, payload, "statusCode", path)
	}
	dashboard := get("/v1/dashboard/billing/subscription", "")
	var openAIError map[string]map[string]any
	require.NoError(t, common.Unmarshal(dashboard.Body.Bytes(), &openAIError))
	assert.Contains(t, openAIError["error"]["message"], i18n.Translate("", i18n.MsgTokenInvalid))
}

func TestGeminiRelayHandsLongRunningPredictionToPluginRoute(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "veo-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "veotestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	source, err := plugins.Source("google")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	_, err = registry.RegisterFactory(source, jsplugin.Options{Key: "google"})
	require.NoError(t, err)
	// The plugin route authenticates as in production, then names itself.
	handlers := func(generation *jsplugin.RoutingGeneration, group jsplugin.RouteGroup) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, group)
		return []gin.HandlerFunc{production[0], production[1], production[2], func(c *gin.Context) {
			c.String(http.StatusOK, "plugin %s for user %d", group.Bindings[0].Plugin.Meta.Key, c.GetInt("id"))
		}}
	}
	require.NoError(t, registry.SetGenerationPreparer(newPluginGenerationBuilder(engine.Routes(), nil, handlers).prepare))
	require.Empty(t, registry.RoutingErrors())
	previousRegistry := defaultPluginDispatcher.registry
	defaultPluginDispatcher.registry = registry
	t.Cleanup(func() { defaultPluginDispatcher.registry = previousRegistry })
	engine.NoRoute(defaultPluginDispatcher.dispatch)

	for _, testCase := range []struct {
		method   string
		path     string
		toPlugin bool
	}{
		{method: http.MethodPost, path: "/v1beta/models/veo-3.1-generate-preview:predictLongRunning", toPlugin: true},
		{method: http.MethodGet, path: "/v1beta/models/veo-3.1-generate-preview/operations/task_sample", toPlugin: true},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.5-flash:generateContent"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(`{}`))
			request.Header.Set("x-goog-api-key", "veotestkey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if testCase.toPlugin {
				assert.Equal(t, fmt.Sprintf("plugin google for user %d", user.Id), recorder.Body.String())
				return
			}
			// The relay looks for a Gemini channel serving the model; there is none.
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"model_not_found"`)
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	// A shared in-memory SQLite database lives only while one of its
	// connections is open, and the pool closes each connection after
	// SQL_MAX_LIFETIME (60s by default), which a long test outlives under
	// -race: hold one connection for the whole test.
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	keepAlive, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		_ = keepAlive.Close()
		_ = sqlDB.Close()
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}

// upstreamQuotaBody is new-api's own answer when the account a channel uses
// on it cannot pay for the request.
const upstreamQuotaBody = `{"error":{"message":"预扣费额度失败, 用户剩余额度: ＄0.120000, 需要预扣费额度: ＄0.280000 (request id: 20261104012629802206256826)","type":"new_api_error","code":"pre_consume_token_quota_failed"}}`

const upstreamQuotaRequestID = "20261104012629802206256826"

// A channel with "hide upstream balance errors" on answers an upstream's
// out-of-balance error with its own message (上游tokens数量不足 unless it
// sets one, with new-api's request id) and status (403 unless it sets one)
// in every response shape, and the user's error log shows only that, while
// the upstream's answer stays for administrators. Retries and automatic
// disabling still go by the upstream's answer. With the switch off (the
// default, also on a channel saved before it existed or saved off with a
// message), the user's own balance shortfall, and a rate limit worded as
// quota, nothing changes.
func TestRelayHidesUpstreamQuotaErrorsBehindTheChannelMessage(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.Midjourney{}))
	require.NoError(t, i18n.Init())
	// Midjourney requests use the shared client directly.
	service.InitHttpClient()
	logConsume, batchUpdate := common.LogConsumeEnabled, common.BatchUpdateEnabled
	autoDisable, errorLog, retryTimes := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes
	disableCodes, disableKeywords := operation_setting.AutomaticDisableStatusCodesToString(), operation_setting.AutomaticDisableKeywordsToString()
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	t.Cleanup(func() {
		// Refunds and automatic disables finish on gopool workers.
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		common.LogConsumeEnabled, common.BatchUpdateEnabled = logConsume, batchUpdate
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = autoDisable, errorLog, retryTimes
		require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(disableCodes))
		operation_setting.AutomaticDisableKeywordsFromString(disableKeywords)
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = true, true, 0
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401"))
	operation_setting.AutomaticDisableKeywordsFromString("")
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))

	// answering is an upstream that answers every request alike and counts them.
	type answering struct {
		*httptest.Server
		calls atomic.Int32
	}
	// Each answer carries the upstream's request id when it is new-api.
	answer := func(status int, body, requestID string) *answering {
		upstream := &answering{}
		upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstream.calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if requestID != "" {
				w.Header().Set(common.RequestIdKey, requestID)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(upstream.Close)
		return upstream
	}
	broke := answer(http.StatusForbidden, upstreamQuotaBody, upstreamQuotaRequestID)
	// An upstream that sends no request id of its own.
	served := answer(http.StatusOK, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"uqe-retry","choices":[{"index":0,"message":{"role":"assistant","content":"served by the second channel"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "")
	geminiQuotaMessage := "You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits. Please retry in 43.659002305s."
	rateLimited := answer(http.StatusTooManyRequests, `{"error":{"code":429,"message":"`+geminiQuotaMessage+`","status":"RESOURCE_EXHAUSTED"}}`, "")

	user := model.User{Id: 6500000 + int(relayRouterTestUsers.Add(1)), Username: "uqe-user", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "uqetestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	poor := model.User{Id: 6500000 + int(relayRouterTestUsers.Add(1)), Username: "uqe-poor", AffCode: "uqe-poor", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&poor).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: poor.Id, Key: "uqepoorkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	prices := map[string]float64{}
	create := func(name, modelName string, upstream *answering, settings string, priority int64, autoBan int) model.Channel {
		channel := model.Channel{Name: name, Key: "sk-" + name, Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: modelName, BaseURL: &upstream.URL, OtherSettings: settings, Priority: &priority, AutoBan: &autoBan}
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: modelName, Group: "default", Enabled: true, Priority: &priority}).Error)
		prices[modelName] = 0.01
		return channel
	}
	const on = `{"upstream_quota_error_mask_enabled":true}`
	hiding := create("uqe-default", "uqe-default", broke, on, 0, 0)
	create("uqe-custom", "uqe-custom", broke, `{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_message":"  余额维护中，请稍后再试  "}`, 0, 0)
	create("uqe-status", "uqe-status", broke, `{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_status_code":429}`, 0, 0)
	create("uqe-unsaved", "uqe-unsaved", broke, "", 0, 0)
	create("uqe-legacy", "uqe-legacy", broke, `{"novelai_hd_disabled":true}`, 0, 0)
	create("uqe-saved-off", "uqe-saved-off", broke, `{"upstream_quota_error_message":"余额维护中","upstream_quota_error_status_code":429}`, 0, 0)
	create("uqe-rate-limited", "uqe-rate-limited", rateLimited, on, 0, 0)
	first := create("uqe-retry-first", "uqe-retry", broke, on, 10, 0)
	create("uqe-retry-second", "uqe-retry", served, on, 0, 0)
	byKeyword := create("uqe-disable-keyword", "uqe-disable-keyword", broke, on, 0, 1)
	byStatus := create("uqe-disable-status", "uqe-disable-status", broke, `{"upstream_quota_error_mask_enabled":true,"upstream_quota_error_status_code":429}`, 0, 1)
	// An upstream that answers only once the test lets it, so that the
	// relay's keepalive goes out first.
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	held := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, upstreamQuotaBody)
	}))
	t.Cleanup(held.Close)
	create("uqe-keepalive", "uqe-keepalive", &answering{Server: held}, on, 0, 0)
	// new-api upgrades a Realtime connection before it bills the request, so
	// its pre-consume failure arrives as the first event, with its request id
	// in the event id.
	realtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer ws.Close()
		var event map[string]any
		require.NoError(t, common.UnmarshalJsonStr(upstreamQuotaBody, &event))
		event["type"], event["event_id"] = "error", "evt_"+upstreamQuotaRequestID
		assert.NoError(t, ws.WriteJSON(event))
	}))
	t.Cleanup(realtime.Close)
	create("uqe-realtime", "uqe-realtime", &answering{Server: realtime}, on, 0, 0)
	// new-api's Midjourney answer when the account a channel uses is short.
	mjBroke := answer(http.StatusBadRequest, `{"description":"quota_not_enough ","type":"upstream_error","code":4}`, "")
	midjourney := func(name, modelName string, upstream *answering, settings string) model.Channel {
		channel := create(name, modelName, upstream, settings, 0, 0)
		require.NoError(t, model.DB.Model(&channel).Update("type", constant.ChannelTypeMidjourneyPlus).Error)
		return channel
	}
	midjourney("uqe-mj-imagine", "mj_imagine", broke, on)
	midjourney("uqe-mj-swap", "swap_face", mjBroke, on)
	// An action goes to the channel of the task it acts on, which hides
	// balance errors, though channel selection picks one that does not.
	midjourney("uqe-mj-picked", "mj_upscale", broke, "")
	origin := midjourney("uqe-mj-origin", "uqe-mj-origin", mjBroke, on)
	require.NoError(t, model.DB.Create(&model.Midjourney{UserId: user.Id, Action: "IMAGINE", MjId: "uqe-origin-task", Prompt: "a cat", Status: "SUCCESS", Progress: "100%", ChannelId: origin.Id}).Error)
	priceJSON, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetRelayRouter(engine)
	send := func(key, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+key)
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	chat := func(modelName string) *httptest.ResponseRecorder {
		return send("uqetestkey", "/v1/chat/completions", `{"model":"`+modelName+`","messages":[{"role":"user","content":"hi"}]}`)
	}
	type openAIError struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	errorOf := func(recorder *httptest.ResponseRecorder) openAIError {
		var payload struct {
			Error openAIError `json:"error"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload), recorder.Body.String())
		return payload.Error
	}
	// assertHidden checks that nothing of the upstream's answer is in text.
	assertHidden := func(t *testing.T, text string) {
		t.Helper()
		for _, upstreamPart := range []string{"预扣费额度失败", "0.12", upstreamQuotaRequestID, "pre_consume_token_quota_failed"} {
			assert.NotContains(t, text, upstreamPart)
		}
	}
	lastErrorLog := func(t *testing.T, channelID int) model.Log {
		t.Helper()
		var errorLog model.Log
		require.NoError(t, model.LOG_DB.Where("type = ? AND channel_id = ?", model.LogTypeError, channelID).Order("id desc").First(&errorLog).Error)
		return errorLog
	}
	userView := func(t *testing.T) string {
		t.Helper()
		logs, _, err := model.GetUserLogs(user.Id, model.LogFilter{}, 0, 1000)
		require.NoError(t, err)
		view, err := common.Marshal(logs)
		require.NoError(t, err)
		return string(view)
	}

	t.Run("the channel's default message replaces the upstream's balance error", func(t *testing.T) {
		recorder := chat("uqe-default")

		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		ours := recorder.Header().Get(common.RequestIdKey)
		require.NotEmpty(t, ours)
		assert.Equal(t, openAIError{Message: "上游tokens数量不足 (request id: " + ours + ")", Type: "new_api_error", Code: "service_quota_exhausted"}, errorOf(recorder))
		assertHidden(t, recorder.Body.String())

		errorLog := lastErrorLog(t, hiding.Id)
		assert.Equal(t, "status_code=403, 上游tokens数量不足", errorLog.Content)
		assert.Equal(t, ours, errorLog.RequestId)
		assert.Empty(t, errorLog.UpstreamRequestId)
		other, err := common.StrToMap(errorLog.Other)
		require.NoError(t, err)
		assert.Equal(t, "new_api_error", other["error_type"])
		assert.Equal(t, "service_quota_exhausted", other["error_code"])
		assert.Equal(t, float64(http.StatusForbidden), other["status_code"])
		adminInfo, ok := other["admin_info"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "status_code=403, 预扣费额度失败, 用户剩余额度: ＄0.120000, 需要预扣费额度: ＄0.280000 (request id: 20261104012629802206256826)", adminInfo["upstream_error"])
		assert.Equal(t, float64(http.StatusForbidden), adminInfo["upstream_status_code"])
		assert.Equal(t, "pre_consume_token_quota_failed", adminInfo["upstream_error_code"])
		assert.Equal(t, upstreamQuotaRequestID, adminInfo["upstream_request_id"])
		assertHidden(t, userView(t))
	})

	for _, tc := range []struct {
		name, model, message string
		status               int
	}{
		{"a custom Chinese message replaces it", "uqe-custom", "余额维护中，请稍后再试", http.StatusForbidden},
		{"a custom status replaces its status", "uqe-status", "上游tokens数量不足", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := chat(tc.model)

			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			ours := recorder.Header().Get(common.RequestIdKey)
			assert.Equal(t, openAIError{Message: tc.message + " (request id: " + ours + ")", Type: "new_api_error", Code: "service_quota_exhausted"}, errorOf(recorder))
			assertHidden(t, recorder.Body.String())
			var errorLog model.Log
			require.NoError(t, model.LOG_DB.Where("type = ? AND request_id = ?", model.LogTypeError, ours).First(&errorLog).Error)
			assert.Equal(t, fmt.Sprintf("status_code=%d, %s", tc.status, tc.message), errorLog.Content)
			other, err := common.StrToMap(errorLog.Other)
			require.NoError(t, err)
			assert.Equal(t, float64(tc.status), other["status_code"], "the user's log shows the status the user got")
		})
	}

	t.Run("the Claude format gets the channel's message as a Claude error", func(t *testing.T) {
		recorder := send("uqetestkey", "/v1/messages", `{"model":"uqe-default","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		ours := recorder.Header().Get(common.RequestIdKey)
		assert.JSONEq(t, `{"type":"error","error":{"type":"new_api_error","message":"上游tokens数量不足 (request id: `+ours+`)"}}`, recorder.Body.String())
	})

	t.Run("a stream that fails before its first byte gets the channel's message as JSON", func(t *testing.T) {
		recorder := send("uqetestkey", "/v1/chat/completions", `{"model":"uqe-default","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json"), recorder.Header().Get("Content-Type"))
		ours := recorder.Header().Get(common.RequestIdKey)
		assert.Equal(t, openAIError{Message: "上游tokens数量不足 (request id: " + ours + ")", Type: "new_api_error", Code: "service_quota_exhausted"}, errorOf(recorder))
		assertHidden(t, recorder.Body.String())
	})

	t.Run("the Gemini format gets the channel's message", func(t *testing.T) {
		recorder := send("uqetestkey", "/v1beta/models/uqe-default:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		ours := recorder.Header().Get(common.RequestIdKey)
		assert.Equal(t, openAIError{Message: "上游tokens数量不足 (request id: " + ours + ")", Type: "new_api_error", Code: "service_quota_exhausted"}, errorOf(recorder))
	})

	t.Run("a NovelAI client gets the channel's message in NovelAI's error shape, not the upstream body", func(t *testing.T) {
		recorder := send("uqetestkey", "/ai/generate-image", `{"input":"a cat","model":"uqe-default","action":"generate","parameters":{"n_samples":1}}`)

		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		ours := recorder.Header().Get(common.RequestIdKey)
		assert.JSONEq(t, `{"statusCode":403,"message":"上游tokens数量不足 (request id: `+ours+`)"}`, recorder.Body.String())
	})

	t.Run("a stream committed by a keepalive ends with the channel's message as an event", func(t *testing.T) {
		settings := operation_setting.GetGeneralSetting()
		pingEnabled, pingSeconds := settings.PingIntervalEnabled, settings.PingIntervalSeconds
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
		t.Cleanup(func() { settings.PingIntervalEnabled, settings.PingIntervalSeconds = pingEnabled, pingSeconds })
		server := httptest.NewServer(engine)
		t.Cleanup(server.Close)
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"uqe-keepalive","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer uqetestkey")

		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		reader := bufio.NewReader(response.Body)
		keepAlive, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, ": PING\n", keepAlive)
		releaseOnce.Do(func() { close(release) })
		rest, err := io.ReadAll(reader)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, response.StatusCode, "the keepalive committed the stream")
		ours := response.Header.Get(common.RequestIdKey)
		assert.Contains(t, string(rest), `data: {"error":{"message":"上游tokens数量不足 (request id: `+ours+`)","type":"new_api_error","param":"","code":"service_quota_exhausted"}}`)
		assertHidden(t, string(rest))
	})

	t.Run("a Realtime client gets the channel's message in place of the upstream's error event", func(t *testing.T) {
		server := httptest.NewServer(engine)
		t.Cleanup(server.Close)
		client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/realtime?model=uqe-realtime", http.Header{"Authorization": []string{"Bearer uqetestkey"}})
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })
		require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))

		_, message, err := client.ReadMessage()

		require.NoError(t, err)
		var event struct {
			Type    string      `json:"type"`
			EventID string      `json:"event_id"`
			Error   openAIError `json:"error"`
		}
		require.NoError(t, common.Unmarshal(message, &event), string(message))
		ours := strings.TrimPrefix(event.EventID, "evt_")
		require.NotEqual(t, upstreamQuotaRequestID, ours, string(message))
		assert.Equal(t, "error", event.Type)
		assert.Equal(t, openAIError{Message: "上游tokens数量不足 (request id: " + ours + ")", Type: "new_api_error", Code: "service_quota_exhausted"}, event.Error)
		assertHidden(t, string(message))
	})

	for _, tc := range []struct{ name, path, body string }{
		{"a Midjourney submission gets the channel's message, also in the user's task list", "/mj/submit/imagine", `{"prompt":"a cat"}`},
		{"a face swap gets the channel's message", "/mj/insight-face/swap", `{"sourceBase64":"data:image/png;base64,AAAA","targetBase64":"data:image/png;base64,AAAA"}`},
		{"an action follows the setting of the channel of its task", "/mj/submit/change", `{"taskId":"uqe-origin-task","action":"UPSCALE","index":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := send("uqetestkey", tc.path, tc.body)

			require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			ours := recorder.Header().Get(common.RequestIdKey)
			assert.JSONEq(t, `{"code":4,"description":"上游tokens数量不足 (request id: `+ours+`)","properties":null,"result":""}`, recorder.Body.String())
			var task model.Midjourney
			require.NoError(t, model.DB.Where("user_id = ?", user.Id).Order("id desc").First(&task).Error)
			assert.Equal(t, "上游tokens数量不足", task.Description)
			for _, shown := range []string{recorder.Body.String(), task.Description, task.FailReason} {
				assertHidden(t, shown)
				assert.NotContains(t, shown, "quota_not_enough")
			}
		})
	}

	t.Run("a retry serves the request on the next channel and the failed attempt's log is hidden too", func(t *testing.T) {
		common.RetryTimes = 1
		t.Cleanup(func() { common.RetryTimes = 0 })
		brokeCalls, servedCalls := broke.calls.Load(), served.calls.Load()

		recorder := chat("uqe-retry")

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "served by the second channel")
		assert.Equal(t, brokeCalls+1, broke.calls.Load())
		assert.Equal(t, servedCalls+1, served.calls.Load())
		errorLog := lastErrorLog(t, first.Id)
		assert.Equal(t, "status_code=403, 上游tokens数量不足", errorLog.Content)
		assert.Equal(t, recorder.Header().Get(common.RequestIdKey), errorLog.RequestId)
		assertHidden(t, userView(t))
	})

	// The upstream's answer matches the keyword and the status rule; the
	// channel's message and its status 429 match neither.
	for _, tc := range []struct {
		name, model, keywords, codes string
		channel                      model.Channel
	}{
		{"a disable keyword of the upstream's answer still disables the channel", "uqe-disable-keyword", "预扣费额度失败", "401", byKeyword},
		{"the upstream's status still disables the channel", "uqe-disable-status", "", "401,403", byStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.AutomaticDisableKeywordsFromString(tc.keywords)
			require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(tc.codes))
			t.Cleanup(func() {
				operation_setting.AutomaticDisableKeywordsFromString("")
				require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401"))
			})

			recorder := chat(tc.model)

			assert.NotContains(t, recorder.Body.String(), "预扣费额度失败")
			assert.Eventually(t, func() bool {
				channel, err := model.GetChannelById(tc.channel.Id, true)
				require.NoError(t, err)
				return channel.Status == common.ChannelStatusAutoDisabled
			}, 5*time.Second, 10*time.Millisecond)
			channel, err := model.GetChannelById(tc.channel.Id, true)
			require.NoError(t, err)
			assert.Contains(t, channel.GetOtherInfo()["status_reason"], "预扣费额度失败", "administrators read the upstream's answer")
		})
	}

	// The default: the upstream's error goes through as it does without the
	// setting, also on a channel whose settings predate it or that saved a
	// message with the switch off.
	for _, modelName := range []string{"uqe-unsaved", "uqe-legacy", "uqe-saved-off"} {
		t.Run("with the switch off the upstream's answer goes through: "+modelName, func(t *testing.T) {
			recorder := chat(modelName)

			require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			assert.Equal(t, openAIError{Message: "预扣费额度失败, 用户剩余额度: ＄0.120000, 需要预扣费额度: ＄0.280000 (request id: 20261104012629802206256826)", Type: "new_api_error", Code: "pre_consume_token_quota_failed"}, errorOf(recorder))
			var errorLog model.Log
			require.NoError(t, model.LOG_DB.Where("type = ? AND request_id = ?", model.LogTypeError, recorder.Header().Get(common.RequestIdKey)).First(&errorLog).Error)
			assert.Contains(t, errorLog.Content, "预扣费额度失败")
			assert.Equal(t, upstreamQuotaRequestID, errorLog.UpstreamRequestId)
		})
	}

	t.Run("the user's own balance shortfall still tells the user", func(t *testing.T) {
		calls := broke.calls.Load()

		recorder := send("uqepoorkey", "/v1/chat/completions", `{"model":"uqe-default","messages":[{"role":"user","content":"hi"}]}`)

		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		assert.Equal(t, openAIError{
			Message: i18n.Translate("", i18n.MsgQuotaPreConsumeInsufficient, map[string]any{"Remaining": logger.FormatQuota(100), "Required": logger.FormatQuota(5000)}) + " (request id: " + recorder.Header().Get(common.RequestIdKey) + ")",
			Type:    "new_api_error",
			Code:    "insufficient_user_quota",
		}, errorOf(recorder))
		assert.Equal(t, calls, broke.calls.Load(), "the request never reached the upstream")
	})

	t.Run("a rate limit worded as quota goes through", func(t *testing.T) {
		recorder := chat("uqe-rate-limited")

		require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
		assert.Equal(t, geminiQuotaMessage, errorOf(recorder).Message)
	})
}

// freeKeyUpstream plays Google for the free key relay test: it counts the
// calls of each kind per key and answers them as the test says.
type freeKeyUpstream struct {
	mu     sync.Mutex
	calls  map[string]int // "<key> <action>"
	answer func(key, action string) (int, string)
}

func (u *freeKeyUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	key := r.Header.Get("x-goog-api-key")
	_, action, _ := strings.Cut(r.URL.Path, ":")
	u.mu.Lock()
	u.calls[key+" "+action]++
	answer := u.answer
	u.mu.Unlock()
	status, body := answer(key, action)
	contentType := "application/json; charset=UTF-8"
	if strings.HasPrefix(body, "data: ") {
		contentType = "text/event-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (u *freeKeyUpstream) respond(answer func(key, action string) (int, string)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.answer = answer
}

// count is how many calls of action reached keys (any key when none).
func (u *freeKeyUpstream) count(action string, keys ...string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for call, calls := range u.calls {
		key, callAction, _ := strings.Cut(call, " ")
		if callAction == action && (len(keys) == 0 || slices.Contains(keys, key)) {
			n += calls
		}
	}
	return n
}

// googleAnswers answers countTokens with tokens and every other call as the
// model did.
func googleAnswers(tokens int, status int, body string) func(key, action string) (int, string) {
	return func(_, action string) (int, string) {
		switch action {
		case "countTokens":
			return http.StatusOK, fmt.Sprintf(`{"totalTokens":%d}`, tokens)
		case "batchEmbedContents":
			if status == http.StatusOK {
				return http.StatusOK, `{"embeddings":[{"values":[0.5]}]}`
			}
		case "streamGenerateContent":
			if status == http.StatusOK {
				return http.StatusOK, "data: " + body + "\n\n"
			}
		}
		return status, body
	}
}

type freeKeyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *freeKeyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *freeKeyClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// A Gemini channel in free key mode in front of a paid channel, with one
// retry: OpenAI, Gemini, Claude and embedding requests go to a free key with
// room; a request larger than the model's tokens per minute, one every key
// refused with Google's free tier 429, and, once every key paused for the
// day, every later one, go to the paid channel. Users see Google's plain rate
// limit, exactly as Google's own passes through, in the response and their
// error log, while administrators see what happened; no free key is ever
// disabled for a rate limit.
func TestGeminiFreeKeyChannelFallsThroughToTheNextChannel(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}))
	require.NoError(t, i18n.Init())
	logConsume, batchUpdate := common.LogConsumeEnabled, common.BatchUpdateEnabled
	autoDisable, errorLog, retryTimes := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes
	disableCodes, disableKeywords := operation_setting.AutomaticDisableStatusCodesToString(), operation_setting.AutomaticDisableKeywordsToString()
	streamingTimeout := constant.StreamingTimeout
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	constant.StreamingTimeout = 30
	clock := &freeKeyClock{now: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)}
	previousLimiter := geminifreekey.SetDefault(geminifreekey.New(clock.Now))
	t.Cleanup(func() {
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		geminifreekey.SetDefault(previousLimiter)
		constant.StreamingTimeout = streamingTimeout
		common.LogConsumeEnabled, common.BatchUpdateEnabled = logConsume, batchUpdate
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = autoDisable, errorLog, retryTimes
		require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(disableCodes))
		operation_setting.AutomaticDisableKeywordsFromString(disableKeywords)
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = true, true, 1
	// Google's free tier 429 says "You exceeded your current quota", one of
	// the default keywords, and 429 is a disable status here as well.
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401,429"))
	operation_setting.AutomaticDisableKeywordsFromString("You exceeded your current quota\nPermission denied")
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"gemini-2.5-flash":0.01,"gemini-embedding-001":0.01,"gemini-3.6-flash":0.01,"gemini-2.5-pro":0.01,"gemini-genuine":0.01,"gemini-reveal":0.01,"gemini-reveal-genuine":0.01,"gemini-off":0.01,"gemini-tiers":0.01}`))

	const generation = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`
	const freeTierTPM = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 250000, model: gemini-3.6-flash, description: Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 49.477199395s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more about Gemini API quotas","url":"https://aistudio.google.com/docs/rate-limits"}]},{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"description":"Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.","quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_input_token_count","quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaDimensions":{"location":"global","model":"gemini-3.6-flash"},"quotaValue":"250000"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"49s"}]}}`
	// freeTierRPD is Google's per-day 429 answered at now, whose delay runs
	// to the next midnight in Los Angeles, a little short as Google rounds it.
	freeTierRPD := func(now time.Time) string {
		delay := geminifreekey.NextReset(now).Sub(now) - 718*time.Millisecond
		return fmt.Sprintf(`{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 20, model: gemini-2.5-flash\nPlease retry in %s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"location":"global","model":"gemini-2.5-flash"},"quotaValue":"20"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"%ds"}]}}`, delay, int(delay.Seconds()))
	}
	const plainRateLimit = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`

	free := &freeKeyUpstream{calls: map[string]int{}}
	free.respond(googleAnswers(100, http.StatusOK, generation))
	paid := &freeKeyUpstream{calls: map[string]int{}}
	paid.respond(googleAnswers(100, http.StatusOK, generation))
	genuine := &freeKeyUpstream{calls: map[string]int{}}
	genuine.respond(googleAnswers(100, http.StatusTooManyRequests, plainRateLimit))
	revealGenuine := &freeKeyUpstream{calls: map[string]int{}}
	revealGenuine.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierTPM))
	servers := map[*freeKeyUpstream]*httptest.Server{}
	for _, upstream := range []*freeKeyUpstream{free, paid, genuine, revealGenuine} {
		servers[upstream] = httptest.NewServer(upstream)
		t.Cleanup(servers[upstream].Close)
	}

	user := model.User{Id: 6600000 + int(relayRouterTestUsers.Add(1)), Username: "gfk-user", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "gfktestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	freeKeys := []string{"AIzaSyRouterFree0-abcdefghijklmnopqrstu", "AIzaSyRouterFree1-abcdefghijklmnopqrstu", "AIzaSyRouterFree2-abcdefghijklmnopqrstu"}
	// Free channels may disable keys automatically; the others may not, so
	// that they keep answering.
	create := func(name string, upstream *freeKeyUpstream, keys []string, models, settings string, priority int64) model.Channel {
		baseURL := servers[upstream].URL
		autoBan := 0
		if upstream == free {
			autoBan = 1
		}
		channel := model.Channel{Name: name, Key: strings.Join(keys, "\n"), Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeGemini, Group: "default", Models: models, BaseURL: &baseURL, OtherSettings: settings, Priority: &priority, AutoBan: &autoBan}
		if len(keys) > 1 {
			channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: len(keys), MultiKeyMode: constant.MultiKeyModePolling}
		}
		require.NoError(t, model.DB.Create(&channel).Error)
		for _, name := range channel.GetModels() {
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true, Priority: &priority}).Error)
		}
		return channel
	}
	freeChannel := create("gfk-free", free, freeKeys, "gemini-2.5-flash,gemini-embedding-001,gemini-3.6-flash,gemini-2.5-pro", `{"gemini_free_key_enabled":true}`, 10)
	paidChannel := create("gfk-paid", paid, []string{"AIzaSyRouterPaid-abcdefghijklmnopqrstuv"}, "gemini-2.5-flash,gemini-embedding-001,gemini-2.5-pro", "", 0)
	create("gfk-genuine", genuine, []string{"AIzaSyRouterGenuine-abcdefghijklmnopqr"}, "gemini-genuine", "", 0)
	revealing := create("gfk-reveal", free, freeKeys, "gemini-reveal", `{"gemini_free_key_enabled":true,"gemini_free_key_reveal_tier_errors":true,"gemini_free_key_model_limits":[{"model":"gemini-reveal","rpm":-1,"tpm":-1,"rpd":-1}]}`, 0)
	create("gfk-reveal-genuine", revealGenuine, []string{"AIzaSyRouterRevealGenuine-abcdefghijkl"}, "gemini-reveal-genuine", "", 0)
	// A multi-key channel with the mode saved off behaves as before it existed.
	offKeys := []string{"AIzaSyRouterOff0-abcdefghijklmnopqrstuv", "AIzaSyRouterOff1-abcdefghijklmnopqrstuv"}
	off := create("gfk-off", revealGenuine, offKeys, "gemini-off", `{"gemini_free_key_reveal_tier_errors":true,"gemini_free_key_max_attempts":3}`, 0)
	require.NoError(t, model.DB.Model(&off).Update("auto_ban", 1).Error)
	// Three priority tiers: free keys, then a paid channel, then one that
	// always answers Google's rate limit.
	create("gfk-tiers-free", free, []string{"AIzaSyRouterTiers0-abcdefghijklmnopqrst", "AIzaSyRouterTiers1-abcdefghijklmnopqrst"}, "gemini-tiers", `{"gemini_free_key_enabled":true}`, 10)
	create("gfk-tiers-paid", paid, []string{"AIzaSyRouterTiersPaid-abcdefghijklmnop"}, "gemini-tiers", "", 5)
	create("gfk-tiers-last", genuine, []string{"AIzaSyRouterTiersLast-abcdefghijklmnop"}, "gemini-tiers", "", 0)

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetRelayRouter(engine)
	send := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer gfktestkey")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	requests := map[string]func(modelName string) *httptest.ResponseRecorder{
		"OpenAI chat": func(modelName string) *httptest.ResponseRecorder {
			return send("/v1/chat/completions", `{"model":"`+modelName+`","messages":[{"role":"user","content":"hi"}]}`)
		},
		"Gemini generateContent": func(modelName string) *httptest.ResponseRecorder {
			return send("/v1beta/models/"+modelName+":generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		},
		"Gemini streamGenerateContent": func(modelName string) *httptest.ResponseRecorder {
			return send("/v1beta/models/"+modelName+":streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		},
		"Claude messages": func(modelName string) *httptest.ResponseRecorder {
			return send("/v1/messages", `{"model":"`+modelName+`","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		},
	}
	routes := slices.Sorted(maps.Keys(requests))
	userView := func(t *testing.T) string {
		t.Helper()
		logs, _, err := model.GetUserLogs(user.Id, model.LogFilter{}, 0, 1000)
		require.NoError(t, err)
		view, err := common.Marshal(logs)
		require.NoError(t, err)
		return string(view)
	}
	errorLogsOf := func(t *testing.T, requestID string) []model.Log {
		t.Helper()
		var logs []model.Log
		require.NoError(t, model.LOG_DB.Where("type = ? AND request_id = ?", model.LogTypeError, requestID).Order("id").Find(&logs).Error)
		return logs
	}
	adminInfoOf := func(t *testing.T, log model.Log) map[string]any {
		t.Helper()
		other, err := common.StrToMap(log.Other)
		require.NoError(t, err)
		adminInfo, ok := other["admin_info"].(map[string]any)
		require.True(t, ok)
		return adminInfo
	}
	// assertNothingTold checks that text tells nothing of the free tier or
	// the channel's keys.
	assertNothingTold := func(t *testing.T, text string) {
		t.Helper()
		for _, told := range append([]string{"free_tier", "FreeTier", "Free Tier", "free tier", "billing details", "generativelanguage", "quotaId", "250000", "aistudio", "ai.dev", "ai.google.dev", "retry in", "49s", "Free key mode", "免费", "免費", "gemini_free_key", "key_index", "…"}, freeKeys...) {
			assert.NotContains(t, text, told)
		}
	}
	keysUntouched := func(t *testing.T) {
		t.Helper()
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		channel, err := model.GetChannelById(freeChannel.Id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
		assert.Empty(t, channel.ChannelInfo.MultiKeyStatusList, "no free key is disabled")
	}

	t.Run("every format is served by a free key", func(t *testing.T) {
		for _, route := range routes {
			clock.Add(time.Minute)
			recorder := requests[route]("gemini-2.5-flash")
			require.Equal(t, http.StatusOK, recorder.Code, route+": "+recorder.Body.String())
		}
		recorder := send("/v1/embeddings", `{"model":"gemini-embedding-001","input":"hi"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, len(routes)+1, free.count("countTokens"))
		assert.Equal(t, len(routes), free.count("generateContent")+free.count("streamGenerateContent"))
		assert.Equal(t, 1, free.count("batchEmbedContents"))
		assert.Zero(t, paid.count("generateContent")+paid.count("streamGenerateContent")+paid.count("batchEmbedContents"))

		var consumed model.Log
		require.NoError(t, model.LOG_DB.Where("type = ? AND channel_id = ?", model.LogTypeConsume, freeChannel.Id).Order("id desc").First(&consumed).Error)
		audit, ok := adminInfoOf(t, consumed)["gemini_free_key"].(map[string]any)
		require.True(t, ok, "administrators see the key and the counted tokens")
		assert.Equal(t, "gemini-embedding-001", audit["bucket"])
		assert.Equal(t, float64(100), audit["tokens"])
		assert.Equal(t, "count_tokens", audit["token_source"])
		assertNothingTold(t, userView(t))
	})

	t.Run("a request over the model's tokens per minute goes to the next channel", func(t *testing.T) {
		clock.Add(time.Minute)
		free.respond(googleAnswers(250001, http.StatusOK, generation))
		generated := free.count("generateContent")
		recorder := requests["OpenAI chat"]("gemini-2.5-flash")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, generated, free.count("generateContent"), "no free key got it")
		assert.Equal(t, 1, paid.count("generateContent"))
		logs := errorLogsOf(t, recorder.Header().Get(common.RequestIdKey))
		require.Len(t, logs, 1)
		assert.Equal(t, "status_code=429, Resource has been exhausted (e.g. check quota).", logs[0].Content)
		adminInfo := adminInfoOf(t, logs[0])
		assert.Contains(t, adminInfo["upstream_error"], "250001")
		assert.Equal(t, "gemini_free_key_busy", adminInfo["upstream_error_code"])
		assert.Equal(t, "too_large", adminInfo["gemini_free_key"].(map[string]any)["rejected"])
		var consumed model.Log
		require.NoError(t, model.LOG_DB.Where("type = ? AND request_id = ?", model.LogTypeConsume, recorder.Header().Get(common.RequestIdKey)).First(&consumed).Error)
		assert.Equal(t, paidChannel.Id, consumed.ChannelId)
		assert.NotContains(t, adminInfoOf(t, consumed), "gemini_free_key", "the paid channel's log has nothing of the free one")
		assertNothingTold(t, userView(t))
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	t.Run("a request every key refused goes to the next channel and disables nothing", func(t *testing.T) {
		clock.Add(time.Minute)
		free.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierTPM))
		before := free.count("generateContent", freeKeys...)
		recorder := requests["Gemini generateContent"]("gemini-2.5-flash")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, before+3, free.count("generateContent", freeKeys...), "each key once")
		assert.Equal(t, 2, paid.count("generateContent"))
		logs := errorLogsOf(t, recorder.Header().Get(common.RequestIdKey))
		require.Len(t, logs, 1)
		assert.Equal(t, "status_code=429, Resource has been exhausted (e.g. check quota).", logs[0].Content)
		adminInfo := adminInfoOf(t, logs[0])
		assert.Contains(t, adminInfo["upstream_error"], "generate_content_free_tier_input_token_count")
		attempts := adminInfo["gemini_free_key"].(map[string]any)["attempts"].([]any)
		assert.Len(t, attempts, 3)
		keysUntouched(t)
		assertNothingTold(t, userView(t))
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	// The free channel the request exhausted keeps its priority tier, so the
	// retry goes to the next tier, not the one after it.
	t.Run("a request its free channel refused retries on the next priority tier", func(t *testing.T) {
		clock.Add(time.Minute)
		free.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierTPM))
		paidBefore := paid.count("generateContent")
		recorder := requests["OpenAI chat"]("gemini-tiers")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, paidBefore+1, paid.count("generateContent"))
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	t.Run("a model the free tier does not serve goes to the next channel untouched", func(t *testing.T) {
		before := free.count("countTokens") + free.count("generateContent")
		recorder := requests["OpenAI chat"]("gemini-2.5-pro")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, before, free.count("countTokens")+free.count("generateContent"))
		assert.Empty(t, errorLogsOf(t, recorder.Header().Get(common.RequestIdKey)), "selection skipped the free channel")
	})

	// Selection from the database (memory cache off) in a fresh process, which
	// nothing relayed in free key mode yet: the startup check of the stored
	// channels activates the mode, so the very first request for a model the
	// free tier does not serve goes straight to the paid channel.
	t.Run("selection from the database skips the free channel from a fresh process's first request", func(t *testing.T) {
		memoryCache := common.MemoryCacheEnabled
		common.MemoryCacheEnabled = false
		fresh := geminifreekey.New(clock.Now)
		previous := geminifreekey.SetDefault(fresh)
		t.Cleanup(func() {
			geminifreekey.SetDefault(previous)
			common.MemoryCacheEnabled = memoryCache
		})
		model.InitGeminiFreeKeyLimiter()
		asked, paidBefore := free.count("countTokens")+free.count("generateContent"), paid.count("generateContent")
		for i := range 2 {
			recorder := requests["OpenAI chat"]("gemini-2.5-pro")
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Empty(t, errorLogsOf(t, recorder.Header().Get(common.RequestIdKey)), "request %d: selection skipped the free channel", i+1)
			assert.Equal(t, paidBefore+i+1, paid.count("generateContent"))
		}
		assert.Equal(t, asked, free.count("countTokens")+free.count("generateContent"), "nothing reached Google on the free channel")
	})

	// The user gets exactly what a channel without the mode passes on from
	// Google's own plain rate limit, in every format and in their log.
	t.Run("an exhausted free channel answers as Google's plain rate limit", func(t *testing.T) {
		clock.Add(time.Minute)
		free.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierTPM))
		for _, route := range routes {
			t.Run(route, func(t *testing.T) {
				clock.Add(time.Minute)
				disguised := requests[route]("gemini-3.6-flash")
				real := requests[route]("gemini-genuine")
				require.Equal(t, http.StatusTooManyRequests, real.Code)
				assert.Contains(t, real.Body.String(), "Resource has been exhausted (e.g. check quota).")
				require.Equal(t, real.Code, disguised.Code)
				assert.Equal(t, real.Header().Get("Content-Type"), disguised.Header().Get("Content-Type"))
				ours, theirs := disguised.Header().Get(common.RequestIdKey), real.Header().Get(common.RequestIdKey)
				assert.Equal(t, strings.ReplaceAll(real.Body.String(), theirs, "<id>"), strings.ReplaceAll(disguised.Body.String(), ours, "<id>"))
				assertNothingTold(t, disguised.Body.String())
				disguisedLogs, realLogs := errorLogsOf(t, ours), errorLogsOf(t, theirs)
				require.Len(t, disguisedLogs, 2, "the request and its retry")
				require.Len(t, realLogs, 2)
				for i := range realLogs {
					assert.Equal(t, realLogs[i].Content, disguisedLogs[i].Content)
					disguisedOther, err := common.StrToMap(disguisedLogs[i].Other)
					require.NoError(t, err)
					realOther, err := common.StrToMap(realLogs[i].Other)
					require.NoError(t, err)
					for _, field := range []string{"error_type", "error_code", "status_code"} {
						assert.Equal(t, realOther[field], disguisedOther[field], field)
					}
				}
			})
		}
		keysUntouched(t)
		assertNothingTold(t, userView(t))
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	// Without a retry the request ends on Google's 429.
	t.Run("a channel that reveals free tier errors passes Google's on as without the mode", func(t *testing.T) {
		common.RetryTimes = 0
		t.Cleanup(func() { common.RetryTimes = 1 })
		free.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierTPM))
		for _, route := range routes {
			t.Run(route, func(t *testing.T) {
				clock.Add(time.Minute)
				revealed := requests[route]("gemini-reveal")
				real := requests[route]("gemini-reveal-genuine")
				require.Equal(t, http.StatusTooManyRequests, real.Code)
				assert.Contains(t, real.Body.String(), "generate_content_free_tier_input_token_count")
				require.Equal(t, real.Code, revealed.Code)
				ours, theirs := revealed.Header().Get(common.RequestIdKey), real.Header().Get(common.RequestIdKey)
				assert.Equal(t, strings.ReplaceAll(real.Body.String(), theirs, "<id>"), strings.ReplaceAll(revealed.Body.String(), ours, "<id>"))
				// The user's log reads as without the mode too.
				revealedLogs, realLogs := errorLogsOf(t, ours), errorLogsOf(t, theirs)
				require.Len(t, revealedLogs, 1)
				require.Len(t, realLogs, 1)
				assert.Equal(t, realLogs[0].Content, revealedLogs[0].Content)
				revealedOther, err := common.StrToMap(revealedLogs[0].Other)
				require.NoError(t, err)
				realOther, err := common.StrToMap(realLogs[0].Other)
				require.NoError(t, err)
				for _, field := range []string{"error_type", "error_code", "status_code"} {
					assert.Equal(t, realOther[field], revealedOther[field], field)
				}
				assert.Equal(t, "gemini_free_key_busy", adminInfoOf(t, revealedLogs[0])["upstream_error_code"], "the administrators see why the key stays enabled")
			})
		}
		channel, err := model.GetChannelById(revealing.Id, true)
		require.NoError(t, err)
		assert.Empty(t, channel.ChannelInfo.MultiKeyStatusList, "no free key is disabled")
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	// The retry lands on the same channel, the only one for the model, and
	// finds every key Google refused paused: the user still gets Google's
	// 429 twice, as from a channel without the mode whose key answers it
	// twice, while administrators see the channel's own rejection.
	t.Run("a channel that reveals free tier errors passes Google's on after a retry on it", func(t *testing.T) {
		free.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierTPM))
		for _, route := range routes {
			t.Run(route, func(t *testing.T) {
				clock.Add(time.Minute)
				_, action, _ := strings.Cut(route, " ")
				if route == "OpenAI chat" || route == "Claude messages" {
					action = "generateContent"
				}
				asked := free.count(action, freeKeys...)
				revealed := requests[route]("gemini-reveal")
				real := requests[route]("gemini-reveal-genuine")
				require.Equal(t, http.StatusTooManyRequests, real.Code)
				assert.Contains(t, real.Body.String(), "generate_content_free_tier_input_token_count")
				assert.Equal(t, asked+len(freeKeys), free.count(action, freeKeys...), "each key once, none on the retry")
				require.Equal(t, real.Code, revealed.Code)
				assert.Equal(t, real.Header().Get("Content-Type"), revealed.Header().Get("Content-Type"))
				ours, theirs := revealed.Header().Get(common.RequestIdKey), real.Header().Get(common.RequestIdKey)
				assert.Equal(t, strings.ReplaceAll(real.Body.String(), theirs, "<id>"), strings.ReplaceAll(revealed.Body.String(), ours, "<id>"))
				revealedLogs, realLogs := errorLogsOf(t, ours), errorLogsOf(t, theirs)
				require.Len(t, revealedLogs, 2, "the request and its retry")
				require.Len(t, realLogs, 2)
				for i := range realLogs {
					assert.Equal(t, realLogs[i].Content, revealedLogs[i].Content)
					revealedOther, err := common.StrToMap(revealedLogs[i].Other)
					require.NoError(t, err)
					realOther, err := common.StrToMap(realLogs[i].Other)
					require.NoError(t, err)
					for _, field := range []string{"error_type", "error_code", "status_code"} {
						assert.Equal(t, realOther[field], revealedOther[field], field)
					}
				}
				retry := adminInfoOf(t, revealedLogs[1])
				assert.Equal(t, "gemini_free_key_busy", retry["upstream_error_code"])
				assert.Contains(t, retry["upstream_error"], "Free key mode", "administrators see the channel's own rejection")
				assert.Equal(t, "no_key", retry["gemini_free_key"].(map[string]any)["rejected"])
			})
		}
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		channel, err := model.GetChannelById(revealing.Id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
		assert.Empty(t, channel.ChannelInfo.MultiKeyStatusList, "no free key is disabled")
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	// A stream the channel refuses without asking Google gets the headers of
	// a stream Google refused.
	t.Run("a stream refused without Google reads like Google's own rate limit", func(t *testing.T) {
		common.RetryTimes = 0
		t.Cleanup(func() { common.RetryTimes = 1 })
		clock.Add(time.Minute)
		free.respond(googleAnswers(250001, http.StatusOK, generation))
		refused := requests["Gemini streamGenerateContent"]("gemini-3.6-flash")
		real := requests["Gemini streamGenerateContent"]("gemini-genuine")
		require.Equal(t, http.StatusTooManyRequests, real.Code)
		require.Equal(t, real.Code, refused.Code)
		headers := func(recorder *httptest.ResponseRecorder) http.Header {
			header := recorder.Header().Clone()
			header.Del(common.RequestIdKey)
			return header
		}
		assert.Equal(t, headers(real), headers(refused))
		assert.Equal(t, "no-cache", refused.Header().Get("Cache-Control"))
		ours, theirs := refused.Header().Get(common.RequestIdKey), real.Header().Get(common.RequestIdKey)
		assert.Equal(t, strings.ReplaceAll(real.Body.String(), theirs, "<id>"), strings.ReplaceAll(refused.Body.String(), ours, "<id>"))
		free.respond(googleAnswers(100, http.StatusOK, generation))
	})

	t.Run("with the mode off Google's free tier 429 is relayed and disables the key as before", func(t *testing.T) {
		common.RetryTimes = 0
		t.Cleanup(func() { common.RetryTimes = 1 })
		countsBefore := revealGenuine.count("countTokens")
		recorder := requests["OpenAI chat"]("gemini-off")
		require.Equal(t, http.StatusTooManyRequests, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "generate_content_free_tier_input_token_count")
		assert.Equal(t, countsBefore, revealGenuine.count("countTokens"), "no token count")
		assert.Equal(t, 1, revealGenuine.count("generateContent", offKeys...), "one key, once")
		assert.Eventually(t, func() bool {
			channel, err := model.GetChannelById(off.Id, true)
			return err == nil && len(channel.ChannelInfo.MultiKeyStatusList) == 1
		}, 5*time.Second, 10*time.Millisecond)
		logs := errorLogsOf(t, recorder.Header().Get(common.RequestIdKey))
		require.Len(t, logs, 1)
		assert.Contains(t, logs[0].Content, "generate_content_free_tier_input_token_count")
	})

	t.Run("once every key paused for the day the free channel is skipped", func(t *testing.T) {
		clock.Add(time.Minute)
		free.respond(googleAnswers(100, http.StatusTooManyRequests, freeTierRPD(clock.Now())))
		recorder := requests["OpenAI chat"]("gemini-2.5-flash")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		free.respond(googleAnswers(100, http.StatusOK, generation))
		common.RetryTimes = 0
		t.Cleanup(func() { common.RetryTimes = 1 })
		before, paidBefore := free.count("countTokens")+free.count("generateContent"), paid.count("generateContent")
		recorder = requests["OpenAI chat"]("gemini-2.5-flash")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, before, free.count("countTokens")+free.count("generateContent"))
		assert.Equal(t, paidBefore+1, paid.count("generateContent"))
		assert.Empty(t, errorLogsOf(t, recorder.Header().Get(common.RequestIdKey)))
		keysUntouched(t)

		// The usage panel shows each key's day used up, the key itself never.
		usageEngine := gin.New()
		usageEngine.GET("/usage/:id", controller.GetGeminiFreeKeyUsage)
		usageRecorder := httptest.NewRecorder()
		usageEngine.ServeHTTP(usageRecorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/usage/%d", freeChannel.Id), nil))
		require.Equal(t, http.StatusOK, usageRecorder.Code)
		for _, key := range freeKeys {
			assert.NotContains(t, usageRecorder.Body.String(), key)
		}
		var usage struct {
			Success bool `json:"success"`
			Data    struct {
				Enabled     bool   `json:"enabled"`
				Timezone    string `json:"timezone"`
				NextResetAt int64  `json:"next_reset_at"`
				Keys        []struct {
					Index   int    `json:"index"`
					KeyHint string `json:"key_hint"`
					Enabled bool   `json:"enabled"`
					Models  []struct {
						Model          string `json:"model"`
						RPDUsed        int    `json:"rpd_used"`
						RPDLimit       int    `json:"rpd_limit"`
						CooldownUntil  int64  `json:"cooldown_until"`
						CooldownReason string `json:"cooldown_reason"`
					} `json:"models"`
				} `json:"keys"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(usageRecorder.Body.Bytes(), &usage))
		require.True(t, usage.Success)
		assert.True(t, usage.Data.Enabled)
		assert.Equal(t, "America/Los_Angeles", usage.Data.Timezone)
		assert.Equal(t, time.Date(2026, 11, 17, 0, 0, 0, 0, time.FixedZone("PST", -8*60*60)).Unix(), usage.Data.NextResetAt)
		require.Len(t, usage.Data.Keys, 3)
		for i, key := range usage.Data.Keys {
			assert.Equal(t, i, key.Index)
			assert.Equal(t, "…"+freeKeys[i][len(freeKeys[i])-4:], key.KeyHint)
			assert.True(t, key.Enabled)
			var flash bool
			for _, used := range key.Models {
				if used.Model == "gemini-2.5-flash" {
					flash = true
					assert.Equal(t, 20, used.RPDUsed)
					assert.Equal(t, 20, used.RPDLimit)
					assert.Equal(t, "day", used.CooldownReason)
					assert.Equal(t, usage.Data.NextResetAt, used.CooldownUntil)
				}
			}
			assert.True(t, flash)
		}

		// At midnight in Los Angeles the free channel serves again.
		clock.Add(24 * time.Hour)
		before = free.count("generateContent")
		recorder = requests["OpenAI chat"]("gemini-2.5-flash")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, before+1, free.count("generateContent"))
	})

	// The usage panel says whether the mode is saved on, which keys are
	// enabled (a single key with its channel) and lists the buckets the
	// channel's models count in after its model mapping.
	t.Run("the usage panel shows the mode, the enabled keys and the mapped models", func(t *testing.T) {
		usageEngine := gin.New()
		usageEngine.GET("/usage/:id", controller.GetGeminiFreeKeyUsage)
		type keyUsage struct {
			Index   int  `json:"index"`
			Enabled bool `json:"enabled"`
			Models  []struct {
				Model string `json:"model"`
			} `json:"models"`
		}
		usageOf := func(t *testing.T, channel model.Channel) (bool, []keyUsage) {
			t.Helper()
			require.NoError(t, model.DB.Create(&channel).Error)
			recorder := httptest.NewRecorder()
			usageEngine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/usage/%d", channel.Id), nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			var usage struct {
				Success bool `json:"success"`
				Data    struct {
					Enabled bool       `json:"enabled"`
					Keys    []keyUsage `json:"keys"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &usage))
			require.True(t, usage.Success)
			return usage.Data.Enabled, usage.Data.Keys
		}
		modelsOf := func(key keyUsage) []string {
			var names []string
			for _, used := range key.Models {
				names = append(names, used.Model)
			}
			return names
		}
		mapping := `{"gfk-usage-alias":"gemini-2.5-flash"}`
		enabled, keys := usageOf(t, model.Channel{Name: "gfk-usage-off", Type: constant.ChannelTypeGemini, Status: common.ChannelStatusEnabled, Group: "default",
			Key: "AIzaSyUsageOff0-abcdefghij\nAIzaSyUsageOff1-abcdefghij\nAIzaSyUsageOff2-abcdefghij", Models: "gfk-usage-alias", ModelMapping: &mapping, OtherSettings: `{"gemini_free_key_enabled":false}`,
			ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}}})
		assert.False(t, enabled, "the mode is not saved on")
		require.Len(t, keys, 3)
		assert.Equal(t, []bool{true, false, true}, []bool{keys[0].Enabled, keys[1].Enabled, keys[2].Enabled})
		assert.Equal(t, []string{"gemini-2.5-flash"}, modelsOf(keys[0]), "the model counts in the bucket it is mapped to")

		enabled, keys = usageOf(t, model.Channel{Name: "gfk-usage-single", Type: constant.ChannelTypeGemini, Status: common.ChannelStatusManuallyDisabled, Group: "default",
			Key: "AIzaSyUsageSingle-abcdefghij", Models: "gemini-2.5-flash", OtherSettings: `{"gemini_free_key_enabled":true}`})
		assert.True(t, enabled)
		require.Len(t, keys, 1)
		assert.False(t, keys[0].Enabled, "a single key is off with its channel")
	})
}

// moderationUpstream plays OpenAI for the content moderation test. It records
// every request and answers moderation calls by the key: the sk-mod-* keys
// fail as their names say, and any other key flags sexual content when an
// input carries MOD-FLAG-SEXUAL or an image URL contains flag-image, and
// violence for MOD-FLAG-VIOLENCE. Chat answers 500 for the key sk-chat-500.
// Channel records the X-Channel header a channel's header override sets.
type moderationUpstream struct {
	mu       sync.Mutex
	requests []moderationUpstreamRequest
}

type moderationUpstreamRequest struct {
	Path, Authorization, Organization, Channel, Body string
}

func (u *moderationUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.requests = append(u.requests, moderationUpstreamRequest{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Organization: r.Header.Get("OpenAI-Organization"), Channel: r.Header.Get("X-Channel"), Body: string(body)})
	u.mu.Unlock()
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	reply := func(status int, contentType, answer string) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}
	const jsonType = "application/json"
	switch r.URL.Path {
	case "/v1/moderations":
		var call struct {
			Input any `json:"input"`
		}
		_ = common.Unmarshal(body, &call)
		inputs, ok := call.Input.([]any)
		if !ok {
			inputs = []any{call.Input}
		}
		image := false
		results := make([]string, 0, len(inputs))
		for _, input := range inputs {
			text, url := "", ""
			switch value := input.(type) {
			case string:
				text = value
			case map[string]any:
				image = true
				if imageURL, ok := value["image_url"].(map[string]any); ok {
					url, _ = imageURL["url"].(string)
				}
			}
			sexual := strings.Contains(text, "MOD-FLAG-SEXUAL") || strings.Contains(url, "flag-image")
			violence := strings.Contains(text, "MOD-FLAG-VIOLENCE")
			inputType := "text"
			if image {
				inputType = "image"
			}
			score := func(flagged bool, high float64) float64 {
				if flagged {
					return high
				}
				return 0.0001
			}
			results = append(results, fmt.Sprintf(`{"flagged":%t,"categories":{"sexual":%t,"violence":%t},"category_scores":{"sexual":%v,"violence":%v},"category_applied_input_types":{"sexual":["%s"],"violence":["%s"]}}`,
				sexual || violence, sexual, violence, score(sexual, 0.91), score(violence, 0.88), inputType, inputType))
		}
		switch {
		case key == "sk-mod-401":
			reply(http.StatusUnauthorized, jsonType, `{"error":{"message":"Incorrect API key provided: sk-mod-401","type":"invalid_request_error","code":"invalid_api_key"}}`)
		case key == "sk-mod-403":
			reply(http.StatusForbidden, jsonType, `{"error":{"message":"You are not allowed to use this model","type":"invalid_request_error","code":"model_not_found"}}`)
		case key == "sk-mod-404":
			reply(http.StatusNotFound, "text/html", "<html><body>Not Found</body></html>")
		case key == "sk-mod-429":
			reply(http.StatusTooManyRequests, jsonType, `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`)
		case key == "sk-mod-quota":
			reply(http.StatusTooManyRequests, jsonType, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)
		case key == "sk-mod-500":
			reply(http.StatusInternalServerError, jsonType, `{"error":{"message":"The server had an error","type":"server_error"}}`)
		case key == "sk-mod-slow":
			<-r.Context().Done()
		case key == "sk-mod-html":
			reply(http.StatusOK, "text/html", "<html><body>gateway</body></html>")
		case key == "sk-mod-empty":
			reply(http.StatusOK, jsonType, `{"id":"modr-empty","model":"omni-moderation-2024-09-26","results":[]}`)
		case key == "sk-mod-429size":
			reply(http.StatusTooManyRequests, jsonType, `{"error":{"message":"Request too large for omni-moderation-latest on tokens per min (TPM): Limit 10000, Requested 12000.","type":"tokens","code":"rate_limit_exceeded"}}`)
		case key == "sk-mod-image400" && image:
			reply(http.StatusBadRequest, jsonType, `{"error":{"message":"The image could not be downloaded","type":"invalid_request_error","code":"invalid_image_url"}}`)
		default:
			reply(http.StatusOK, jsonType, `{"id":"modr-test","model":"omni-moderation-2024-09-26","results":[`+strings.Join(results, ",")+`]}`)
		}
	case "/v1/chat/completions":
		if key == "sk-chat-500" {
			reply(http.StatusInternalServerError, jsonType, `{"error":{"message":"upstream broke","type":"server_error"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			reply(http.StatusOK, "text/event-stream", "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"forwarded\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		reply(http.StatusOK, jsonType, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"forwarded"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	case "/v1/completions":
		reply(http.StatusOK, jsonType, `{"id":"cmpl-1","object":"text_completion","created":1,"model":"m","choices":[{"index":0,"text":"forwarded","finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	case "/v1/responses", "/v1/responses/compact":
		reply(http.StatusOK, jsonType, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"forwarded","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	case "/v1/images/generations", "/v1/images/edits":
		reply(http.StatusOK, jsonType, `{"created":1,"data":[{"url":"https://img.example/out.png"}]}`)
	case "/v1/videos":
		reply(http.StatusOK, jsonType, `{"id":"video_mod_1","object":"video","status":"queued","model":"sora-2","created_at":1}`)
	case "/v1/audio/speech":
		reply(http.StatusOK, "audio/mpeg", "ID3")
	case "/v1/embeddings":
		reply(http.StatusOK, jsonType, `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	default:
		reply(http.StatusNotFound, jsonType, `{"error":{"message":"not found"}}`)
	}
}

func (u *moderationUpstream) mark() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

// since returns the requests to path (every path when empty) after mark.
func (u *moderationUpstream) since(mark int, path string) []moderationUpstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var requests []moderationUpstreamRequest
	for _, request := range u.requests[mark:] {
		if path == "" || request.Path == path {
			requests = append(requests, request)
		}
	}
	return requests
}

// An OpenAI channel with content moderation on checks every attempt before
// anything reaches its upstream: rules first, then OpenAI's moderation model
// with the attempt's own key. A rejection is answered with a generic message,
// never retried on another channel, never billed and never a reason to
// disable the channel; it is recorded as one log row of type 100 whose
// details only administrators see. Other channels and channel types behave
// as before, and the gateway safety identifier is independent of it.
func TestRelayModeratesOpenAIChannels(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.Task{}))
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	logConsume, batchUpdate := common.LogConsumeEnabled, common.BatchUpdateEnabled
	autoDisable, errorLog, retryTimes := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes
	disableCodes, disableKeywords := operation_setting.AutomaticDisableStatusCodesToString(), operation_setting.AutomaticDisableKeywordsToString()
	savedPrices := ratio_setting.ModelPrice2JSONString()
	globalSettings := model_setting.GetGlobalSettings()
	passThrough, responsesPolicy := globalSettings.PassThroughRequestEnabled, globalSettings.ChatCompletionsToResponsesPolicy
	savedOptions := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedOptions[key] = value
		return nil
	}))
	t.Cleanup(func() {
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		common.LogConsumeEnabled, common.BatchUpdateEnabled = logConsume, batchUpdate
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = autoDisable, errorLog, retryTimes
		require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(disableCodes))
		operation_setting.AutomaticDisableKeywordsFromString(disableKeywords)
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		globalSettings.PassThroughRequestEnabled, globalSettings.ChatCompletionsToResponsesPolicy = passThrough, responsesPolicy
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedOptions))
	})
	common.LogConsumeEnabled, common.BatchUpdateEnabled = true, false
	// Every status a moderation failure or rejection carries would disable
	// a channel if it reached the channel error path, which only a key-level
	// moderation failure does.
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled, common.RetryTimes = true, true, 2
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("400,401,403,404,429,503"))
	operation_setting.AutomaticDisableKeywordsFromString("")
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))

	upstream := &moderationUpstream{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	// The leak guard: an unmoderated channel of the same model and group with
	// a lower priority, which a retry would reach.
	leak := &moderationUpstream{}
	leakServer := httptest.NewServer(leak)
	t.Cleanup(leakServer.Close)

	user := model.User{Id: 6600000 + int(relayRouterTestUsers.Add(1)), Username: "mod-user", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "modtestkey", Name: "mod-token", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	prices := map[string]float64{}
	type channelOption func(*model.Channel)
	create := func(modelName, key, settings string, options ...channelOption) model.Channel {
		priority, autoBan := int64(10), 1
		channel := model.Channel{Name: "mod-" + modelName, Key: key, Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: modelName, BaseURL: common.GetPointer(server.URL), OtherSettings: settings, Priority: &priority, AutoBan: &autoBan}
		for _, option := range options {
			option(&channel)
		}
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: modelName, Group: "default", Enabled: true, Priority: channel.Priority}).Error)
		prices[modelName] = 0.01
		return channel
	}
	lowPriority := func(channel *model.Channel) { channel.Priority = common.GetPointer(int64(0)) }
	noAutoBan := func(channel *model.Channel) { channel.AutoBan = common.GetPointer(0) }
	withSetting := func(setting string) channelOption {
		return func(channel *model.Channel) { channel.Setting = common.GetPointer(setting) }
	}
	guarded := func(modelName string) {
		create(modelName, "sk-leak", "", lowPriority, func(channel *model.Channel) {
			channel.Name, channel.BaseURL = "mod-leak-"+modelName, common.GetPointer(leakServer.URL)
		})
	}
	const on = `{"moderation_enabled":true}`
	create("mod-off", "sk-ok-off", "")
	create("mod-clean", "sk-ok-clean", on)
	flagChannel := create("mod-flag", "sk-ok-flag", on)
	guarded("mod-flag")
	create("mod-categories", "sk-ok-categories", `{"moderation_enabled":true,"moderation_categories":["sexual"]}`)
	create("mod-rules", "sk-ok-rules", `{"moderation_enabled":true,"moderation_rule_presets":["en-reveal-cot"],"moderation_rules":[{"type":"keyword","pattern":"Forbidden-Word-X"},{"name":"plans","type":"regex","pattern":"secret\\s+plan\\d+"}]}`)
	guarded("mod-rules")
	failures := []struct{ suffix, class string }{
		{"404", "http_404"}, {"429", "http_429_rate"},
		{"500", "http_5xx"}, {"html", "decode"}, {"empty", "decode"}, {"429size", "http_429_size"}, {"slow", "timeout"}, {"image400", "image_rejected"},
	}
	failureChannels := map[string]model.Channel{}
	for _, failure := range failures {
		settings := on
		if failure.suffix == "slow" {
			settings = `{"moderation_enabled":true,"moderation_timeout_seconds":1}`
		}
		failureChannels[failure.suffix] = create("mod-fail-"+failure.suffix, "sk-mod-"+failure.suffix, settings)
		guarded("mod-fail-" + failure.suffix)
	}
	// A moderation answer that says the key or its OpenAI account is
	// unusable is the channel's error, retried under the normal rules on the
	// next channel, which applies its own moderation settings: each such
	// channel has a lower-priority moderated backup with a working key, and
	// no other channel.
	keyFailures := []struct{ suffix, class string }{{"401", "http_401"}, {"403", "http_403"}, {"quota", "http_429_quota"}}
	keyChannels, keyBackups := map[string]model.Channel{}, map[string]model.Channel{}
	for _, failure := range keyFailures {
		keyChannels[failure.suffix] = create("mod-key-"+failure.suffix, "sk-mod-"+failure.suffix, on)
		keyBackups[failure.suffix] = create("mod-key-"+failure.suffix, "sk-ok-key-backup-"+failure.suffix, on, lowPriority)
	}
	keyAlone := create("mod-key-alone", "sk-mod-401", on)
	// The only channel of both groups of an auto token with cross-group
	// retry, whose retry counter starts again in the second group.
	keyCross := create("mod-key-cross", "sk-mod-401", on, noAutoBan, func(channel *model.Channel) { channel.Group = "default,vip" })
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: keyCross.Id, Model: "mod-key-cross", Group: "vip", Enabled: true, Priority: keyCross.Priority}).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "modautokey", Name: "mod-auto-token", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, Group: "auto", CrossGroupRetry: true}).Error)
	// A key-level failure whose retry reaches a moderated backup whose own
	// upstream then fails.
	keyThenBroken := create("mod-key-then-500", "sk-mod-401", on)
	create("mod-key-then-500", "sk-chat-500", on, noAutoBan, lowPriority)
	keyMulti := create("mod-key-multi", "sk-mod-401\nsk-ok-key-multi", on, func(channel *model.Channel) {
		channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}
	})
	create("mod-fail-open", "sk-mod-500", `{"moderation_enabled":true,"moderation_fail_open":true}`)
	create("mod-multikey", "sk-mk-1\nsk-mk-2", on, func(channel *model.Channel) {
		channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}
	})
	create("mod-retry", "sk-chat-500", on, noAutoBan)
	create("mod-retry", "sk-ok-retry-b", on, lowPriority)
	create("mod-retry-rule", "sk-chat-500", on, noAutoBan)
	retryRuleB := create("mod-retry-rule", "sk-ok-retry-rule-b", `{"moderation_enabled":true,"moderation_rules":[{"type":"keyword","pattern":"retry-rule-hit"}]}`, lowPriority)
	create("mod-org", "sk-chat-500", "", noAutoBan, func(channel *model.Channel) { channel.OpenAIOrganization = common.GetPointer("org-a") })
	create("mod-org", "sk-ok-org-b", on, lowPriority)
	// The first channel's header override sends the key chat fails for, and
	// its param override makes the relay use a runtime header override.
	create("mod-hdr", "sk-hdr-a", "", noAutoBan, func(channel *model.Channel) {
		channel.HeaderOverride = common.GetPointer(`{"Authorization":"Bearer sk-chat-500"}`)
		channel.ParamOverride = common.GetPointer(`{"temperature":0.5}`)
	})
	create("mod-hdr", "sk-ok-hdr-b", on, lowPriority, func(channel *model.Channel) {
		channel.HeaderOverride = common.GetPointer(`{"X-Channel":"B"}`)
	})
	for _, modelName := range []string{"mod-claude", "mod-gemini", "mod-responses", "mod-own", "mod-embed", "mod-image", "mod-speech", "mod-realtime", "mod-novelai", "mod-budget-system", "mod-img", "mod-completions", "mod-cache", "mod-global-pass"} {
		create(modelName, "sk-ok-"+modelName, on)
	}
	for _, modelName := range []string{"mod-responses", "mod-completions", "mod-global-pass"} {
		guarded(modelName)
	}
	create("mod-budget", "sk-ok-budget", `{"moderation_enabled":true,"moderation_max_chars":1000}`)
	create("mod-img-skip", "sk-ok-img-skip", `{"moderation_enabled":true,"moderation_skip_images":true}`)
	create("mod-presets", "sk-ok-presets", `{"moderation_enabled":true,"moderation_rule_presets":["en-reveal-cot"]}`)
	create("mod-pass", "sk-ok-pass", on, withSetting(`{"pass_through_body_enabled":true}`))
	guarded("mod-pass")
	create("mod-pass-img", "sk-ok-pass-img", on, withSetting(`{"pass_through_body_enabled":true}`))
	guarded("mod-pass-img")
	const identifierOn = `{"send_safety_identifier":true}`
	create("mod-sid", "sk-ok-sid", identifierOn)
	sidResponses := create("mod-sid-resp", "sk-ok-sid-resp", identifierOn, func(channel *model.Channel) {
		channel.ParamOverride = common.GetPointer(`{"operations":[{"path":"safety_identifier","mode":"delete"}]}`)
	})
	create("mod-sid-pass", "sk-ok-sid-pass", identifierOn, withSetting(`{"pass_through_body_enabled":true}`))
	create("mod-sid-off", "sk-ok-sid-off", "")
	create("mod-sid-off-pass", "sk-ok-sid-off-pass", "", withSetting(`{"pass_through_body_enabled":true}`))
	for _, channelType := range []int{constant.ChannelTypeAzure, constant.ChannelTypeCustom, constant.ChannelTypeAnthropic, constant.ChannelTypeCodex} {
		create(fmt.Sprintf("mod-type-%d", channelType), "sk-ok-type", `{"moderation_enabled":true,"send_safety_identifier":true}`, func(channel *model.Channel) {
			channel.Type = channelType
			if channelType == constant.ChannelTypeCustom {
				channel.BaseURL = common.GetPointer(server.URL + "/v1/chat/completions")
			}
		})
	}
	soraChannel := create("sora-2", "sk-ok-sora", on)
	create("sora-2-pro", "sk-mod-500", on)
	soraKey := create("sora-2-key", "sk-mod-403", on)
	create("sora-2-key", "sk-ok-sora-key-backup", on, lowPriority)
	create("sora-2-dead", "sk-mod-quota", on)
	guarded("sora-2")
	guarded("sora-2-pro")
	chatToResponses := create("mod-c2r", "sk-ok-c2r", on)
	globalSettings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{
		Enabled: true, ChannelIDs: []int{sidResponses.Id, chatToResponses.Id}, ModelPatterns: []string{"^mod-sid-resp$", "^mod-c2r$"},
	}
	priceJSON, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetRelayRouter(engine)
	SetVideoRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	send := func(path, contentType string, body io.Reader) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, body)
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Authorization", "Bearer modtestkey")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		return send(path, "application/json", strings.NewReader(body))
	}
	chat := func(modelName, messages string) *httptest.ResponseRecorder {
		return post("/v1/chat/completions", `{"model":"`+modelName+`","messages":`+messages+`}`)
	}
	var sequence atomic.Int64
	// unique makes a text no earlier request sent, so no verdict is cached.
	unique := func(text string) string { return fmt.Sprintf("%s (sample %d)", text, sequence.Add(1)) }
	userMessage := func(text string) string { return `[{"role":"user","content":"` + text + `"}]` }
	type apiError struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	errorOf := func(t *testing.T, recorder *httptest.ResponseRecorder) apiError {
		t.Helper()
		var payload struct {
			Error apiError `json:"error"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload), recorder.Body.String())
		return payload.Error
	}
	requestID := func(recorder *httptest.ResponseRecorder) string { return recorder.Header().Get(common.RequestIdKey) }
	rowsOf := func(t *testing.T, id string) []*model.Log {
		t.Helper()
		var rows []*model.Log
		require.NoError(t, model.LOG_DB.Where("request_id = ?", id).Order("id").Find(&rows).Error)
		return rows
	}
	otherOf := func(t *testing.T, row *model.Log) map[string]any {
		t.Helper()
		other, err := common.StrToMap(row.Other)
		require.NoError(t, err)
		return other
	}
	adminOf := func(t *testing.T, other map[string]any) map[string]any {
		t.Helper()
		adminInfo, ok := other["admin_info"].(map[string]any)
		require.True(t, ok, other)
		return adminInfo
	}
	auditOf := func(t *testing.T, other map[string]any) map[string]any {
		t.Helper()
		audit, ok := adminOf(t, other)["moderation"].(map[string]any)
		require.True(t, ok, other)
		return audit
	}
	// rejectedRow is the one row of a rejected request: type 100.
	rejectedRow := func(t *testing.T, recorder *httptest.ResponseRecorder) (*model.Log, map[string]any) {
		t.Helper()
		rows := rowsOf(t, requestID(recorder))
		require.Len(t, rows, 1, "a rejection writes exactly one row")
		assert.Equal(t, model.LogTypeModeration, rows[0].Type)
		return rows[0], otherOf(t, rows[0])
	}
	consumeAudit := func(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var row model.Log
		require.Eventually(t, func() bool {
			return model.LOG_DB.Where("request_id = ? AND type = ?", requestID(recorder), model.LogTypeConsume).First(&row).Error == nil
		}, 5*time.Second, 10*time.Millisecond)
		return auditOf(t, otherOf(t, &row))
	}
	userView := func(t *testing.T, id string) string {
		t.Helper()
		logs, _, err := model.GetUserLogs(user.Id, model.LogFilter{RequestId: id}, 0, 10)
		require.NoError(t, err)
		require.NotEmpty(t, logs)
		view, err := common.Marshal(logs)
		require.NoError(t, err)
		return string(view)
	}
	quota := func(t *testing.T) int {
		t.Helper()
		value, err := model.GetUserQuota(user.Id, true)
		require.NoError(t, err)
		return value
	}
	// assertNoRetry checks the leak guard: no lower-priority channel was
	// asked and the policy events hold no retry.
	assertNoRetry := func(t *testing.T, other map[string]any) {
		t.Helper()
		assert.Empty(t, leak.since(0, ""), "an unmoderated channel received the request")
		events, _ := adminOf(t, other)["request_policy"].([]any)
		require.NotEmpty(t, events)
		for _, event := range events {
			decision, _ := event.(map[string]any)["decision"].(map[string]any)
			assert.NotEqual(t, "retry", decision["action"], events)
		}
		last, _ := events[len(events)-1].(map[string]any)["decision"].(map[string]any)
		assert.Equal(t, map[string]any{"action": "stop", "reason": "local_rejection", "source": "channel"}, last)
	}
	flaggedMessage := func(recorder *httptest.ResponseRecorder) apiError {
		return apiError{Message: "The request was rejected by content moderation (request id: " + requestID(recorder) + ")", Type: "new_api_error", Code: "content_moderation_flagged"}
	}

	t.Run("switched off, nothing is moderated", func(t *testing.T) {
		mark := upstream.mark()
		recorder := chat("mod-off", userMessage(unique("MOD-FLAG-SEXUAL off")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, "/v1/moderations"))
		assert.Len(t, upstream.since(mark, "/v1/chat/completions"), 1)
	})

	t.Run("a clean request is checked with the channel's key and forwarded", func(t *testing.T) {
		mark := upstream.mark()
		system, userText := unique("clean system prompt"), unique("clean user question")
		recorder := chat("mod-clean", `[{"role":"system","content":"`+system+`"},{"role":"user","content":"`+userText+`"},{"role":"assistant","content":"assistant text MOD-FLAG-SEXUAL","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"tool text MOD-FLAG-SEXUAL"}]`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "/v1/moderations")
		require.Len(t, calls, 1)
		assert.Equal(t, "Bearer sk-ok-clean", calls[0].Authorization)
		assert.Contains(t, calls[0].Body, system)
		assert.Contains(t, calls[0].Body, userText)
		assert.Contains(t, calls[0].Body, `"model":"omni-moderation-latest"`)
		assert.NotContains(t, calls[0].Body, "MOD-FLAG-SEXUAL", "assistant and tool text are not checked")
		assert.Len(t, upstream.since(mark, "/v1/chat/completions"), 1)
		audit := consumeAudit(t, recorder)
		assert.Equal(t, "passed", audit["status"])
		assert.Greater(t, audit["checked_chars"], float64(0))
		assert.NotContains(t, userView(t, requestID(recorder)), "admin_info")
	})

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("a flagged request is rejected, refunded and recorded (stream %t)", stream), func(t *testing.T) {
			var logs bytes.Buffer
			if !stream {
				common.LogWriterMu.Lock()
				infoWriter, errorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
				gin.DefaultWriter, gin.DefaultErrorWriter = &logs, &logs
				common.LogWriterMu.Unlock()
				t.Cleanup(func() {
					common.LogWriterMu.Lock()
					gin.DefaultWriter, gin.DefaultErrorWriter = infoWriter, errorWriter
					common.LogWriterMu.Unlock()
				})
			}
			before := quota(t)
			mark := upstream.mark()
			flagged := unique("MOD-FLAG-SEXUAL flagged")
			recorder := post("/v1/chat/completions", fmt.Sprintf(`{"model":"mod-flag","stream":%t,"messages":%s}`, stream, userMessage(flagged)))

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Equal(t, flaggedMessage(recorder), errorOf(t, recorder))
			for _, hidden := range []string{"sexual", "MOD-FLAG", server.URL} {
				assert.NotContains(t, recorder.Body.String(), hidden)
			}
			assert.Len(t, upstream.since(mark, "/v1/moderations"), 1)
			assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
			assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 && quota(t) == before }, 5*time.Second, 10*time.Millisecond, "the pre-consumed quota is refunded")

			row, other := rejectedRow(t, recorder)
			assert.Equal(t, flagChannel.Id, row.ChannelId)
			assert.Equal(t, "mod-flag", row.ModelName)
			assert.Equal(t, 0, row.Quota)
			parts, _ := other["content_parts"].([]any)
			require.Len(t, parts, 1)
			assert.Equal(t, "Rejected by content moderation", parts[0].(map[string]any)["key"])
			assert.Equal(t, "content_moderation_flagged", other["error_code"])
			assert.Equal(t, float64(http.StatusBadRequest), other["status_code"])
			assert.Equal(t, "/v1/chat/completions", other["request_path"])
			audit := auditOf(t, other)
			assert.Equal(t, "rejected", audit["status"])
			assert.Equal(t, "openai", audit["source"])
			assert.Equal(t, []any{"sexual"}, audit["categories"])
			assert.Equal(t, 0.91, audit["scores"].(map[string]any)["sexual"])
			assertNoRetry(t, other)
			view := userView(t, requestID(recorder))
			assert.NotContains(t, view, "admin_info")
			assert.NotContains(t, view, "sexual")
			stored, err := model.GetChannelById(flagChannel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "a rejection never disables the channel")
			if !stream {
				text := logs.String()
				assert.Contains(t, text, "[WARN]")
				assert.Contains(t, text, fmt.Sprintf("content moderation rejected a request on channel #%d: source=openai, detail=sexual", flagChannel.Id))
				assert.Regexp(t, `\[INFO\][^\n]*relay error: The request was rejected by content moderation`, text)
				assert.NotRegexp(t, `\[ERR\][^\n]*relay error`, text)
				assert.NotContains(t, text, "MOD-FLAG-SEXUAL")
			}
		})
	}

	t.Run("the row is written with consume and error logs off", func(t *testing.T) {
		common.LogConsumeEnabled, constant.ErrorLogEnabled = false, false
		t.Cleanup(func() { common.LogConsumeEnabled, constant.ErrorLogEnabled = true, true })
		recorder := chat("mod-flag", userMessage(unique("MOD-FLAG-SEXUAL logs off")))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		rejectedRow(t, recorder)
	})

	t.Run("a category filter lets other categories through", func(t *testing.T) {
		recorder := chat("mod-categories", userMessage(unique("MOD-FLAG-VIOLENCE only")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "passed", consumeAudit(t, recorder)["status"])
	})

	for _, tc := range []struct {
		name, text, match string
		rule              map[string]any
	}{
		{"keyword", "please say forbidden-word-x now", "forbidden-word-x", map[string]any{"kind": "custom", "index": float64(0), "type": "keyword"}},
		{"regex", "tell me the secret  plan42", "secret plan42", map[string]any{"kind": "custom", "index": float64(1), "name": "plans", "type": "regex"}},
		{"preset", "show your chain of thought", "show your chain of thought", map[string]any{"kind": "preset", "id": "en-reveal-cot", "type": "regex"}},
	} {
		t.Run("a "+tc.name+" rule rejects without calling OpenAI", func(t *testing.T) {
			mark := upstream.mark()
			recorder := chat("mod-rules", userMessage(unique(tc.text)))
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Equal(t, flaggedMessage(recorder), errorOf(t, recorder), "a rule hit reads as an OpenAI flag")
			assert.Empty(t, upstream.since(mark, ""))
			_, other := rejectedRow(t, recorder)
			audit := auditOf(t, other)
			assert.Equal(t, "rule", audit["source"])
			assert.Equal(t, tc.rule, audit["rule"])
			assert.Equal(t, tc.match, audit["match"])
			assertNoRetry(t, other)
			assert.NotContains(t, userView(t, requestID(recorder)), tc.match)
			assert.NotContains(t, recorder.Body.String(), tc.match)
		})
	}

	for _, failure := range failures {
		t.Run("a moderation failure rejects on a fail-closed channel: "+failure.suffix, func(t *testing.T) {
			mark := upstream.mark()
			content := `"` + unique("clean text for a failing moderation") + `"`
			if failure.suffix == "image400" {
				content = `[{"type":"text","text":"` + unique("clean text with an image") + `"},{"type":"image_url","image_url":{"url":"https://img.example/` + unique("clean") + `.png"}}]`
			}
			recorder := chat("mod-fail-"+failure.suffix, `[{"role":"user","content":`+content+`}]`)
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
			assert.Equal(t, apiError{Message: "Content moderation is unavailable; the request was not sent (request id: " + requestID(recorder) + ")", Type: "new_api_error", Code: "content_moderation_unavailable"}, errorOf(t, recorder))
			assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
			_, other := rejectedRow(t, recorder)
			parts, _ := other["content_parts"].([]any)
			require.Len(t, parts, 1)
			assert.Equal(t, "Rejected: content moderation unavailable", parts[0].(map[string]any)["key"])
			audit := auditOf(t, other)
			assert.Equal(t, "unavailable", audit["source"])
			assert.Equal(t, failure.class, audit["error"].(map[string]any)["class"])
			assert.NotContains(t, other, "sk-mod-")
			assertNoRetry(t, other)
			stored, err := model.GetChannelById(failureChannels[failure.suffix].Id, true)
			require.NoError(t, err)
			assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
		})
	}

	rowsOfType := func(rows []*model.Log, logType int) []*model.Log {
		var found []*model.Log
		for _, row := range rows {
			if row.Type == logType {
				found = append(found, row)
			}
		}
		return found
	}
	// keyFailureRow checks the error row of an attempt that a key-level
	// moderation failure ended: the user reads the failed check's message,
	// administrators the moderation answer and the check.
	keyFailureRow := func(t *testing.T, row *model.Log, channelID int, class string, status int) {
		t.Helper()
		assert.Equal(t, channelID, row.ChannelId)
		assert.Equal(t, "status_code=503, Content moderation is unavailable; the request was not sent", row.Content)
		other := otherOf(t, row)
		assert.Equal(t, "content_moderation_unavailable", other["error_code"])
		adminInfo := adminOf(t, other)
		assert.Equal(t, float64(status), adminInfo["upstream_status_code"])
		assert.Contains(t, adminInfo["upstream_error"], fmt.Sprintf("status_code=%d, content moderation call failed: ", status))
		assert.Equal(t, class, auditOf(t, other)["error"].(map[string]any)["class"])
		assert.NotContains(t, row.Other, "sk-mod-", "never the key")
	}
	for _, failure := range keyFailures {
		t.Run("a key-level moderation failure is the channel's error and fails over: "+failure.suffix, func(t *testing.T) {
			mark := upstream.mark()
			recorder := chat("mod-key-"+failure.suffix, userMessage(unique("clean text for an unusable key")))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			calls := upstream.since(mark, "")
			require.Len(t, calls, 3)
			backupKey := "Bearer " + keyBackups[failure.suffix].Key
			assert.Equal(t, []string{"/v1/moderations", "/v1/moderations", "/v1/chat/completions"}, []string{calls[0].Path, calls[1].Path, calls[2].Path})
			assert.Equal(t, []string{"Bearer sk-mod-" + failure.suffix, backupKey, backupKey}, []string{calls[0].Authorization, calls[1].Authorization, calls[2].Authorization},
				"only the check went out with the unusable key")
			assert.Empty(t, leak.since(0, ""), "an unmoderated channel received the request")
			audit := consumeAudit(t, recorder)
			assert.Equal(t, "passed", audit["status"])
			assert.Equal(t, float64(keyBackups[failure.suffix].Id), audit["channel_id"])

			rows := rowsOf(t, requestID(recorder))
			assert.Empty(t, rowsOfType(rows, model.LogTypeModeration), "a request another channel served is not rejected")
			errorRows := rowsOfType(rows, model.LogTypeError)
			require.Len(t, errorRows, 1)
			status := http.StatusUnauthorized
			switch failure.suffix {
			case "403":
				status = http.StatusForbidden
			case "quota":
				status = http.StatusTooManyRequests
			}
			keyFailureRow(t, errorRows[0], keyChannels[failure.suffix].Id, failure.class, status)
			view := userView(t, requestID(recorder))
			assert.NotContains(t, view, "content moderation call failed")
			assert.NotContains(t, view, "sk-mod-")
			events, _ := adminOf(t, otherOf(t, errorRows[0]))["request_policy"].([]any)
			require.NotEmpty(t, events)
			last := events[len(events)-1].(map[string]any)
			assert.Equal(t, "retry", last["decision"].(map[string]any)["action"])
			assert.Equal(t, "channel_disable_requested", last["health"])
			assert.Eventually(t, func() bool {
				stored, err := model.GetChannelById(keyChannels[failure.suffix].Id, true)
				return err == nil && stored.Status == common.ChannelStatusAutoDisabled
			}, 5*time.Second, 10*time.Millisecond, "the status code disables the channel")
		})
	}

	t.Run("a key-level moderation failure that no other channel serves answers 503 with one moderation row", func(t *testing.T) {
		mark := upstream.mark()
		recorder := chat("mod-key-alone", userMessage(unique("clean text, no other channel")))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		assert.Equal(t, apiError{Message: "Content moderation is unavailable; the request was not sent (request id: " + requestID(recorder) + ")", Type: "new_api_error", Code: "content_moderation_unavailable"}, errorOf(t, recorder))
		assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
		checks := upstream.since(mark, "/v1/moderations")
		require.NotEmpty(t, checks)

		rows := rowsOf(t, requestID(recorder))
		rejected := rowsOfType(rows, model.LogTypeModeration)
		require.Len(t, rejected, 1, "one moderation row for the request")
		assert.Equal(t, rejected[0], rows[len(rows)-1])
		errorRows := rowsOfType(rows, model.LogTypeError)
		require.Len(t, errorRows, len(checks), "one error row per attempt")
		for _, row := range errorRows {
			keyFailureRow(t, row, keyAlone.Id, "http_401", http.StatusUnauthorized)
		}
		other := otherOf(t, rejected[0])
		audit := auditOf(t, other)
		assert.Equal(t, "unavailable", audit["source"])
		assert.Equal(t, "http_401", audit["error"].(map[string]any)["class"])
		attempts, _ := audit["key_failures"].([]any)
		require.Len(t, attempts, len(checks))
		for i, attempt := range attempts {
			assert.Equal(t, map[string]any{"channel_id": float64(keyAlone.Id), "retry_index": float64(i), "attempt": float64(i + 1), "class": "http_401", "status": float64(http.StatusUnauthorized)}, attempt)
		}
		assert.NotContains(t, rejected[0].Other, "sk-mod-")
		events, _ := adminOf(t, other)["request_policy"].([]any)
		require.NotEmpty(t, events)
		assert.Equal(t, map[string]any{"action": "stop", "reason": "local_rejection", "source": "channel"}, events[len(events)-1].(map[string]any)["decision"])
		assert.Eventually(t, func() bool {
			stored, err := model.GetChannelById(keyAlone.Id, true)
			return err == nil && stored.Status == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("key-level moderation failures under auto cross-group retry are numbered by attempt", func(t *testing.T) {
		// One retry per group: the second group starts its retry counter at
		// 0 again, so the retry index repeats while the attempt number does
		// not.
		common.RetryTimes = 1
		previousUsable, previousAuto := setting.UserUsableGroups2JSONString(), setting.AutoGroups2JsonString()
		t.Cleanup(func() {
			common.RetryTimes = 2
			require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsable))
			require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAuto))
		})
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","auto":"Auto"}`))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","default"]`))
		mark := upstream.mark()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mod-key-cross","messages":`+userMessage(unique("clean text, two groups"))+`}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer modautokey")
		engine.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
		require.Len(t, upstream.since(mark, "/v1/moderations"), 4, "two attempts in each group")

		rejected := rowsOfType(rowsOf(t, requestID(recorder)), model.LogTypeModeration)
		require.Len(t, rejected, 1)
		var got []map[string]any
		attempts, _ := auditOf(t, otherOf(t, rejected[0]))["key_failures"].([]any)
		for _, attempt := range attempts {
			entry := attempt.(map[string]any)
			assert.Equal(t, float64(keyCross.Id), entry["channel_id"])
			got = append(got, map[string]any{"retry_index": entry["retry_index"], "attempt": entry["attempt"]})
		}
		assert.Equal(t, []map[string]any{
			{"retry_index": float64(0), "attempt": float64(1)},
			{"retry_index": float64(1), "attempt": float64(2)},
			{"retry_index": float64(0), "attempt": float64(3)},
			{"retry_index": float64(1), "attempt": float64(4)},
		}, got, "the retry index starts again in the second group; the attempt number does not")
	})

	t.Run("a key-level moderation failure whose retry meets an upstream error answers with that error and no moderation row", func(t *testing.T) {
		common.RetryTimes = 1
		t.Cleanup(func() { common.RetryTimes = 2 })
		mark := upstream.mark()
		recorder := chat("mod-key-then-500", userMessage(unique("clean text, broken backup")))
		require.Equal(t, http.StatusInternalServerError, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "upstream broke", "the backup's own error")
		assert.NotContains(t, recorder.Body.String(), "content_moderation_unavailable")
		chats := upstream.since(mark, "/v1/chat/completions")
		require.Len(t, chats, 1)
		assert.Equal(t, "Bearer sk-chat-500", chats[0].Authorization, "sent on the backup")

		rows := rowsOf(t, requestID(recorder))
		assert.Empty(t, rowsOfType(rows, model.LogTypeModeration), "a request that was sent is not a moderation rejection")
		errorRows := rowsOfType(rows, model.LogTypeError)
		require.Len(t, errorRows, 2, "one error row per attempt")
		keyFailureRow(t, errorRows[0], keyThenBroken.Id, "http_401", http.StatusUnauthorized)
		assert.NotEqual(t, keyThenBroken.Id, errorRows[1].ChannelId)
	})

	t.Run("a key-level moderation failure on a multi-key channel disables only that key", func(t *testing.T) {
		// Without a retry, so that no key selection saves the channel while
		// the key is being disabled.
		common.RetryTimes = 0
		t.Cleanup(func() { common.RetryTimes = 2 })
		mark := upstream.mark()
		recorder := chat("mod-key-multi", userMessage(unique("clean multi-key text")))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		rejected := rowsOfType(rowsOf(t, requestID(recorder)), model.LogTypeModeration)
		require.Len(t, rejected, 1)
		other := otherOf(t, rejected[0])
		assert.Equal(t, []any{map[string]any{"channel_id": float64(keyMulti.Id), "retry_index": float64(0), "attempt": float64(1), "multi_key_index": float64(0), "class": "http_401", "status": float64(http.StatusUnauthorized)}},
			auditOf(t, other)["key_failures"], "the polling mode starts with the first key")
		assert.Eventually(t, func() bool {
			stored, err := model.GetChannelById(keyMulti.Id, true)
			return err == nil && stored.ChannelInfo.MultiKeyStatusList[0] == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond, "the key is disabled")
		for range 2 {
			recorder = chat("mod-key-multi", userMessage(unique("clean multi-key text")))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		}
		var keys []string
		for _, call := range upstream.since(mark, "") {
			keys = append(keys, call.Path+" "+call.Authorization)
		}
		assert.Equal(t, []string{
			"/v1/moderations Bearer sk-mod-401",
			"/v1/moderations Bearer sk-ok-key-multi", "/v1/chat/completions Bearer sk-ok-key-multi",
			"/v1/moderations Bearer sk-ok-key-multi", "/v1/chat/completions Bearer sk-ok-key-multi",
		}, keys, "the disabled key is not used again")
		stored, err := model.GetChannelById(keyMulti.Id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
		assert.NotEqual(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[1])
	})

	t.Run("a fail-open channel forwards on a moderation failure and audits it", func(t *testing.T) {
		recorder := chat("mod-fail-open", userMessage(unique("clean text, fail open")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		audit := consumeAudit(t, recorder)
		assert.Equal(t, "fail_open", audit["status"])
		assert.Equal(t, "http_5xx", audit["error"].(map[string]any)["class"])
		var rows int64
		require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("request_id = ? AND type = ?", requestID(recorder), model.LogTypeModeration).Count(&rows).Error)
		assert.Zero(t, rows)
	})

	t.Run("a multi-key channel moderates with the key of the attempt", func(t *testing.T) {
		var keys []string
		for range 2 {
			mark := upstream.mark()
			recorder := chat("mod-multikey", userMessage(unique("clean multi-key text")))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			calls := upstream.since(mark, "")
			require.Len(t, calls, 2)
			assert.Equal(t, "/v1/moderations", calls[0].Path)
			assert.Equal(t, calls[0].Authorization, calls[1].Authorization, "moderation and chat use one key")
			keys = append(keys, calls[0].Authorization)
		}
		assert.ElementsMatch(t, []string{"Bearer sk-mk-1", "Bearer sk-mk-2"}, keys)
	})

	t.Run("a retry is moderated again on its channel, reusing verdicts", func(t *testing.T) {
		mark := upstream.mark()
		recorder := chat("mod-retry", userMessage(unique("clean retried text")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Len(t, upstream.since(mark, "/v1/moderations"), 1, "the second channel reuses the first verdict")
		assert.Len(t, upstream.since(mark, "/v1/chat/completions"), 2)

		recorder = chat("mod-retry-rule", userMessage(unique("retry-rule-hit text")))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		var types []int
		for _, row := range rowsOf(t, requestID(recorder)) {
			types = append(types, row.Type)
			if row.Type == model.LogTypeModeration {
				assert.Equal(t, retryRuleB.Id, row.ChannelId, "the row names the channel that rejected")
			}
		}
		assert.Equal(t, []int{model.LogTypeError, model.LogTypeModeration}, types, "the first channel's failure, then the rejection")
	})

	t.Run("a retry does not carry the first channel's organization", func(t *testing.T) {
		mark := upstream.mark()
		recorder := chat("mod-org", userMessage(unique("clean organization text")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "")
		require.Len(t, calls, 3)
		assert.Equal(t, "org-a", calls[0].Organization)
		for _, call := range calls[1:] {
			assert.Empty(t, call.Organization, call.Path)
		}
	})

	t.Run("a retry does not carry the first channel's header override", func(t *testing.T) {
		mark := upstream.mark()
		recorder := chat("mod-hdr", userMessage(unique("clean header override text")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "")
		require.Len(t, calls, 3)
		assert.Equal(t, "Bearer sk-chat-500", calls[0].Authorization, "the first channel's own override")
		assert.Equal(t, []string{"/v1/moderations", "/v1/chat/completions"}, []string{calls[1].Path, calls[2].Path})
		for _, call := range calls[1:] {
			assert.Equal(t, "Bearer sk-ok-hdr-b", call.Authorization, call.Path)
			assert.Equal(t, "B", call.Channel, call.Path)
		}
	})

	// distinct makes text of at least n runes, without whitespace, in which
	// no 2000-rune piece repeats another.
	distinct := func(prefix string, n int) string {
		prefix = strings.ReplaceAll(prefix, " ", "")
		var builder strings.Builder
		for i := 0; builder.Len() < n; i++ {
			fmt.Fprintf(&builder, "%s%06d,", prefix, i)
		}
		return builder.String()
	}
	engine.POST("/pg-test/chat/completions", func(c *gin.Context) {
		userCache, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		c.Set("id", user.Id)
		c.Set("username", user.Username)
		c.Set("role", common.RoleCommonUser)
		c.Set("group", user.Group)
		c.Set("user_group", user.Group)
		userCache.WriteContext(c)
	}, middleware.Distribute(), controller.Playground)

	t.Run("Claude- and Gemini-format clients are moderated with their system text", func(t *testing.T) {
		mark := upstream.mark()
		system, flagged := unique("claude system prompt"), unique("MOD-FLAG-SEXUAL claude")
		recorder := post("/v1/messages", `{"model":"mod-claude","max_tokens":16,"system":"`+system+`","messages":[{"role":"user","content":"`+flagged+`"}]}`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.JSONEq(t, `{"type":"error","error":{"type":"new_api_error","message":"The request was rejected by content moderation (request id: `+requestID(recorder)+`)"}}`, recorder.Body.String())
		calls := upstream.since(mark, "/v1/moderations")
		require.NotEmpty(t, calls)
		assert.Contains(t, calls[0].Body, system)
		assert.Contains(t, calls[0].Body, flagged)

		mark = upstream.mark()
		system, flagged = unique("gemini system prompt"), unique("MOD-FLAG-SEXUAL gemini")
		recorder = post("/v1beta/models/mod-gemini:generateContent", `{"systemInstruction":{"parts":[{"text":"`+system+`"}]},"contents":[{"role":"user","parts":[{"text":"`+flagged+`"}]}]}`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Equal(t, flaggedMessage(recorder), errorOf(t, recorder))
		calls = upstream.since(mark, "/v1/moderations")
		require.NotEmpty(t, calls)
		assert.Contains(t, calls[0].Body, system)
		assert.Contains(t, calls[0].Body, flagged)
		assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
	})

	for _, tc := range []struct{ name, path, body string }{
		{"Responses", "/v1/responses", `{"model":"mod-responses","input":"%s"}`},
		{"Responses compact", "/v1/responses/compact", `{"model":"mod-responses","input":"%s"}`},
		{"image generation", "/v1/images/generations", `{"model":"mod-image","prompt":"%s"}`},
		{"speech", "/v1/audio/speech", `{"model":"mod-speech","input":"%s","voice":"alloy"}`},
		{"playground", "/pg-test/chat/completions", `{"model":"mod-flag","messages":[{"role":"user","content":"%s"}]}`},
		{"chat converted to Responses", "/v1/chat/completions", `{"model":"mod-c2r","messages":[{"role":"user","content":"%s"}]}`},
		{"tool output that becomes user text on Responses", "/v1/chat/completions", `{"model":"mod-c2r","messages":[{"role":"user","content":"hello"},{"role":"tool","content":"%s"}]}`},
		{"a channel that passes the body through", "/v1/chat/completions", `{"model":"mod-pass","messages":[{"role":"user","content":"%s"}]}`},
	} {
		t.Run("a flagged "+tc.name+" request is rejected before the upstream", func(t *testing.T) {
			mark := upstream.mark()
			recorder := post(tc.path, fmt.Sprintf(tc.body, unique("MOD-FLAG-SEXUAL "+tc.name)))
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Equal(t, "content_moderation_flagged", errorOf(t, recorder).Code)
			for _, call := range upstream.since(mark, "") {
				assert.Equal(t, "/v1/moderations", call.Path, "nothing but moderation reached the upstream")
			}
			_, other := rejectedRow(t, recorder)
			assertNoRetry(t, other)
		})
	}

	t.Run("Claude- and Gemini-format bodies passed through to Chat Completions are moderated as chat bodies", func(t *testing.T) {
		flagURL := "https://img.example/" + strings.ReplaceAll(unique("flag-image-claude-pass"), " ", "") + ".png"
		for _, tc := range []struct{ name, path, body string }{
			{"a chat image part in a Claude-format body", "/v1/messages", `{"model":"mod-pass","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"` + flagURL + `"}}]}]}`},
			{"chat messages in a Gemini-format body", "/v1beta/models/mod-pass:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"messages":[{"role":"user","content":"` + unique("MOD-FLAG-SEXUAL gemini pass-through") + `"}]}`},
		} {
			mark := upstream.mark()
			recorder := post(tc.path, tc.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, tc.name+": "+recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "rejected by content moderation", tc.name)
			assert.NotEmpty(t, upstream.since(mark, "/v1/moderations"), tc.name)
			assert.Empty(t, upstream.since(mark, "/v1/chat/completions"), tc.name)
		}
	})

	t.Run("with the global pass-through setting a flagged request is rejected", func(t *testing.T) {
		globalSettings.PassThroughRequestEnabled = true
		t.Cleanup(func() { globalSettings.PassThroughRequestEnabled = passThrough })
		mark := upstream.mark()
		recorder := chat("mod-global-pass", userMessage(unique("MOD-FLAG-SEXUAL global pass-through")))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
		_, other := rejectedRow(t, recorder)
		assertNoRetry(t, other)
	})

	t.Run("a clean request converted to Responses reaches the Responses endpoint", func(t *testing.T) {
		mark := upstream.mark()
		recorder := chat("mod-c2r", userMessage(unique("clean chat for Responses")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Len(t, upstream.since(mark, "/v1/responses"), 1)
	})

	t.Run("the user's own moderation call and embeddings are not moderated again", func(t *testing.T) {
		mark := upstream.mark()
		recorder := post("/v1/moderations", `{"model":"mod-own","input":"`+unique("MOD-FLAG-SEXUAL own moderation call")+`"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Len(t, upstream.since(mark, "/v1/moderations"), 1, "only the relayed call")

		mark = upstream.mark()
		recorder = post("/v1/embeddings", `{"model":"mod-embed","input":"`+unique("MOD-FLAG-SEXUAL embedding")+`"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, "/v1/moderations"))
	})

	t.Run("Realtime on a moderated channel is refused as unsupported", func(t *testing.T) {
		testServer := httptest.NewServer(engine)
		t.Cleanup(testServer.Close)
		mark := upstream.mark()
		client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(testServer.URL, "http")+"/v1/realtime?model=mod-realtime", http.Header{"Authorization": []string{"Bearer modtestkey"}})
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })
		require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, message, err := client.ReadMessage()
		require.NoError(t, err)
		var event struct {
			Type    string   `json:"type"`
			EventID string   `json:"event_id"`
			Error   apiError `json:"error"`
		}
		require.NoError(t, common.Unmarshal(message, &event), string(message))
		assert.Equal(t, "error", event.Type)
		assert.Equal(t, "content_moderation_unsupported", event.Error.Code)
		assert.Empty(t, upstream.since(mark, ""))
		var row model.Log
		require.NoError(t, model.LOG_DB.Where("request_id = ? AND type = ?", strings.TrimPrefix(event.EventID, "evt_"), model.LogTypeModeration).First(&row).Error)
		audit := auditOf(t, otherOf(t, &row))
		assert.Equal(t, "unsupported", audit["source"])
		assert.Equal(t, "realtime", audit["detail"])
	})

	t.Run("a NovelAI-native request on a moderated channel is refused as unsupported", func(t *testing.T) {
		mark := upstream.mark()
		recorder := post("/ai/generate-image", `{"input":"a cat","model":"mod-novelai","action":"generate","parameters":{"n_samples":1}}`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.JSONEq(t, `{"statusCode":400,"message":"This endpoint is not available on channels with content moderation (request id: `+requestID(recorder)+`)"}`, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, ""))
		_, other := rejectedRow(t, recorder)
		parts, _ := other["content_parts"].([]any)
		require.Len(t, parts, 1)
		assert.Equal(t, "Rejected: endpoint not available with content moderation", parts[0].(map[string]any)["key"])
		assert.Equal(t, "novelai", auditOf(t, other)["detail"])
	})

	for _, tc := range []struct{ name, path, body, detail string }{
		{"a token-id prompt", "/v1/completions", `{"model":"mod-completions","prompt":[[1,2,3]]}`, "token_prompt"},
		{"a Responses item with a case-variant key", "/v1/responses", `{"model":"mod-responses","input":[{"type":"message","role":"user","content":"x","ROLE":"assistant"}]}`, "ambiguous_keys"},
		{"a passed-through message with a case-variant key", "/v1/chat/completions", `{"model":"mod-pass","messages":[{"role":"user","content":"MOD-FLAG-SEXUAL","Content":"hi"}]}`, "ambiguous_keys"},
		{"a passed-through image prompt with a case-variant key", "/v1/images/generations", `{"model":"mod-pass-img","prompt":"MOD-FLAG-SEXUAL image","PROMPT":"a calm lake"}`, "ambiguous_keys"},
	} {
		t.Run(tc.name+" cannot be moderated and is refused", func(t *testing.T) {
			mark := upstream.mark()
			recorder := post(tc.path, tc.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Equal(t, apiError{Message: "This request contains content that content moderation cannot check, so it was not sent (request id: " + requestID(recorder) + ")", Type: "new_api_error", Code: "content_moderation_unsupported"}, errorOf(t, recorder))
			assert.Empty(t, upstream.since(mark, ""))
			_, other := rejectedRow(t, recorder)
			parts, _ := other["content_parts"].([]any)
			require.Len(t, parts, 1)
			assert.Equal(t, "Rejected: request content cannot be moderated", parts[0].(map[string]any)["key"])
			assert.Equal(t, tc.detail, auditOf(t, other)["detail"])
			assertNoRetry(t, other)
		})
	}

	t.Run("text over the budget is sent in part and audited as truncated", func(t *testing.T) {
		// Pieces of 2000 and 1000 runes: the budget of 1000 checks the start
		// of the first piece instead of skipping it.
		text := distinct(unique("budget"), 3000)[:3000]
		mark := upstream.mark()
		recorder := chat("mod-budget", userMessage(text))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "/v1/moderations")
		require.Len(t, calls, 1)
		assert.Contains(t, calls[0].Body, `"input":["`+text[:1000]+`"]`)
		audit := consumeAudit(t, recorder)
		assert.Equal(t, true, audit["truncated"])
		assert.Equal(t, float64(1000), audit["checked_chars"])
	})

	t.Run("a long system prompt cannot push the newest user message out of the budget", func(t *testing.T) {
		recorder := chat("mod-budget-system", `[{"role":"system","content":"`+distinct(unique("padding"), 12000)+`"},{"role":"user","content":"`+unique("MOD-FLAG-SEXUAL after padding")+`"}]`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		_, other := rejectedRow(t, recorder)
		assert.Equal(t, true, auditOf(t, other)["truncated"])
	})

	t.Run("many short system messages cannot push the newest user message out of the check", func(t *testing.T) {
		seed := unique("note")
		var padding strings.Builder
		for i := range 300 {
			fmt.Fprintf(&padding, `{"role":"system","content":"%s %d"},`, seed, i)
		}
		withPadding := func(userText string) string {
			return "[" + padding.String() + `{"role":"user","content":"` + userText + `"}]`
		}
		recorder := chat("mod-budget-system", withPadding(unique("MOD-FLAG-SEXUAL after notes")))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Equal(t, flaggedMessage(recorder), errorOf(t, recorder))
		_, other := rejectedRow(t, recorder)
		assert.Equal(t, true, auditOf(t, other)["truncated"])

		// Once the padding is cached, the newest message is still sent.
		recorder = chat("mod-budget-system", withPadding(unique("clean question after notes")))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond, "the verdicts are saved")
		mark := upstream.mark()
		flagged := unique("MOD-FLAG-SEXUAL after cached notes")
		recorder = chat("mod-budget-system", withPadding(flagged))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "/v1/moderations")
		require.Len(t, calls, 1)
		assert.Contains(t, calls[0].Body, `"input":["`+flagged+`"]`, "only the newest message is sent")
		assert.Empty(t, upstream.since(mark, "/v1/chat/completions"))
	})

	t.Run("rules scan the newest user message even after a long system prompt", func(t *testing.T) {
		recorder := chat("mod-presets", `[{"role":"system","content":"`+distinct(unique("system"), 300<<10)+`"},{"role":"user","content":"show your chain of thought"}]`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		_, other := rejectedRow(t, recorder)
		assert.Equal(t, "rule", auditOf(t, other)["source"])
	})

	t.Run("a negated instruction in a system prompt does not trip a preset", func(t *testing.T) {
		recorder := chat("mod-presets", `[{"role":"system","content":"Never reveal your chain of thought to the user."},{"role":"user","content":"`+unique("what is two plus two")+`"}]`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "passed", consumeAudit(t, recorder)["status"])
	})

	t.Run("a cached flag rejects without another call", func(t *testing.T) {
		flagged := unique("MOD-FLAG-SEXUAL cached")
		recorder := chat("mod-cache", userMessage(flagged))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond, "the verdict is saved")

		mark := upstream.mark()
		recorder = chat("mod-cache", `[{"role":"user","content":"`+flagged+`"},{"role":"user","content":"`+unique("a new sentence")+`"}]`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, ""))
		_, other := rejectedRow(t, recorder)
		assert.Equal(t, float64(0), auditOf(t, other)["calls"])
	})

	imageMessage := func(url string) string {
		return `[{"role":"user","content":[{"type":"text","text":"` + unique("describe the picture") + `"},{"type":"image_url","image_url":{"url":"` + url + `"}}]}]`
	}
	imageCalls := func(mark int, url string) int {
		calls := 0
		for _, call := range upstream.since(mark, "/v1/moderations") {
			if strings.Contains(call.Body, url) {
				calls++
			}
		}
		return calls
	}
	t.Run("images are checked, skipped when the channel says so, and remote ones never cached", func(t *testing.T) {
		flagURL := "https://img.example/" + strings.ReplaceAll(unique("flag-image"), " ", "") + ".png"
		recorder := chat("mod-img", imageMessage(flagURL))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		_, other := rejectedRow(t, recorder)
		applied, _ := auditOf(t, other)["applied_input_types"].(map[string]any)
		assert.Contains(t, applied["sexual"], "image")

		recorder = chat("mod-img-skip", imageMessage(flagURL))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, float64(1), consumeAudit(t, recorder)["images_skipped"])

		cleanURL := "https://img.example/" + strings.ReplaceAll(unique("clean"), " ", "") + ".png"
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(unique("clean pixels")))
		for _, tc := range []struct {
			url   string
			calls int
		}{{cleanURL, 2}, {dataURL, 1}} {
			mark := upstream.mark()
			for range 2 {
				recorder = chat("mod-img", imageMessage(tc.url))
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
			}
			assert.Equal(t, tc.calls, imageCalls(mark, tc.url), tc.url)
		}
	})

	t.Run("converter paths that turn other fields into user or system content are moderated", func(t *testing.T) {
		flag := func(name string) string { return unique("MOD-FLAG-SEXUAL " + name) }
		flagImage := func(name string) string {
			return "https://img.example/" + strings.ReplaceAll(unique("flag-image-"+name), " ", "") + ".png"
		}
		for name, body := range map[string]string{
			"Claude system role message": `{"model":"mod-claude","max_tokens":16,"messages":[{"role":"system","content":"` + flag("claude system role") + `"},{"role":"user","content":"hi"}]}`,
			"Claude input_text block":    `{"model":"mod-claude","max_tokens":16,"messages":[{"role":"user","content":[{"type":"input_text","text":"` + flag("claude input_text") + `"}]}]}`,
			"Claude text document":       `{"model":"mod-claude","max_tokens":16,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"` + flag("claude document") + `"}}]}]}`,
			"Claude tool_result image":   `{"model":"mod-claude","max_tokens":16,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"url","url":"` + flagImage("tool") + `"}}]}]}]}`,
			"Claude image url over data": `{"model":"mod-claude","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo=","url":"` + flagImage("source") + `"}}]}]}`,
			"Claude system block foo":    `{"model":"mod-claude","max_tokens":16,"system":[{"type":"foo","text":"` + flag("claude system block") + `"}],"messages":[{"role":"user","content":"hi"}]}`,
			"Gemini system role":         `{"contents":[{"role":"system","parts":[{"text":"` + flag("gemini system role") + `"}]}]}`,
			"Gemini USER role":           `{"contents":[{"role":"USER","parts":[{"text":"` + flag("gemini USER role") + `"}]}]}`,
			"Gemini text inlineData":     `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"text/plain","data":"` + base64.StdEncoding.EncodeToString([]byte(flag("gemini inline text"))) + `"}}]}]}`,
			"Gemini image fileData":      `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"image/png","fileUri":"` + flagImage("file") + `"}}]}]}`,
			"Gemini thought system part": `{"systemInstruction":{"parts":[{"text":"` + flag("gemini thought") + `","thought":true}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		} {
			path := "/v1/messages"
			if strings.HasPrefix(name, "Gemini") {
				path = "/v1beta/models/mod-gemini:generateContent"
			}
			mark := upstream.mark()
			recorder := post(path, body)
			assert.Equal(t, http.StatusBadRequest, recorder.Code, name+": "+recorder.Body.String())
			assert.Empty(t, upstream.since(mark, "/v1/chat/completions"), name)
		}
	})

	t.Run("every prompt of a multipart image edit is moderated", func(t *testing.T) {
		var form bytes.Buffer
		writer := multipart.NewWriter(&form)
		require.NoError(t, writer.WriteField("model", "mod-image"))
		require.NoError(t, writer.WriteField("prompt", unique("a clean prompt")))
		require.NoError(t, writer.WriteField("prompt", unique("MOD-FLAG-SEXUAL second prompt")))
		part, err := writer.CreateFormFile("image", "a.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("\x89PNG\r\n\x1a\n"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		mark := upstream.mark()
		recorder := send("/v1/images/edits", writer.FormDataContentType(), &form)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Empty(t, upstream.since(mark, "/v1/images/edits"))
	})

	t.Run("an administrator's listing finds the rows, also with the deputy filter", func(t *testing.T) {
		recorder := chat("mod-flag", userMessage(unique("MOD-FLAG-SEXUAL listing")))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		logs, _, err := model.GetAllLogs(model.LogFilter{ExcludeTypes: model.RechargeLogTypes, RequestId: requestID(recorder)}, 0, 10)
		require.NoError(t, err)
		require.Len(t, logs, 1)
		assert.Equal(t, model.LogTypeModeration, logs[0].Type)
		logs, _, err = model.GetAllLogs(model.LogFilter{Type: model.LogTypeModeration}, 0, 1000)
		require.NoError(t, err)
		require.NotEmpty(t, logs)
		for _, row := range logs {
			assert.Equal(t, model.LogTypeModeration, row.Type)
		}
	})

	gatewayIdentifier := relaycommon.GatewaySafetyIdentifier(user.Id)
	lastBody := func(t *testing.T, mark int, path string) string {
		t.Helper()
		calls := upstream.since(mark, path)
		require.NotEmpty(t, calls, path)
		return calls[len(calls)-1].Body
	}
	t.Run("the gateway safety identifier replaces the client's on every OpenAI body", func(t *testing.T) {
		for _, tc := range []struct{ name, path, body, upstreamPath string }{
			{"chat", "/v1/chat/completions", `{"model":"mod-sid","safety_identifier":"evil","messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
			{"Responses", "/v1/responses", `{"model":"mod-sid","safety_identifier":"evil","input":"hi"}`, "/v1/responses"},
			{"Claude format", "/v1/messages", `{"model":"mod-sid","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
			{"Gemini format", "/v1beta/models/mod-sid:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "/v1/chat/completions"},
			{"chat converted to Responses despite a param override", "/v1/chat/completions", `{"model":"mod-sid-resp","safety_identifier":"evil","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		} {
			mark := upstream.mark()
			recorder := post(tc.path, tc.body)
			require.Equal(t, http.StatusOK, recorder.Code, tc.name+": "+recorder.Body.String())
			body := lastBody(t, mark, tc.upstreamPath)
			assert.Contains(t, body, `"safety_identifier":"`+gatewayIdentifier+`"`, tc.name)
			assert.NotContains(t, body, "evil", tc.name)
		}
		for _, tc := range []struct{ path, body, upstreamPath string }{
			{"/v1/chat/completions", `{"model":"mod-sid-pass", "messages" : [{"role":"user","content":"hi"}], "safety_identifier":"evil"}`, "/v1/chat/completions"},
			{"/v1/responses", `{"safety_identifier":"evil",  "model":"mod-sid-pass","input":"hi"}`, "/v1/responses"},
			{"/v1/messages", `{"model":"mod-sid-pass","max_tokens":16, "safety_identifier":"evil","messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
			{"/v1beta/models/mod-sid-pass:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"safety_identifier":"evil" }`, "/v1/chat/completions"},
		} {
			mark := upstream.mark()
			recorder := post(tc.path, tc.body)
			require.Equal(t, http.StatusOK, recorder.Code, tc.path+": "+recorder.Body.String())
			assert.Equal(t, strings.Replace(tc.body, `"evil"`, `"`+gatewayIdentifier+`"`, 1), lastBody(t, mark, tc.upstreamPath), tc.path+": only the identifier changes")
		}
	})

	t.Run("without the setting the client's identifier is handled as before", func(t *testing.T) {
		mark := upstream.mark()
		recorder := post("/v1/chat/completions", `{"model":"mod-sid-off","safety_identifier":"evil","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.NotContains(t, lastBody(t, mark, "/v1/chat/completions"), "safety_identifier")
		mark = upstream.mark()
		recorder = post("/v1/chat/completions", `{"model":"mod-sid-off-pass","safety_identifier":"evil","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, lastBody(t, mark, "/v1/chat/completions"), `"safety_identifier":"evil"`)
	})

	for _, channelType := range []int{constant.ChannelTypeAzure, constant.ChannelTypeCustom, constant.ChannelTypeAnthropic, constant.ChannelTypeCodex} {
		t.Run(fmt.Sprintf("channel type %d ignores both settings", channelType), func(t *testing.T) {
			mark := upstream.mark()
			post("/v1/chat/completions", fmt.Sprintf(`{"model":"mod-type-%d","safety_identifier":"evil","messages":[{"role":"user","content":"%s"}]}`, channelType, unique("MOD-FLAG-SEXUAL other type")))
			assert.Empty(t, upstream.since(mark, "/v1/moderations"))
			for _, call := range upstream.since(mark, "") {
				assert.NotContains(t, call.Body, gatewayIdentifier)
				if channelType == constant.ChannelTypeAzure || channelType == constant.ChannelTypeCustom {
					assert.NotContains(t, call.Body, "evil", "stripped as before")
				}
			}
			if channelType == constant.ChannelTypeAzure || channelType == constant.ChannelTypeCustom {
				assert.NotEmpty(t, upstream.since(mark, ""), "the request was forwarded")
			}
		})
	}

	t.Run("a Sora task on a moderated OpenAI channel is checked before submission", func(t *testing.T) {
		var tasks int64
		countTasks := func() int64 {
			require.NoError(t, model.DB.Model(&model.Task{}).Count(&tasks).Error)
			return tasks
		}
		before, tasksBefore := quota(t), countTasks()
		for _, tc := range []struct {
			model, prompt, code string
			status              int
		}{
			{"sora-2", unique("MOD-FLAG-SEXUAL video"), "content_moderation_flagged", http.StatusBadRequest},
			{"sora-2-pro", unique("a quiet harbour at dawn"), "content_moderation_unavailable", http.StatusServiceUnavailable},
		} {
			mark := upstream.mark()
			recorder := post("/v1/videos", `{"model":"`+tc.model+`","prompt":"`+tc.prompt+`","seconds":"4","size":"720x1280"}`)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			var answer struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &answer))
			assert.Equal(t, tc.code, answer.Code)
			assert.True(t, strings.HasSuffix(answer.Message, " (request id: "+requestID(recorder)+")"), answer.Message)
			assert.Empty(t, upstream.since(mark, "/v1/videos"))
			_, other := rejectedRow(t, recorder)
			assertNoRetry(t, other)
		}
		assert.Equal(t, tasksBefore, countTasks(), "no task row")
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 && quota(t) == before }, 5*time.Second, 10*time.Millisecond, "refunded")

		mark := upstream.mark()
		recorder := post("/v1/videos", `{"model":"sora-2","prompt":"`+unique("a quiet harbour at dawn")+`","seconds":"4","size":"720x1280"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "/v1/videos")
		require.Len(t, calls, 1)
		assert.Equal(t, "Bearer "+soraChannel.Key, calls[0].Authorization)
		assert.Equal(t, tasksBefore+1, countTasks())
	})

	t.Run("a key-level moderation failure on a Sora task is the channel's error", func(t *testing.T) {
		mark := upstream.mark()
		recorder := post("/v1/videos", `{"model":"sora-2-key","prompt":"`+unique("a quiet harbour at dawn")+`","seconds":"4","size":"720x1280"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		calls := upstream.since(mark, "/v1/videos")
		require.Len(t, calls, 1)
		assert.Equal(t, "Bearer sk-ok-sora-key-backup", calls[0].Authorization, "submitted on the next channel")
		rows := rowsOf(t, requestID(recorder))
		assert.Empty(t, rowsOfType(rows, model.LogTypeModeration))
		errorRows := rowsOfType(rows, model.LogTypeError)
		require.Len(t, errorRows, 1)
		keyFailureRow(t, errorRows[0], soraKey.Id, "http_403", http.StatusForbidden)
		assert.Eventually(t, func() bool {
			stored, err := model.GetChannelById(soraKey.Id, true)
			return err == nil && stored.Status == common.ChannelStatusAutoDisabled
		}, 5*time.Second, 10*time.Millisecond)

		// Without auto-disable the dead key stays enabled, so the first
		// attempt and both retries land on it.
		common.AutomaticDisableChannelEnabled = false
		t.Cleanup(func() { common.AutomaticDisableChannelEnabled = true })
		before := quota(t)
		mark = upstream.mark()
		recorder = post("/v1/videos", `{"model":"sora-2-dead","prompt":"`+unique("a quiet harbour at dusk")+`","seconds":"4","size":"720x1280"}`)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		var answer struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &answer))
		assert.Equal(t, "content_moderation_unavailable", answer.Code)
		assert.Equal(t, "Content moderation is unavailable; the request was not sent (request id: "+requestID(recorder)+")", answer.Message)
		assert.Empty(t, upstream.since(mark, "/v1/videos"))
		rejected := rowsOfType(rowsOf(t, requestID(recorder)), model.LogTypeModeration)
		require.Len(t, rejected, 1)
		attempts, _ := auditOf(t, otherOf(t, rejected[0]))["key_failures"].([]any)
		require.Len(t, attempts, 3, "the first attempt and two retries")
		for i, attempt := range attempts {
			assert.Equal(t, "http_429_quota", attempt.(map[string]any)["class"])
			assert.Equal(t, float64(i), attempt.(map[string]any)["retry_index"], "each attempt has its own retry index")
			assert.Equal(t, float64(i+1), attempt.(map[string]any)["attempt"], "and its own place in the retry chain")
		}
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 && quota(t) == before }, 5*time.Second, 10*time.Millisecond, "refunded")
	})
}
