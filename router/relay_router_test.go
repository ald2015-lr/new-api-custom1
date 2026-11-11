package router

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
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
	// $0.02 per image, and $0.06 per image as an expression.
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"nai-diffusion-4-5-full":0.02,"nai-retry-0":0.02,"nai-retry-1":0.02,"nai-retry-2":0.02,"nai-retry-3":0.02,"nai-retry-4":0.02,"nai-retry-5":0.02}`))
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
	channel := model.Channel{Name: "novelai-upstream", Key: "sk-novelai", Status: common.ChannelStatusEnabled, Type: channelType, Group: "default", Models: "nai-diffusion-4-5-full,nai-diffusion-3", BaseURL: &baseURL}
	require.NoError(t, model.DB.Create(&channel).Error)
	for _, name := range []string{"nai-diffusion-4-5-full", "nai-diffusion-3"} {
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
		var other map[string]any
		require.NoError(t, common.Unmarshal([]byte(last.Other), &other))
		assert.Equal(t, float64(2), other["image_count"])
		assert.Equal(t, "tiered_expr", other["billing_mode"])
		assert.Equal(t, before-60_000, walletQuota())
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
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"nai-diffusion-4-5-full":0.02,"nai-diffusion-5-curated":0.02,"nai-diffusion-3":0.02,"nai-diffusion-4-full":0.02,"nai-diffusion-furry-3":0.02,"nai-diffusion-4-curated-preview":0.02}`))
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
	channel := model.Channel{Name: "novelai-official", Key: "pst-openai", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-4-5-full,nai-diffusion-5-full,nai-diffusion-5-curated,nai-diffusion-furry-3", BaseURL: common.GetPointer(server.URL)}
	require.NoError(t, model.DB.Create(&channel).Error)
	for _, name := range []string{"nai-diffusion-4-5-full", "nai-diffusion-5-full", "nai-diffusion-5-curated", "nai-diffusion-furry-3"} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true}).Error)
	}
	// This channel makes NovelAI's prompt presets opt-in.
	withoutPresets := model.Channel{Name: "novelai-without-presets", Key: "pst-plain", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeNovelAI, Group: "default",
		Models: "nai-diffusion-4-full", BaseURL: common.GetPointer(server.URL), OtherSettings: `{"novelai_presets_disabled":true}`}
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
		} `json:"data"`
		Background   string `json:"background"`
		OutputFormat string `json:"output_format"`
		Size         string `json:"size"`
		Usage        struct {
			InputTokens        int `json:"input_tokens"`
			InputTokensDetails struct {
				TextTokens int `json:"text_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	decode := func(recorder *httptest.ResponseRecorder) imagesResponse {
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
		var response imagesResponse
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.NotZero(t, response.Created)
		assert.Positive(t, response.Usage.InputTokens)
		assert.Equal(t, response.Usage.InputTokens, response.Usage.InputTokensDetails.TextTokens)
		assert.Zero(t, response.Usage.OutputTokens)
		assert.Equal(t, response.Usage.InputTokens, response.Usage.TotalTokens)
		return response
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
		assert.Equal(t, tokenkit.Estimate("nai-diffusion-4-5-full", sent.body["input"].(string)), log.PromptTokens, "the final prompt's estimate")
		assert.Equal(t, log.PromptTokens, response.Usage.InputTokens)
		assert.Zero(t, log.CompletionTokens)
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("fewer images than reserved settle at the images received", func(t *testing.T) {
		upstream.queue(http.StatusOK, "binary/octet-stream", novelAIZip(t, red))
		before := walletQuota()

		response := decode(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":3}`)))

		assert.Len(t, response.Data, 1)
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
		assert.Equal(t, before-10_000, walletQuota())
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
		upstream.queue(http.StatusOK, "text/event-stream", sse(
			map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": 10, "image": encode(blue)},
			map[string]any{"event_type": "intermediate", "samp_ix": 0, "step_ix": 11, "image": encode(blue)},
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
		assert.Equal(t, tokenkit.Estimate("nai-diffusion-4-5-full", sent.body["input"].(string)), log.PromptTokens)
		assert.Zero(t, log.CompletionTokens)
		assert.Equal(t, float64(log.PromptTokens), events[2].data["usage"].(map[string]any)["input_tokens"], "the usage on the last image")
		assert.Zero(t, events[1].data["usage"].(map[string]any)["input_tokens"])
		assert.Equal(t, before-20_000, walletQuota())
	})

	t.Run("a stream error after 200 bills the images delivered, before any image nothing", func(t *testing.T) {
		failure := map[string]any{"event_type": "error", "code": 500, "message": "Error generating image, an internal error occurred"}
		upstream.queue(http.StatusOK, "text/event-stream", sse(map[string]any{"event_type": "final", "samp_ix": 0, "image": encode(red)}, failure))
		before := walletQuota()

		events := streamEvents(send("/v1/images/generations", "application/json", []byte(`{"model":"nai-diffusion-4-5-full","prompt":"a cat","n":2,"stream":true}`)))

		require.Len(t, events, 3)
		assert.Equal(t, "image_generation.completed", events[0].name)
		assert.Contains(t, events[1].data["error"].(map[string]any)["message"], "Error generating image, an internal error occurred")
		assert.Equal(t, map[string]any{"[DONE]": true}, events[2].data)
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
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
		assert.Equal(t, float64(lastLog().PromptTokens), events[0].data["usage"].(map[string]any)["input_tokens"])
		assert.Equal(t, 10_000, lastLog().Quota)
		assert.Contains(t, lastLog().Content, "Image count 1")
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
		assert.Equal(t, tokenkit.Estimate("nai-diffusion-4-5-full", sent.body["input"].(string)), log.PromptTokens, "the final prompt's estimate, not the history's")
		assert.Zero(t, log.CompletionTokens, "the images are not counted as text")
		assert.Equal(t, [3]int{log.PromptTokens, 0, log.PromptTokens}, [3]int{response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens})
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
		assert.Equal(t, float64(log.PromptTokens), events[4].data["usage"].(map[string]any)["prompt_tokens"])
		assert.Zero(t, events[4].data["usage"].(map[string]any)["completion_tokens"])
		assert.Equal(t, map[string]any{"[DONE]": true}, events[5].data)
		assert.Equal(t, "/ai/generate-image", upstream.last().path, "chat asks NovelAI for the whole answer")
		assert.Equal(t, 60_000, log.Quota, "two images at $0.06")
		var other map[string]any
		require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
		assert.Equal(t, float64(2), other["image_count"])
		assert.Equal(t, "tiered_expr", other["billing_mode"])
		assert.Contains(t, log.Content, "Image count 2")
		assert.Zero(t, log.CompletionTokens)
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
			retried := model.Channel{Name: "novelai-retry", Key: "pst-retry", Status: common.ChannelStatusEnabled, Type: channelType, Group: "default", Models: "nai-diffusion-4-curated-preview", BaseURL: &baseURL, Priority: &priority, AutoBan: common.GetPointer(0)}
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
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
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
