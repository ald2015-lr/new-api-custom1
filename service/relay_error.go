package service

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// DecideRelayRetry is the single retry decision for relay attempts. The reason
// is recorded in the request policy decision events of the log details.
func DecideRelayRetry(c *gin.Context, err *types.NewAPIError, retryTimes int) PolicyDecision {
	if err == nil {
		return PolicyDecision{Action: "stop", Reason: "request_completed", Source: "system"}
	}
	// Once bytes reached the client a retry would append a second, complete
	// answer to the same body: the user sees the model restart from its
	// reasoning. WebSocket relays are excluded because the upgrade itself
	// marks the writer as written.
	if c != nil && c.Writer != nil && c.Request != nil && !c.IsWebsocket() && c.Writer.Written() {
		return PolicyDecision{Action: "stop", Reason: "response_started", Source: "system"}
	}
	// The upstream request was cancelled together with the client; another
	// channel would fail the same way and nobody would read its answer.
	if ClientGone(c) {
		return PolicyDecision{Action: "stop", Reason: "client_gone", Source: "client"}
	}
	if ShouldSkipRetryAfterChannelAffinityFailure(c) {
		source := RequestPolicy(c).SessionModeSource
		if source == "" {
			source = "session_rule"
		}
		return PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}
	}
	if GetChannelConstraints(c).SuppressesRetry() {
		return PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}
	}
	if types.IsChannelError(err) {
		return PolicyDecision{Action: "retry", Reason: "channel_error", Source: "system"}
	}
	if types.IsSkipRetryError(err) {
		return PolicyDecision{Action: "stop", Reason: "non_retryable_error", Source: "system"}
	}
	if retryTimes <= 0 {
		return PolicyDecision{Action: "stop", Reason: "attempt_budget_exhausted", Source: "global"}
	}
	code := err.StatusCode
	if code >= 200 && code < 300 {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if code < 100 || code > 599 {
		return PolicyDecision{Action: "retry", Reason: "unrecognized_status", Source: "system"}
	}
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(code) {
		return PolicyDecision{Action: "stop", Reason: "system_retry_exclusion", Source: "system"}
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return PolicyDecision{Action: "retry", Reason: "retry_status_matched", Source: "global"}
	}
	return PolicyDecision{Action: "stop", Reason: "status_not_retryable", Source: "global"}
}

// ClientGone reports that the client of this request disconnected. Relay
// requests to the upstream share its context, so a failure seen afterwards was
// caused by the client leaving, not by the channel.
func ClientGone(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

func ShouldRetryRelayError(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	return DecideRelayRetry(c, openaiErr, retryTimes).Action == "retry"
}

// ProcessChannelError disables the channel when err calls for it and records
// the failed attempt in the user's error log. userErr is what the user is told
// in place of err, such as the channel's message for an upstream balance
// error (MaskUpstreamQuotaError), or nil for err itself. The disable decision,
// its reason and the server log always use err; with userErr the error log
// shows userErr and keeps err, its upstream request id included, for
// administrators only.
func ProcessChannelError(c *gin.Context, channelError types.ChannelError, err, userErr *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if err == nil {
		return
	}
	upstreamRequestId := c.GetString(common.UpstreamRequestIdKey)
	if userErr != nil && upstreamRequestId != "" {
		// The hidden answer's request id stays out of every later log of the
		// request too, such as the consume log of a retry on a channel whose
		// upstream sends none.
		c.Set(common.UpstreamRequestIdKey, "")
	}
	upstreamURLs := common.UpstreamAddresses(c)
	logger.LogError(c, common.LogText("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.MaskSensitiveErrorWithStatusCode(upstreamURLs...))))
	if ShouldDisableChannel(err) && channelError.AutoBan {
		reason := err.MaskSensitiveErrorWithStatusCode(upstreamURLs...)
		gopool.Go(func() {
			DisableChannel(channelError, reason)
		})
	}

	recordRelayErrorLog(c, channelError.ChannelId, err, userErr, upstreamRequestId, relayInfo)
}

// RecordRelayErrorLog writes the error log of a failed relay request when
// error logs are on. channelId is 0 for a request that reached no channel.
func RecordRelayErrorLog(c *gin.Context, channelId int, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	recordRelayErrorLog(c, channelId, err, nil, c.GetString(common.UpstreamRequestIdKey), relayInfo)
}

// recordRelayErrorLog is RecordRelayErrorLog for an error the user may be
// told as userErr instead (see ProcessChannelError). upstreamRequestId is
// the upstream request id of err: the log shows it when the user is told
// err, and keeps it for administrators only when the user is told userErr.
func recordRelayErrorLog(c *gin.Context, channelId int, err, userErr *types.NewAPIError, upstreamRequestId string, relayInfo *relaycommon.RelayInfo) {
	if !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return
	}
	upstreamURLs := common.UpstreamAddresses(c)
	userId := c.GetInt("id")
	tokenName := c.GetString("token_name")
	modelName := c.GetString("original_model")
	tokenId := c.GetInt("token_id")
	userGroup := c.GetString("group")
	other := model.NewLogOther()
	if c.Request != nil && c.Request.URL != nil {
		other.SetPublic("request_path", c.Request.URL.Path)
	}
	shown := err
	if userErr != nil {
		shown = userErr
		other.SetAdmin("upstream_error", err.MaskSensitiveErrorWithStatusCode(upstreamURLs...))
		other.SetAdmin("upstream_status_code", err.StatusCode)
		other.SetAdmin("upstream_error_code", err.GetErrorCode())
		if upstreamRequestId != "" {
			other.SetAdmin("upstream_request_id", upstreamRequestId)
			upstreamRequestId = ""
		}
	}
	other.SetPublic("error_type", shown.GetErrorType())
	other.SetPublic("error_code", shown.GetErrorCode())
	other.SetPublic("status_code", shown.StatusCode)
	AppendRelayLogAdminInfo(c, relayInfo, other)
	appendRequestPluginRecord(relayInfo, other)
	AppendResponseModelLogInfo(relayInfo, other)
	AppendTaskPluginContextAuditInfo(c, other)
	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	useTimeSeconds := int(time.Since(startTime).Seconds())
	model.RecordErrorLog(c, userId, channelId, modelName, tokenName, shown.MaskSensitiveErrorWithStatusCode(upstreamURLs...), upstreamRequestId, tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
}
