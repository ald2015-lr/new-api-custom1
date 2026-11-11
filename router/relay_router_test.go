package router

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
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

// A NovelAI channel serves only the NovelAI native routes: NovelAI's own 201
// zip passes through as sent, while an OpenAI-format request for one of its
// models, converted or passed through, or a search on a model the channel
// lists, is rejected with a 400 before anything reaches NovelAI, and charges
// nothing.
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
	for _, rejected := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"nai-diffusion-4-5-full","messages":[{"role":"user","content":"a cat"}]}`},
		{"/v1/images/generations", `{"model":"nai-diffusion-4-5-full","prompt":"a cat"}`},
		{"/v1/chat/completions", `{"model":"nai-diffusion-3","messages":[{"role":"user","content":"a cat"}]}`},
		{"/v1/images/generations", `{"model":"nai-diffusion-3","prompt":"a cat"}`},
		{"/v1/alpha/search", `{"model":"nai-diffusion-4-5-full","query":"a cat"}`},
		{"/v1/tools/search", `{"query":"a cat"}`},
	} {
		t.Run(rejected.path+" "+rejected.body, func(t *testing.T) {
			calls, before := upstream.calls, walletQuota()

			recorder := send(rejected.path, rejected.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			var payload map[string]map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload["error"]["message"], nativeOnly)
			assert.Equal(t, calls, upstream.calls, "nothing reaches NovelAI")
			assert.Eventually(t, func() bool { return walletQuota() == before }, 5*time.Second, 10*time.Millisecond)
		})
	}
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
