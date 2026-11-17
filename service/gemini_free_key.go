package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// geminiPlainRateLimitBody is the answer Google gives for a rate limit
// without quota details, byte for byte as Google sends it. It names no tier,
// quota, limit or retry delay.
const geminiPlainRateLimitBody = "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"

// IsGeminiFreeTierError tells whether err's text shows that a Gemini key is
// on the free tier (geminifreekey.RevealsFreeTier).
func IsGeminiFreeTierError(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	texts := []string{err.Error()}
	switch relayError := err.RelayError.(type) {
	case types.OpenAIError:
		texts = append(texts, relayError.Message)
	case types.ClaudeError:
		texts = append(texts, relayError.Message)
	}
	return geminifreekey.RevealsFreeTier(strings.Join(texts, "\n"))
}

// DisguiseGeminiFreeKeyError is the error the user gets in place of err on a
// Gemini channel in free key mode: Google's plain rate limit, made as the
// relay makes it of Google's answer (the channel's status code mapping
// included), so that it reads exactly like one. It replaces the relay's own
// free key rejections always, since they describe the channel's keys, and,
// unless the channel reveals free tier errors, Google's free tier errors and
// any other 429 of Google. A channel that reveals them shows Google's last
// 429 as the relay made it, without ErrorCodeGeminiFreeKeyBusy, which only
// keeps its key enabled. Otherwise, and on any other channel, it is nil.
// Billing, retries, automatic disabling and the administrators' log keep err.
func DisguiseGeminiFreeKeyError(c *gin.Context, err *types.NewAPIError) *types.NewAPIError {
	if c == nil || err == nil || common.GetContextKeyInt(c, constant.ContextKeyChannelType) != constant.ChannelTypeGemini {
		return nil
	}
	settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	if !ok || !settings.GeminiFreeKeyEnabled {
		return nil
	}
	local := err.GetErrorType() == types.ErrorTypeNewAPIError
	busy := err.GetErrorCode() == types.ErrorCodeGeminiFreeKeyBusy
	switch {
	case busy && local:
	case settings.GeminiFreeKeyRevealTierErrors:
		if busy {
			return asGoogleSentIt(err)
		}
		return nil
	case busy:
	case !local && (err.StatusCode == http.StatusTooManyRequests || IsGeminiFreeTierError(err)):
	default:
		return nil
	}
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	plain := RelayErrorHandler(ctx, &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json; charset=UTF-8"}},
		Body:       io.NopCloser(strings.NewReader(geminiPlainRateLimitBody)),
	}, false)
	ResetStatusCode(plain, c.GetString("status_code_mapping"))
	return plain
}

// asGoogleSentIt is a copy of err, Google's answer the relay marked with
// ErrorCodeGeminiFreeKeyBusy, with the code the relay gave that answer, or
// nil when err is not one the relay made of an answer.
func asGoogleSentIt(err *types.NewAPIError) *types.NewAPIError {
	answer, ok := err.RelayError.(types.OpenAIError)
	if !ok || err.GetErrorType() != types.ErrorTypeOpenAIError {
		return nil
	}
	shown := *err
	types.ErrOptionWithErrorCode(types.WithOpenAIError(answer, err.StatusCode).GetErrorCode())(&shown)
	return &shown
}

// GeminiFreeKeyTaskError is what becomes of a task submission Google refused
// on a Gemini channel in free key mode (Veo's long-running predictions go
// through the google task plugin, not the free key limits). spare tells that
// the refusal is Google's rate limit, which never disables a free key. shown
// is the TaskError the user gets in its place, Google's plain rate limit as
// the task relay passes Google's answers on, when the channel hides free tier
// errors and Google answered 429 or with a free tier error; otherwise it is
// nil and the user gets taskErr.
func GeminiFreeKeyTaskError(c *gin.Context, taskErr *taskdto.TaskError) (shown *taskdto.TaskError, spare bool) {
	if c == nil || taskErr == nil || taskErr.LocalError || common.GetContextKeyInt(c, constant.ContextKeyChannelType) != constant.ChannelTypeGemini {
		return nil, false
	}
	settings, ok := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	if !ok || !settings.GeminiFreeKeyEnabled {
		return nil, false
	}
	spare = taskErr.StatusCode == http.StatusTooManyRequests
	if settings.GeminiFreeKeyRevealTierErrors || !spare && !geminifreekey.RevealsFreeTier(taskErr.Message) {
		return nil, spare
	}
	return TaskErrorWrapper(errors.New(geminiPlainRateLimitBody), "fail_to_fetch_task", http.StatusTooManyRequests, common.UpstreamAddresses(c)...), spare
}
