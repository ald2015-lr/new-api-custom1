package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Since rc.52 (#8335) several task plugins may declare one built-in channel
// type, so a task plugin other than the built-in sora can run its tasks on an
// OpenAI channel. Content moderation knows only Sora's request, whose text is
// its string prompt; another plugin may send text from any field. On a
// moderated OpenAI channel such a plugin's submissions are therefore rejected
// as unsupported, through the plugin's own route and through a host protocol
// alike, before anything is priced or sent and without a retry on another
// channel. A channel without moderation serves the plugin as before, and Sora
// is still checked and submitted.
func TestModeratedOpenAIChannelRejectsOtherTaskPlugins(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.Task{}))
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	logConsume, batchUpdate, retryTimes := common.LogConsumeEnabled, common.BatchUpdateEnabled, common.RetryTimes
	savedPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 5*time.Millisecond)
		common.LogConsumeEnabled, common.BatchUpdateEnabled, common.RetryTimes = logConsume, batchUpdate, retryTimes
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	common.LogConsumeEnabled, common.BatchUpdateEnabled, common.RetryTimes = true, false, 1
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))

	type call struct{ path, authorization, body string }
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.URL.Path, r.Header.Get("Authorization"), string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/moderations":
			_, _ = io.WriteString(w, `{"id":"modr-task","model":"omni-moderation-latest","results":[{"flagged":false,"categories":{"sexual":false},"category_scores":{"sexual":0.0001},"category_applied_input_types":{"sexual":["text"]}}]}`)
		case "/other/submit":
			_, _ = io.WriteString(w, `{"id":"up-other-1"}`)
		case "/v1/videos":
			_, _ = io.WriteString(w, `{"id":"video-sora-1","object":"video","status":"queued"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
		}
	}))
	t.Cleanup(server.Close)
	callsTo := func(path string) []call {
		mu.Lock()
		defer mu.Unlock()
		var matched []call
		for _, c := range calls {
			if c.path == path {
				matched = append(matched, c)
			}
		}
		return matched
	}

	// A community task plugin for OpenAI-type channels whose vendor body
	// carries the user's text in "text". Before rc.52 its registration
	// failed next to the built-in sora: "channelType 1 conflicts".
	registerRelayPlugin(t, `export const meta = {apiVersion: 1, key: "mod-other-video", name: "Other video", version: "1.0.0", author: {name: "Test"},
  channelTypes: [1], models: ["mod-other-video-1", "mod-other-video-2"], fetchMode: "per_task", protocols: ["openai_video"]};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl + "/other/submit", method: "POST", headers: {Authorization: "Bearer " + ctx.apiKey}, body: {model: ctx.requestBody.model, text: ctx.requestBody.text}};
}
export function parseSubmitResponse() { return {taskId: "up-other-1"}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/other/query"}; }
export function parseTaskResult() { return {status: "IN_PROGRESS"}; }
export function listArtifacts() { return []; }
export function buildContentRequest() { throw new Error("artifact_not_found"); }
export const protocols = {openai_video: {
  decodeRequest: function(ctx) { return {kind: "submit", model: ctx.model, requestBody: Object.assign({}, ctx.body.value, {model: ctx.model})}; },
  render: function(ctx, task) { return {id: task.task_id, status: "queued"}; }
}};`)
	var declaring []string
	for _, plugin := range jsplugin.DefaultRegistry.PluginsByChannelType(constant.ChannelTypeOpenAI) {
		declaring = append(declaring, plugin.Meta.Key)
	}
	require.ElementsMatch(t, []string{"mod-other-video", "sora"}, declaring)

	user := model.User{Id: 6600000 + int(relayRouterTestUsers.Add(1)), Username: "mod-other-task", Status: common.UserStatusEnabled, Group: "default", Quota: 10_000_000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "modothertaskkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	const moderated = `{"moderation_enabled":true,"moderation_rules":[{"type":"keyword","pattern":"Forbidden-Word-X"}]}`
	create := func(name, key, models, settings string, priority int64) {
		channel := model.Channel{Name: name, Key: key, Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: models,
			BaseURL: common.GetPointer(server.URL), OtherSettings: settings, Priority: &priority, AutoBan: common.GetPointer(0)}
		require.NoError(t, model.DB.Create(&channel).Error)
		for _, modelName := range strings.Split(models, ",") {
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: modelName, Group: "default", Enabled: true, Priority: channel.Priority}).Error)
		}
	}
	// The moderated channel comes first; a rejection must not fall through
	// to the unmoderated one behind it.
	create("mod-other-moderated", "sk-other-moderated", "mod-other-video-1,sora-2", moderated, 10)
	create("mod-other-behind", "sk-other-behind", "mod-other-video-1,sora-2", "", 0)
	create("mod-other-plain", "sk-other-plain", "mod-other-video-2", "", 0)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"mod-other-video-1":0.01,"mod-other-video-2":0.01,"sora-2":0.01}`))

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetRelayRouter(engine)
	SetVideoRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	SetTaskRouter(engine)
	post := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer modothertaskkey")
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	quota := func() int {
		value, err := model.GetUserQuota(user.Id, true)
		require.NoError(t, err)
		return value
	}
	countTasks := func() int64 {
		var tasks int64
		require.NoError(t, model.DB.Model(&model.Task{}).Count(&tasks).Error)
		return tasks
	}

	quotaBefore, tasksBefore := quota(), countTasks()
	for _, tc := range []struct{ name, path, body string }{
		{"text outside the prompt on the plugin's route", "/v1/tasks/mod-other-video", `{"model":"mod-other-video-1","text":"Forbidden-Word-X video"}`},
		{"a clean prompt does not vouch for the other fields", "/v1/tasks/mod-other-video", `{"model":"mod-other-video-1","prompt":"a calm sea","text":"Forbidden-Word-X video"}`},
		{"the plugin's host protocol", "/v1/videos", `{"model":"mod-other-video-1","text":"Forbidden-Word-X video"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := post(tc.path, tc.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "content_moderation_unsupported")
			assert.Empty(t, callsTo("/other/submit"), "nothing was submitted, on any channel")
			assert.Empty(t, callsTo("/v1/moderations"))

			var rows []*model.Log
			require.NoError(t, model.LOG_DB.Where("request_id = ?", recorder.Header().Get(common.RequestIdKey)).Find(&rows).Error)
			require.Len(t, rows, 1, "one moderation row, no consume or error row")
			assert.Equal(t, model.LogTypeModeration, rows[0].Type)
			other, err := common.StrToMap(rows[0].Other)
			require.NoError(t, err)
			adminInfo, _ := other["admin_info"].(map[string]any)
			audit, _ := adminInfo["moderation"].(map[string]any)
			assert.Equal(t, "unsupported", audit["source"], other)
			assert.Equal(t, "request_type", audit["detail"], other)
		})
	}
	assert.Equal(t, tasksBefore, countTasks(), "no task row")
	assert.Eventually(t, func() bool { return gopool.WorkerCount() == 0 && quota() == quotaBefore }, 5*time.Second, 10*time.Millisecond, "nothing charged")

	t.Run("a channel without moderation serves the plugin", func(t *testing.T) {
		recorder := post("/v1/tasks/mod-other-video", `{"model":"mod-other-video-2","text":"a calm sea"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		submitted := callsTo("/other/submit")
		require.Len(t, submitted, 1)
		assert.Equal(t, "Bearer sk-other-plain", submitted[0].authorization)
		assert.Contains(t, submitted[0].body, "a calm sea")
	})

	t.Run("Sora on the moderated channel is still checked and submitted", func(t *testing.T) {
		recorder := post("/v1/videos", `{"model":"sora-2","prompt":"Forbidden-Word-X harbour","seconds":"4","size":"720x1280"}`)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "content_moderation_flagged")
		assert.Empty(t, callsTo("/v1/videos"))

		recorder = post("/v1/videos", `{"model":"sora-2","prompt":"a quiet harbour at dawn","seconds":"4","size":"720x1280"}`)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		moderations := callsTo("/v1/moderations")
		require.Len(t, moderations, 1)
		assert.Contains(t, moderations[0].body, "a quiet harbour at dawn")
		submitted := callsTo("/v1/videos")
		require.Len(t, submitted, 1)
		assert.Equal(t, "Bearer sk-other-moderated", submitted[0].authorization)
	})
}
