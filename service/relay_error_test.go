package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestShouldRetryRelayErrorHonorsChannelPinOnChannelError(t *testing.T) {
	err := types.NewError(errors.New("channel failed"), types.ErrorCodeChannelNoAvailableKey)
	for _, test := range []struct {
		name      string
		pin       *dto.ChannelPin
		wantRetry bool
	}{
		{name: "unrestricted channel error", wantRetry: true},
		{
			name: "single attempt pin suppresses channel error retry",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt,
			},
			wantRetry: false,
		},
		{
			name: "origin task pin permits retry on the same channel",
			pin: &dto.ChannelPin{
				ChannelId: 1, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel,
			},
			wantRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.pin != nil {
				GetChannelConstraints(c).AddPin(*test.pin)
			}
			assert.Equal(t, test.wantRetry, ShouldRetryRelayError(c, err, 1))
		})
	}
}

func TestProcessChannelErrorMasksDisableReasonAndNotification(t *testing.T) {
	previousDB, previousType := model.DB, common.MainDatabaseType()
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousAutoDisable, previousErrorLog := common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	previousNotifyLimit := constant.NotifyLimitCount
	previousClient, previousWorker := httpClient, system_setting.WorkerUrl
	fetch := system_setting.GetFetchSetting()
	previousFetch := *fetch
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = previousAutoDisable, previousErrorLog
		constant.NotifyLimitCount = previousNotifyLimit
		httpClient, system_setting.WorkerUrl = previousClient, previousWorker
		*fetch = previousFetch
	})
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = true, false
	constant.NotifyLimitCount = 10
	channel := &model.Channel{Name: "relay-review", Key: "fixture-key", Type: 1, Status: common.ChannelStatusEnabled, Group: "default", Models: "test-model"}
	require.NoError(t, channel.Insert())
	notifications := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		notifications <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	httpClient, system_setting.WorkerUrl = server.Client(), ""
	fetch.EnableSSRFProtection = false
	settings, err := common.Marshal(kitdto.UserSetting{NotifyType: kitdto.NotifyTypeWebhook, WebhookUrl: server.URL})
	require.NoError(t, err)
	root := &model.User{Username: "notification-test-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Setting: string(settings)}
	require.NoError(t, database.Create(root).Error)
	notifyKey := fmt.Sprintf("%d:%s:%s", root.Id, formatNotifyType(channel.Id, common.ChannelStatusAutoDisabled), time.Now().Format("2006010215"))
	notifyLimitStore.Delete(notifyKey)
	t.Cleanup(func() { notifyLimitStore.Delete(notifyKey) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://private.example.com")
	apiErr := types.NewErrorWithStatusCode(errors.New("upstream https://private.example.com/path?token=review-token api_key:review-secret"), types.ErrorCodeChannelNoAvailableKey, http.StatusUnauthorized)
	ProcessChannelError(c, types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, AutoBan: true}, apiErr, nil, nil)
	var notification WebhookPayload
	select {
	case payload := <-notifications:
		require.NoError(t, common.Unmarshal(payload, &notification))
	case <-time.After(5 * time.Second):
		t.Fatal("automatic channel-disable notification was not delivered")
	}
	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.Status)
	wantReason := "status_code=401, upstream https://***.com/***?token=*** api_key:***"
	assert.Equal(t, wantReason, loaded.GetOtherInfo()["status_reason"])
	assert.Contains(t, notification.Content, wantReason)
	assert.NotContains(t, notification.Content, "review-token")
	assert.NotContains(t, notification.Content, "review-secret")
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

// Only addresses the admin configured are masked: a channel on the base URL
// new-api presets reaches a public official endpoint, and so do adaptors that
// build official endpoints themselves.
func TestUpstreamAddressesMaskOnlyConfiguredAddresses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channelType int
		baseURL     string
		origin      string
		proxyEnv    string
		message     string
		want        string
	}{
		{
			name:        "preset base URL stays readable",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			origin:      "https://api.anthropic.com",
			message:     "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
			want:        "request to https://api.anthropic.com/v1/messages failed, see https://platform.claude.com/docs/en/build-with-claude/task-budgets",
		},
		{
			name:        "official endpoint built by the adaptor stays readable",
			channelType: constant.ChannelTypeVertexAi,
			origin:      "https://us-central1-aiplatform.googleapis.com",
			message:     "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
			want:        "request to https://us-central1-aiplatform.googleapis.com/v1/projects failed",
		},
		{
			name:        "configured base URL is masked",
			channelType: constant.ChannelTypeOpenAI,
			baseURL:     "https://relay.private.example",
			origin:      "https://relay.private.example",
			message:     "request to https://relay.private.example/v1/chat/completions failed",
			want:        "request to https://***.example/***/***/*** failed",
		},
		{
			name:        "absolute Advanced Custom route is masked",
			channelType: constant.ChannelTypeAdvancedCustom,
			origin:      "https://route.private.example",
			message:     "dial tcp: lookup route.private.example: no such host",
			want:        "dial tcp: lookup ***.***.example: no such host",
		},
		{
			name:        "environment proxy is masked on a preset channel",
			channelType: constant.ChannelTypeAnthropic,
			baseURL:     "https://api.anthropic.com",
			proxyEnv:    "http://egress.private.invalid:8080",
			message:     "proxyconnect tcp: dial tcp: lookup egress.private.invalid: no such host",
			want:        "proxyconnect tcp: dial tcp: lookup ***.***.invalid: no such host",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", tc.proxyEnv)
			t.Setenv("HTTP_PROXY", "")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, tc.baseURL)
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, tc.origin)
			apiErr := types.NewErrorWithStatusCode(errors.New(tc.message), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

			assert.Equal(t, tc.want, apiErr.ToOpenAIError(common.UpstreamAddresses(c)...).Message)
		})
	}
}

func TestDecideRelayRetryReasons(t *testing.T) {
	upstream := func(status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, status)
	}
	for _, tc := range []struct {
		name    string
		err     *types.NewAPIError
		retries int
		setup   func(*gin.Context)
		want    PolicyDecision
	}{
		{name: "retry status matched", err: upstream(http.StatusTooManyRequests), retries: 1, want: PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}},
		{name: "status outside retry rules", err: upstream(http.StatusBadRequest), retries: 1, want: PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}},
		{name: "attempt budget exhausted", err: upstream(http.StatusTooManyRequests), retries: 0, want: PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}},
		{name: "always skipped status", err: upstream(http.StatusGatewayTimeout), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "success status never retries", err: upstream(http.StatusOK), retries: 1, want: PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}},
		{name: "skip retry error", err: types.NewErrorWithStatusCode(errors.New("local"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry()), retries: 1, want: PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}},
		{name: "channel error retries without budget", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 0, want: PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}},
		{name: "single attempt pin", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
		}, want: PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}},
		{name: "strict session", err: upstream(http.StatusTooManyRequests), retries: 1, setup: func(c *gin.Context) {
			c.Set(ginKeyChannelAffinitySkipRetry, true)
			RequestPolicy(c).SessionModeSource = "global"
		}, want: PolicyDecision{Action: "stop", Reason: "strict_session", Source: "global"}},
		{name: "nil error", retries: 1, want: PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}},
		{name: "client gone stops even channel errors", err: types.NewError(errors.New("no key"), types.ErrorCodeChannelNoAvailableKey), retries: 1, setup: func(c *gin.Context) {
			c.Request = cancelledClientRequest()
		}, want: PolicyDecision{Action: "stop", Reason: "client_gone", Source: "client"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.setup != nil {
				tc.setup(c)
			}
			decision := DecideRelayRetry(c, tc.err, tc.retries)
			assert.Equal(t, tc.want, decision)
			assert.Equal(t, tc.want.Action == "retry", ShouldRetryRelayError(c, tc.err, tc.retries))
		})
	}
}

// cancelledClientRequest is a relay request whose client already disconnected.
func cancelledClientRequest() *http.Request {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
}

// The upstream request is cancelled with its client, so the failure that
// follows says nothing about the channel: it requests no disable, even for a
// status that otherwise would.
func TestRecordPolicyFailureForClientGoneLeavesHealthUnchanged(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = cancelledClientRequest()
	c.Set("auto_ban", true)
	state := RequestPolicy(c)
	state.BeginAttempt(&model.Channel{Id: 7}, "default")
	apiErr := types.NewOpenAIError(errors.New("invalid credential"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	require.True(t, ShouldDisableChannel(apiErr), "with the client present this status disables the channel")

	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 1))

	events := state.Events()
	require.Len(t, events, 3)
	assert.Equal(t, PolicyDecision{Action: "failure", Reason: "client_gone", Source: "client"}, events[1].Decision)
	assert.Equal(t, "client", events[1].ErrorSource)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "client_gone", Source: "client"}, events[2].Decision)
	assert.Equal(t, "unchanged", events[2].Health)
}

// A NovelAI channel's own queue limit (every key generating with a full
// queue, the longest wait over, or the queues stopped for shutdown) never
// disables the channel or a key, even with 429 among the automatic disable
// codes; NovelAI's own 429 still does.
func TestShouldDisableChannelSparesTheNovelAIQueueLimit(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	previousCodes := operation_setting.AutomaticDisableStatusCodesToString()
	common.AutomaticDisableChannelEnabled = true
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("401,429,503"))
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = previousAutoDisable
		require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString(previousCodes))
	})

	queueFull := types.NewErrorWithStatusCode(errors.New("every key is busy"), types.ErrorCodeNovelAIKeyBusy, http.StatusTooManyRequests)
	stopped := types.NewErrorWithStatusCode(errors.New("restarting"), types.ErrorCodeNovelAIKeyBusy, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	upstream := types.NewOpenAIError(errors.New("Concurrent generation is locked"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)

	assert.False(t, ShouldDisableChannel(queueFull))
	assert.False(t, ShouldDisableChannel(types.NewOpenAIError(queueFull, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)), "the relay keeps the queue's error when it wraps it")
	assert.False(t, ShouldDisableChannel(stopped))
	assert.True(t, ShouldDisableChannel(upstream), "NovelAI's own 429 is the channel's answer")
}

func TestRequestPolicyEventsReachLogAdminInfo(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutoDisable })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("auto_ban", true)
	c.Set("channel_id", 7)
	state := RequestPolicy(c)
	state.BeginAttempt(&model.Channel{Id: 7}, "default")
	apiErr := types.NewOpenAIError(errors.New("invalid credential"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))

	failed := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, failed)
	events, ok := failed.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a failed relay exposes its decision events to administrators")
	require.Len(t, events, 3)
	assert.Equal(t, PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}, events[0].Decision)
	assert.Equal(t, "default", events[0].Group)
	assert.Equal(t, PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: "upstream"}, events[1].Decision)
	assert.Equal(t, http.StatusUnauthorized, events[1].Status)
	assert.Equal(t, PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}, events[2].Decision)
	assert.Equal(t, "channel_disable_requested", events[2].Health, "the health entry follows the automatic disable rules")
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	RecordPolicyFailure(c, 7, apiErr, DecideRelayRetry(c, apiErr, 0))
	assert.Equal(t, "key_disable_requested", state.Events()[4].Health)

	state.BeginAttempt(&model.Channel{Id: 8}, "default")
	c.Set("channel_id", 8)
	MarkRequestPolicySuccess(c, nil)
	MarkRequestPolicySuccess(c, nil)
	succeeded := model.NewLogOther()
	AppendRelayLogAdminInfo(c, nil, succeeded)
	events, ok = succeeded.Snapshot()["admin_info"].(map[string]any)["request_policy"].([]PolicyEvent)
	require.True(t, ok, "a successful relay exposes its decision events to administrators")
	require.Len(t, events, 7, "the outcome is recorded once")
	assert.Equal(t, PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}, events[6].Decision)
	assert.Equal(t, 8, events[6].ChannelID)
	assert.Equal(t, 2, events[6].Attempt)
	assert.True(t, state.Successful)

	untouched, _ := gin.CreateTestContext(httptest.NewRecorder())
	other := model.NewLogOther()
	AppendRelayLogAdminInfo(untouched, nil, other)
	assert.NotContains(t, other.Snapshot()["admin_info"], "request_policy", "requests without decisions do not carry an empty record")
}

// An upstream's answer that its own account is out of balance or quota is
// recognised by 402, by a balance code or type, or by a balance phrase of the
// upstreams new-api meets, in any case and in Chinese, unless it is a rate
// limit (Gemini's free tier says "exceeded your current quota" of per-minute
// limits). new-api's own errors and channel errors never count, whatever
// they say.
func TestIsUpstreamQuotaExhaustedError(t *testing.T) {
	upstream := func(status int, code any, errorType, message string) *types.NewAPIError {
		return types.WithOpenAIError(types.OpenAIError{Message: message, Type: errorType, Code: code}, status)
	}
	for _, tc := range []struct {
		name string
		err  *types.NewAPIError
		want bool
	}{
		{"new-api pre-consume (zh-CN)", upstream(http.StatusForbidden, "pre_consume_token_quota_failed", "new_api_error", "预扣费额度失败, 用户剩余额度: ＄0.120000, 需要预扣费额度: ＄0.280000 (request id: 20261104012629802206256826)"), true},
		{"new-api pre-consume message alone", upstream(http.StatusForbidden, nil, "", "预扣费额度失败, 用户剩余额度: ＄0.120000"), true},
		{"new-api pre-consume (en)", upstream(http.StatusForbidden, nil, "", "Not enough quota to pre-consume for this request: $0.12 remaining, $0.28 required"), true},
		{"new-api pre-consume (zh-TW)", upstream(http.StatusForbidden, nil, "", "預扣費額度失敗，使用者剩餘額度：＄0.12"), true},
		{"new-api user quota code", upstream(http.StatusForbidden, "insufficient_user_quota", "new_api_error", "remaining: $0"), true},
		{"new-api user quota (en)", upstream(http.StatusForbidden, nil, "", "Insufficient user quota, remaining quota: $0.000000"), true},
		{"new-api subscription quota (zh-CN)", upstream(http.StatusForbidden, nil, "", "订阅额度不足或未配置订阅: no active subscription"), true},
		{"new-api subscription quota (en)", upstream(http.StatusForbidden, nil, "", "Subscription quota is insufficient or no subscription is configured: none"), true},
		{"one-api token exhausted", upstream(http.StatusForbidden, nil, "", "该令牌额度已用尽 TokenStatusExhausted[sk-abc]"), true},
		{"one-api token quota", upstream(http.StatusForbidden, nil, "", "令牌额度不足"), true},
		{"one-api user quota", upstream(http.StatusForbidden, nil, "", "user quota is not enough"), true},
		{"new-api token quota", upstream(http.StatusForbidden, nil, "", "token quota is not enough, token remain quota: $0.01, need quota: $0.28"), true},
		{"OpenAI insufficient_quota under 429", upstream(http.StatusTooManyRequests, "insufficient_quota", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details. For more information on this error, read the docs: https://platform.openai.com/docs/guides/error-codes/api-errors."), true},
		{"OpenAI billing not active", upstream(http.StatusTooManyRequests, "billing_not_active", "", "Your account is not active, please check your billing details on our website."), true},
		{"Anthropic credit balance", types.WithClaudeError(types.ClaudeError{Type: "invalid_request_error", Message: "Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}, http.StatusBadRequest), true},
		{"DeepSeek 402", upstream(http.StatusPaymentRequired, "invalid_request_error", "", "Insufficient Balance"), true},
		{"402 alone", upstream(http.StatusPaymentRequired, nil, "", "Payment Required"), true},
		{"OpenRouter credits", upstream(http.StatusForbidden, nil, "", "Insufficient credits. Add more using https://openrouter.ai/settings/credits"), true},
		{"SiliconFlow balance", upstream(http.StatusForbidden, float64(30001), "", "Sorry, your account balance is insufficient"), true},
		{"Zhipu arrears under 429", upstream(http.StatusTooManyRequests, "1113", "", "您的账户已欠费，请充值后重试。"), true},
		{"Volcengine overdue code", upstream(http.StatusForbidden, "AccountOverdueError", "Forbidden", "The request failed because your account has an overdue balance."), true},
		{"Moonshot quota type", upstream(http.StatusTooManyRequests, nil, "exceeded_current_quota_error", "Your account org-abc <ak-abc> is suspended, please check your plan and billing details"), true},
		{"DashScope arrearage code", upstream(http.StatusBadRequest, "Arrearage", "", "Access denied, please make sure your account is in good standing."), true},
		{"insufficient_balance code", upstream(http.StatusBadRequest, "insufficient_balance", "", "balance check failed"), true},
		{"generic balance (zh-CN)", upstream(http.StatusBadRequest, nil, "", "账户余额不足"), true},
		{"generic balance (zh-TW)", upstream(http.StatusBadRequest, nil, "", "帳戶餘額不足"), true},
		{"generic quota used up", upstream(http.StatusBadRequest, nil, "", "本月额度已用尽"), true},
		{"upper case phrase and code", upstream(http.StatusBadRequest, "INSUFFICIENT_QUOTA", "", "INSUFFICIENT BALANCE"), true},
		{"Gemini free tier rate limit", upstream(http.StatusTooManyRequests, float64(429), "", "You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/usage?tab=rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 10, model: gemini-2.5-flash\nPlease retry in 43.659002305s."), false},
		{"RESOURCE_EXHAUSTED type", upstream(http.StatusTooManyRequests, nil, "RESOURCE_EXHAUSTED", "You exceeded your current quota"), false},
		{"rate limit wording", upstream(http.StatusTooManyRequests, "insufficient_quota", "", "Rate limit reached: exceeded your current quota"), false},
		{"RPM limit", upstream(http.StatusTooManyRequests, nil, "", "You exceeded your current quota: 3 RPM"), false},
		{"tokens per minute", upstream(http.StatusTooManyRequests, nil, "", "Insufficient balance of tokens per min for this model"), false},
		{"Chinese rate limit", upstream(http.StatusTooManyRequests, nil, "", "请求过于频繁，额度不足"), false},
		{"Chinese frequency limit", upstream(http.StatusTooManyRequests, nil, "", "调用频率超限，余额不足"), false},
		{"quota exceeded alone", upstream(http.StatusForbidden, nil, "", "quota exceeded"), false},
		{"unrelated upstream error", upstream(http.StatusInternalServerError, "server_error", "", "The server had an error while processing your request"), false},
		{"own pre-consume failure", types.NewErrorWithStatusCode(errors.New("预扣费额度失败, 用户剩余额度: ＄0.120000, 需要预扣费额度: ＄0.280000"), types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), false},
		{"own user quota", types.NewErrorWithStatusCode(errors.New("用户额度不足, 剩余额度: ＄0.000000"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), false},
		{"own error under 402", types.NewErrorWithStatusCode(errors.New("insufficient balance"), types.ErrorCodeInsufficientUserQuota, http.StatusPaymentRequired), false},
		{"channel error", upstream(http.StatusPaymentRequired, string(types.ErrorCodeChannelInvalidKey), "", "余额不足"), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsUpstreamQuotaExhaustedError(tc.err))
		})
	}
}

// Upstream answers are told apart as RelayErrorHandler reads them off the
// wire, with what its error leaves out: Google's status of a rate limit and a
// code outside the "error" object, as DashScope's native endpoints send it.
// The client still gets the upstream's text as before.
func TestIsUpstreamQuotaExhaustedErrorReadsTheUpstreamBody(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantText string
		status               int
		want                 bool
	}{
		{name: "new-api pre-consume", status: http.StatusForbidden, body: `{"error":{"message":"预扣费额度失败, 用户剩余额度: ＄0.120000 (request id: X)","type":"new_api_error","code":"pre_consume_token_quota_failed"}}`, wantText: "预扣费额度失败, 用户剩余额度: ＄0.120000 (request id: X)", want: true},
		// An upstream that runs this switch itself answers with its message.
		{name: "upstream hiding its own upstream's balance error", status: http.StatusForbidden, body: `{"error":{"message":"上游tokens数量不足 (request id: X)","type":"new_api_error","code":"service_quota_exhausted"}}`, wantText: "上游tokens数量不足 (request id: X)", want: true},
		{name: "upstream hiding it behind a custom message", status: http.StatusTooManyRequests, body: `{"error":{"message":"服务繁忙 (request id: X)","type":"new_api_error","code":"service_quota_exhausted"}}`, wantText: "服务繁忙 (request id: X)", want: true},
		{name: "OpenAI insufficient_quota", status: http.StatusTooManyRequests, body: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`, wantText: "You exceeded your current quota, please check your plan and billing details.", want: true},
		{name: "Gemini free tier rate limit without its links", status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED"}}`, wantText: "You exceeded your current quota, please check your plan and billing details.", want: false},
		{name: "DashScope native arrearage", status: http.StatusBadRequest, body: `{"code":"Arrearage","message":"Access denied, please make sure your account is in good standing.","request_id":"b8f1"}`, wantText: "Access denied, please make sure your account is in good standing.", want: true},
		{name: "DashScope compatible-mode arrearage", status: http.StatusBadRequest, body: `{"error":{"code":"Arrearage","message":"Access denied, please make sure your account is in good standing.","type":"Arrearage"},"request_id":"b8f1"}`, wantText: "Access denied, please make sure your account is in good standing.", want: true},
		{name: "DashScope native unrelated failure", status: http.StatusBadRequest, body: `{"code":"InvalidParameter","message":"Input is too long.","request_id":"b8f1"}`, wantText: "Input is too long.", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}

			err := RelayErrorHandler(context.Background(), response, false)

			assert.Equal(t, tc.want, IsUpstreamQuotaExhaustedError(err))
			assert.Equal(t, tc.wantText, err.Error())
			assert.Equal(t, tc.status, err.StatusCode)
		})
	}
}

// A task submission's upstream answer reaches the host as a fail_to_fetch_task
// error with the vendor's body as its message; its code, type, status and
// message count wherever the body puts them. Local rejections and the user's
// own pre-consume failure (whose code the host copies in) never count.
func TestIsUpstreamQuotaExhaustedTaskError(t *testing.T) {
	upstream := func(status int, body string) *dto.TaskError {
		return &dto.TaskError{Code: "fail_to_fetch_task", Message: body, StatusCode: status, Error: errors.New(body)}
	}
	for _, tc := range []struct {
		name    string
		taskErr *dto.TaskError
		want    bool
	}{
		{"new-api task body", upstream(http.StatusForbidden, `{"code":"pre_consume_token_quota_failed","message":"预扣费额度失败, 用户剩余额度: ＄0.120000, 需要预扣费额度: ＄0.280000","data":null}`), true},
		{"new-api relay body", upstream(http.StatusForbidden, `{"error":{"message":"预扣费额度失败 (request id: X)","type":"new_api_error","code":"pre_consume_token_quota_failed"}}`), true},
		{"escaped Chinese message", upstream(http.StatusBadRequest, `{"error":{"message":"账户余额不足","code":"x"}}`), true},
		{"Volcengine overdue code", upstream(http.StatusForbidden, `{"error":{"code":"AccountOverdueError","message":"The request failed because your account has an overdue balance."}}`), true},
		{"numeric code with arrears", upstream(http.StatusBadRequest, `{"code":1102,"message":"账户欠费"}`), true},
		{"402", upstream(http.StatusPaymentRequired, `payment required`), true},
		{"Midjourney proxy description", upstream(http.StatusBadRequest, `{"code":4,"description":"\u8d26\u6237\u4f59\u989d\u4e0d\u8db3","result":""}`), true},
		// An upstream that runs this switch itself, in a presenter's shape.
		{"upstream hiding its own upstream's balance error", upstream(http.StatusForbidden, `{"code":5001,"message":"上游tokens数量不足 (request id: X)"}`), true},
		{"Gemini rate limit", upstream(http.StatusTooManyRequests, `{"error":{"code":429,"message":"You exceeded your current quota","status":"RESOURCE_EXHAUSTED"}}`), false},
		{"unrelated failure", upstream(http.StatusBadRequest, `{"code":"invalid_prompt","message":"prompt rejected"}`), false},
		{"local rejection", &dto.TaskError{Code: "plugin_request_invalid", Message: "余额不足", StatusCode: http.StatusPaymentRequired, LocalError: true}, false},
		{"own adjusted-cost reservation", &dto.TaskError{Code: string(types.ErrorCodeInsufficientUserQuota), Message: "insufficient quota for adjusted task cost", StatusCode: http.StatusForbidden, LocalError: true}, false},
		{"own pre-consume failure", TaskErrorFromAPIError(types.NewErrorWithStatusCode(errors.New("预扣费额度失败, 用户剩余额度: ＄0.120000"), types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden)), false},
		{"own user quota", TaskErrorFromAPIError(types.NewErrorWithStatusCode(errors.New("用户额度不足, 剩余额度: ＄0"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden)), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsUpstreamQuotaExhaustedTaskError(tc.taskErr))
		})
	}
}

// Only a channel with the switch on hides an upstream balance error, behind
// its message and status or the defaults; with the switch off, unsaved or
// saved off with a message, nothing is replaced.
func TestMaskUpstreamQuotaErrorFollowsTheChannelSetting(t *testing.T) {
	balance := types.WithOpenAIError(types.OpenAIError{Message: "预扣费额度失败, 用户剩余额度: ＄0.120000 (request id: X)", Type: "new_api_error", Code: "pre_consume_token_quota_failed"}, http.StatusForbidden)
	balanceTask := &dto.TaskError{Code: "fail_to_fetch_task", Message: `{"code":"pre_consume_token_quota_failed","message":"预扣费额度失败"}`, StatusCode: http.StatusForbidden}
	for _, tc := range []struct {
		name        string
		settings    *kitdto.ChannelOtherSettings
		wantMessage string
		wantStatus  int
	}{
		{name: "no channel settings"},
		{name: "switch unsaved", settings: &kitdto.ChannelOtherSettings{}},
		{name: "switch off with a saved message", settings: &kitdto.ChannelOtherSettings{UpstreamQuotaErrorMessage: "余额维护中", UpstreamQuotaErrorStatusCode: 429}},
		{name: "switch on", settings: &kitdto.ChannelOtherSettings{UpstreamQuotaErrorMaskEnabled: true}, wantMessage: "上游tokens数量不足", wantStatus: http.StatusForbidden},
		{name: "switch on with a custom message and status", settings: &kitdto.ChannelOtherSettings{UpstreamQuotaErrorMaskEnabled: true, UpstreamQuotaErrorMessage: " 余额维护中，请稍后再试 ", UpstreamQuotaErrorStatusCode: 429}, wantMessage: "余额维护中，请稍后再试", wantStatus: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.settings != nil {
				common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, *tc.settings)
			}
			for _, masked := range []*types.NewAPIError{MaskUpstreamQuotaError(c, balance), MaskUpstreamQuotaTaskError(c, balanceTask)} {
				if tc.wantMessage == "" {
					assert.Nil(t, masked)
					continue
				}
				require.NotNil(t, masked)
				assert.Equal(t, tc.wantStatus, masked.StatusCode)
				assert.Equal(t, types.ErrorTypeNewAPIError, masked.GetErrorType())
				assert.Equal(t, types.ErrorCodeServiceQuotaExhausted, masked.GetErrorCode())
				assert.Equal(t, types.OpenAIError{Message: tc.wantMessage, Type: "new_api_error", Code: types.ErrorCodeServiceQuotaExhausted}, masked.ToOpenAIError())
				assert.Equal(t, types.ClaudeError{Message: tc.wantMessage, Type: "new_api_error"}, masked.ToClaudeError())
			}
			on := tc.settings != nil && tc.settings.UpstreamQuotaErrorMaskEnabled
			assert.Nil(t, MaskUpstreamQuotaError(c, types.NewErrorWithStatusCode(errors.New("预扣费额度失败"), types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden)), "the user's own balance shows, switch on: %v", on)
		})
	}
}

// geminiFreeTierQuota is Google's free tier 429 for input tokens per minute.
const geminiFreeTierQuota = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 250000, model: gemini-3.6-flash, description: Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 49.477199395s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_input_token_count","quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaDimensions":{"location":"global","model":"gemini-3.6-flash"},"quotaValue":"250000"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"49s"}]}}`

// relayErrorOf is the error the relay makes of an upstream answer.
func relayErrorOf(t *testing.T, status int, body string, statusCodeMapping string) *types.NewAPIError {
	t.Helper()
	err := RelayErrorHandler(context.Background(), &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, false)
	ResetStatusCode(err, statusCodeMapping)
	return err
}

// A Gemini channel's free key limits, and Google's free tier rate limits
// once every key a request tried answered one, never disable a key, though
// Google's text matches the default "You exceeded your current quota"
// keyword; the same answer without that code still does.
func TestShouldDisableChannelSparesGeminiFreeKeys(t *testing.T) {
	previousAutoDisable := common.AutomaticDisableChannelEnabled
	previousKeywords := operation_setting.AutomaticDisableKeywordsToString()
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableKeywordsFromString("You exceeded your current quota")
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = previousAutoDisable
		operation_setting.AutomaticDisableKeywordsFromString(previousKeywords)
	})

	local := types.NewErrorWithStatusCode(errors.New("no key of the channel can serve gemini-3.6-flash today"), types.ErrorCodeGeminiFreeKeyBusy, http.StatusTooManyRequests)
	upstream := relayErrorOf(t, http.StatusTooManyRequests, geminiFreeTierQuota, "")
	assert.True(t, ShouldDisableChannel(upstream), "Google's text matches the default keyword")
	types.ErrOptionWithErrorCode(types.ErrorCodeGeminiFreeKeyBusy)(upstream)

	assert.False(t, ShouldDisableChannel(local))
	assert.False(t, ShouldDisableChannel(types.NewOpenAIError(local, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)), "the relay keeps the error when it wraps it")
	assert.False(t, ShouldDisableChannel(upstream))
	assert.False(t, ShouldDisableChannel(types.NewOpenAIError(upstream, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)))
}

// On a Gemini channel in free key mode the user gets Google's plain rate
// limit, exactly as the relay passes Google's own on (status code mapping
// included), in place of the channel's key limits always and, unless the
// channel reveals them, of Google's free tier errors and any other 429 of
// Google. Nothing else is replaced, and nothing is on any other channel.
func TestDisguiseGeminiFreeKeyError(t *testing.T) {
	const plainGoogleRateLimit = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`
	busy := func() *types.NewAPIError {
		return types.NewOpenAIError(types.NewErrorWithStatusCode(errors.New("Free key mode: no key of the channel has room now for 300000 input tokens of gemini-3.6-flash (3 keys, 3 at the input tokens per minute)"), types.ErrorCodeGeminiFreeKeyBusy, http.StatusTooManyRequests), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	exhausted := func() *types.NewAPIError {
		err := relayErrorOf(t, http.StatusTooManyRequests, geminiFreeTierQuota, "")
		types.ErrOptionWithErrorCode(types.ErrorCodeGeminiFreeKeyBusy)(err)
		return err
	}
	freeTier400 := func() *types.NewAPIError {
		return relayErrorOf(t, http.StatusBadRequest, `{"error":{"code":400,"message":"Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio.","status":"FAILED_PRECONDITION"}}`, "")
	}
	paid429 := func() *types.NewAPIError {
		return relayErrorOf(t, http.StatusTooManyRequests, `{"error":{"code":429,"message":"Quota exceeded for quota metric 'Generate Content API requests per minute'","status":"RESOURCE_EXHAUSTED"}}`, "")
	}
	overloaded := func() *types.NewAPIError {
		return relayErrorOf(t, http.StatusServiceUnavailable, `{"error":{"code":503,"message":"This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.","status":"UNAVAILABLE"}}`, "")
	}
	ownQuota := func() *types.NewAPIError {
		return types.NewErrorWithStatusCode(errors.New("channel quota exhausted"), types.ErrorCode("channel_quota_exhausted"), http.StatusTooManyRequests, types.ErrOptionWithSkipRetry())
	}
	const disguised, kept = true, false
	for _, tc := range []struct {
		name        string
		channelType int
		settings    *kitdto.ChannelOtherSettings
		mapping     string
		want        map[string]bool
	}{
		{name: "free key mode", channelType: constant.ChannelTypeGemini, settings: &kitdto.ChannelOtherSettings{GeminiFreeKeyEnabled: true},
			want: map[string]bool{"busy": disguised, "exhausted": disguised, "free tier 400": disguised, "paid 429": disguised, "overloaded": kept, "own quota": kept}},
		{name: "free key mode with a status code mapping", channelType: constant.ChannelTypeGemini, settings: &kitdto.ChannelOtherSettings{GeminiFreeKeyEnabled: true}, mapping: `{"429":"503"}`,
			want: map[string]bool{"busy": disguised, "exhausted": disguised, "free tier 400": disguised, "paid 429": disguised, "overloaded": kept, "own quota": kept}},
		{name: "free tier errors revealed", channelType: constant.ChannelTypeGemini, settings: &kitdto.ChannelOtherSettings{GeminiFreeKeyEnabled: true, GeminiFreeKeyRevealTierErrors: true},
			want: map[string]bool{"busy": disguised, "exhausted": kept, "free tier 400": kept, "paid 429": kept, "overloaded": kept, "own quota": kept}},
		{name: "free key mode off", channelType: constant.ChannelTypeGemini, settings: &kitdto.ChannelOtherSettings{GeminiFreeKeyRevealTierErrors: true},
			want: map[string]bool{"busy": kept, "exhausted": kept, "free tier 400": kept, "paid 429": kept, "overloaded": kept, "own quota": kept}},
		{name: "no channel settings", channelType: constant.ChannelTypeGemini,
			want: map[string]bool{"busy": kept, "exhausted": kept, "free tier 400": kept, "paid 429": kept, "overloaded": kept, "own quota": kept}},
		{name: "another channel type", channelType: constant.ChannelTypeVertexAi, settings: &kitdto.ChannelOtherSettings{GeminiFreeKeyEnabled: true},
			want: map[string]bool{"busy": kept, "exhausted": kept, "free tier 400": kept, "paid 429": kept, "overloaded": kept, "own quota": kept}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channelType)
			if tc.settings != nil {
				common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, *tc.settings)
			}
			c.Set("status_code_mapping", tc.mapping)
			genuine := relayErrorOf(t, http.StatusTooManyRequests, plainGoogleRateLimit, tc.mapping)
			for name, err := range map[string]*types.NewAPIError{"busy": busy(), "exhausted": exhausted(), "free tier 400": freeTier400(), "paid 429": paid429(), "overloaded": overloaded(), "own quota": ownQuota()} {
				got := DisguiseGeminiFreeKeyError(c, err)
				if !tc.want[name] {
					assert.Nil(t, got, name)
					continue
				}
				require.NotNil(t, got, name)
				// Every writer renders the error from these, and none of
				// them tells it from Google's own.
				assert.Equal(t, genuine, got, name)
				assert.Equal(t, genuine.ToOpenAIError(), got.ToOpenAIError(), name)
				assert.Equal(t, genuine.ToClaudeError(), got.ToClaudeError(), name)
				assert.Equal(t, genuine.MaskSensitiveErrorWithStatusCode(), got.MaskSensitiveErrorWithStatusCode(), name)
				openAIJSON, marshalErr := common.Marshal(got.ToOpenAIError())
				require.NoError(t, marshalErr)
				claudeJSON, marshalErr := common.Marshal(got.ToClaudeError())
				require.NoError(t, marshalErr)
				for _, leak := range []string{"free", "Free", "billing", "quota metric", "quotaId", "250000", "limit:", "aistudio", "ai.dev", "ai.google.dev", "retry in", "keys", "key mode", "room"} {
					assert.NotContains(t, string(openAIJSON)+string(claudeJSON)+got.Error(), leak, name)
				}
			}
			assert.Nil(t, DisguiseGeminiFreeKeyError(c, nil))
		})
	}
	assert.Equal(t, http.StatusServiceUnavailable, relayErrorOf(t, http.StatusTooManyRequests, plainGoogleRateLimit, `{"429":"503"}`).StatusCode, "the mapping applies")
	assert.True(t, IsGeminiFreeTierError(freeTier400()))
	assert.True(t, IsGeminiFreeTierError(exhausted()))
	assert.False(t, IsGeminiFreeTierError(paid429()))
	assert.False(t, IsGeminiFreeTierError(nil))
}
