package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// upstreamQuotaErrorCodes are the error codes and types, lower case, that
// upstreams answer when their own account has run out of balance or quota.
var upstreamQuotaErrorCodes = []string{
	"insufficient_quota",             // OpenAI
	"insufficient_user_quota",        // new-api (quota.user_insufficient)
	"pre_consume_token_quota_failed", // new-api (quota.pre_consume_insufficient, token pre-consume)
	"billing_not_active",             // OpenAI
	"insufficient_balance",           // DeepSeek and other OpenAI-compatible providers
	"arrearage",                      // Alibaba Cloud Model Studio (DashScope)
	"exceeded_current_quota_error",   // Moonshot
	"accountoverdueerror",            // Volcengine (AccountOverdueError)
	"service_quota_exhausted",        // new-api hiding its own upstream's balance error (upstream_quota_error_mask_enabled)
}

// upstreamQuotaErrorPhrases are fixed parts, lower case, of the messages
// upstreams answer when their own account has run out of balance or quota.
// Upstreams are usually other new-api or one-api instances, so their
// messages come first; templated parts (amounts, request ids) are left out.
// "quota exceeded" alone is not one: providers also say it of rate limits.
var upstreamQuotaErrorPhrases = []string{
	"insufficient user quota",            // new-api i18n/locales/en.yaml quota.user_insufficient
	"not enough quota to pre-consume",    // new-api i18n/locales/en.yaml quota.pre_consume_insufficient
	"subscription quota is insufficient", // new-api i18n/locales/en.yaml quota.subscription_insufficient
	"用户额度不足",                             // new-api i18n/locales/zh-CN.yaml quota.user_insufficient; one-api
	"预扣费额度失败",                            // new-api i18n/locales/zh-CN.yaml quota.pre_consume_insufficient; one-api
	"订阅额度不足",                             // new-api i18n/locales/zh-CN.yaml quota.subscription_insufficient
	"使用者額度不足",                            // new-api i18n/locales/zh-TW.yaml quota.user_insufficient
	"預扣費額度失敗",                            // new-api i18n/locales/zh-TW.yaml quota.pre_consume_insufficient
	"訂閱額度不足",                             // new-api i18n/locales/zh-TW.yaml quota.subscription_insufficient
	"user quota is not enough",           // new-api and one-api user pre-consume (service/quota.go)
	"token quota is not enough",          // new-api and one-api token pre-consume (service/quota.go)
	"该令牌额度已用尽",                           // one-api
	"令牌额度不足",                             // one-api
	"exceeded your current quota",        // OpenAI
	"credit balance is too low",          // Anthropic
	"insufficient balance",               // DeepSeek
	"insufficient credits",               // OpenRouter
	"account balance is insufficient",    // SiliconFlow
	"欠费",                                 // Zhipu, Volcengine
	"余额不足",                               // generic
	"餘額不足",                               // generic, Traditional Chinese
	"额度不足",                               // generic
	"額度不足",                               // generic, Traditional Chinese
	"额度已用尽",                              // generic
	"quota_not_enough",                   // new-api and one-api Midjourney relay (relay/mjproxy_handler.go)
	"上游tokens数量不足",                       // new-api hiding its own upstream's balance error (relaykit/dto UpstreamQuotaErrorMessageDefault)
}

// upstreamRateLimitPhrases, lower case, mark a rate limit, which is never a
// balance error even when its wording says quota: Gemini's free tier answers
// its per-minute limits with 429 "You exceeded your current quota ...
// Please retry in 43s". RPM and TPM count as whole words only.
var upstreamRateLimitPhrases = []string{
	"resource_exhausted", // Google RPC status of rate limits
	"rate limit",
	"rate-limit",
	"rate_limit",
	"per minute",
	"per day",
	"requests per",
	"tokens per min",
	"retry in",
	"请求过于频繁",
	"频率",
}

// IsUpstreamQuotaExhaustedError reports whether err is an upstream's answer
// that its own account has run out of balance or quota. new-api's own errors
// (type new_api_error), such as the user's own insufficient_user_quota and
// pre_consume_token_quota_failed, and channel errors are never one.
func IsUpstreamQuotaExhaustedError(err *types.NewAPIError) bool {
	if err == nil || err.GetErrorType() == types.ErrorTypeNewAPIError || types.IsChannelError(err) {
		return false
	}
	codes := []string{string(err.GetErrorCode())}
	switch relayError := err.RelayError.(type) {
	case types.OpenAIError:
		codes = append(codes, relayError.Type)
	case types.ClaudeError:
		codes = append(codes, relayError.Type)
	}
	var kept *upstreamErrorFields
	if errors.As(err.Err, &kept) {
		codes = append(codes, kept.codes...)
	}
	return isQuotaExhaustedAnswer(err.StatusCode, []string{err.Error()}, codes)
}

// upstreamErrorFields wraps the error of an upstream's answer with the codes
// of its body that the relay error has no place for. Its text stays the
// error's, so nothing the client or a log gets changes.
type upstreamErrorFields struct {
	error
	codes []string
}

func (e *upstreamErrorFields) Unwrap() error {
	return e.error
}

// keepUpstreamErrorFields keeps on err, for IsUpstreamQuotaExhaustedError, the
// fields of the upstream's error body that err leaves out: Google's status,
// RESOURCE_EXHAUSTED for a rate limit, and a code outside the "error" object,
// as DashScope's native endpoints send Arrearage.
func keepUpstreamErrorFields(err *types.NewAPIError, body []byte) {
	var fields struct {
		Code  any             `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	if err == nil || common.Unmarshal(body, &fields) != nil {
		return
	}
	var codes []string
	if fields.Code != nil {
		codes = append(codes, fmt.Sprint(fields.Code))
	}
	var googleError struct {
		Status string `json:"status"`
	}
	if common.GetJsonType(fields.Error) == "object" && common.Unmarshal(fields.Error, &googleError) == nil && googleError.Status != "" {
		codes = append(codes, googleError.Status)
	}
	if len(codes) > 0 {
		err.Err = &upstreamErrorFields{error: err.Err, codes: codes}
	}
}

// IsUpstreamQuotaExhaustedTaskError is IsUpstreamQuotaExhaustedError for a
// task submission. A local rejection is never one, and neither is the user's
// own pre-consume failure: TaskErrorFromAPIError copies new-api's code into
// it, while an upstream's answer gets a host code (fail_to_fetch_task) and its
// response body as the message.
func IsUpstreamQuotaExhaustedTaskError(taskErr *taskdto.TaskError) bool {
	if taskErr == nil || taskErr.LocalError {
		return false
	}
	switch types.ErrorCode(taskErr.Code) {
	case types.ErrorCodeInsufficientUserQuota, types.ErrorCodePreConsumeTokenQuotaFailed:
		return false
	}
	messages, codes := []string{taskErr.Message}, []string{}
	// The vendor's error fields, at the top level or under "error", so that a
	// code still counts when the body escapes its text.
	type vendorError struct {
		Code        any    `json:"code"`
		Type        string `json:"type"`
		Status      string `json:"status"`
		Message     string `json:"message"`
		Msg         string `json:"msg"`
		Description string `json:"description"` // Midjourney proxies
	}
	var body struct {
		vendorError
		Error json.RawMessage `json:"error"`
	}
	if common.UnmarshalJsonStr(taskErr.Message, &body) == nil {
		found := []vendorError{body.vendorError}
		var nested vendorError
		if common.GetJsonType(body.Error) == "object" && common.Unmarshal(body.Error, &nested) == nil {
			found = append(found, nested)
		}
		for _, vendor := range found {
			if vendor.Code != nil {
				codes = append(codes, fmt.Sprint(vendor.Code))
			}
			codes = append(codes, vendor.Type, vendor.Status)
			messages = append(messages, vendor.Message, vendor.Msg, vendor.Description)
		}
	}
	return isQuotaExhaustedAnswer(taskErr.StatusCode, messages, codes)
}

// isQuotaExhaustedAnswer is the balance rule shared by relay and task errors:
// 402, a balance code or type, or a balance phrase, unless the answer is a
// rate limit.
func isQuotaExhaustedAnswer(statusCode int, messages []string, codes []string) bool {
	text := strings.ToLower(strings.Join(messages, "\n"))
	for _, code := range codes {
		if strings.EqualFold(code, "resource_exhausted") {
			return false
		}
	}
	for _, phrase := range upstreamRateLimitPhrases {
		if strings.Contains(text, phrase) {
			return false
		}
	}
	for word := range strings.FieldsFuncSeq(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if word == "rpm" || word == "tpm" {
			return false
		}
	}
	if statusCode == http.StatusPaymentRequired {
		return true
	}
	for _, code := range codes {
		if slices.Contains(upstreamQuotaErrorCodes, strings.ToLower(code)) {
			return true
		}
	}
	return slices.ContainsFunc(upstreamQuotaErrorPhrases, func(phrase string) bool {
		return strings.Contains(text, phrase)
	})
}

// MaskUpstreamQuotaError is the error the user gets in place of err: the
// message and status of the channel selected for c when err is the upstream's
// balance error and the channel hides those (upstream_quota_error_mask_enabled),
// otherwise nil. It carries no upstream text, code or request id.
func MaskUpstreamQuotaError(c *gin.Context, err *types.NewAPIError) *types.NewAPIError {
	if !IsUpstreamQuotaExhaustedError(err) {
		return nil
	}
	return channelUpstreamQuotaError(c)
}

// MaskUpstreamQuotaTaskError is MaskUpstreamQuotaError for a task submission.
func MaskUpstreamQuotaTaskError(c *gin.Context, taskErr *taskdto.TaskError) *types.NewAPIError {
	if !IsUpstreamQuotaExhaustedTaskError(taskErr) {
		return nil
	}
	return channelUpstreamQuotaError(c)
}

// MaskUpstreamQuotaMidjourneyError is MaskUpstreamQuotaError for a Midjourney
// submission that the upstream refused with statusCode and body.
func MaskUpstreamQuotaMidjourneyError(c *gin.Context, statusCode int, body []byte) *types.NewAPIError {
	return MaskUpstreamQuotaTaskError(c, &taskdto.TaskError{StatusCode: statusCode, Message: string(body)})
}

// channelUpstreamQuotaError is the error the channel selected for c answers in
// place of an upstream balance error, or nil when it passes those through.
func channelUpstreamQuotaError(c *gin.Context) *types.NewAPIError {
	settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	if !ok || !settings.UpstreamQuotaErrorMaskEnabled {
		return nil
	}
	return types.NewErrorWithStatusCode(errors.New(settings.EffectiveUpstreamQuotaErrorMessage()), types.ErrorCodeServiceQuotaExhausted, settings.EffectiveUpstreamQuotaErrorStatusCode(), types.ErrOptionWithSkipRetry())
}
