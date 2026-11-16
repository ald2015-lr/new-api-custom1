package service

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// geminiPlainRateLimitBody is the answer Google gives for a rate limit
// without quota details. It names no tier, quota, limit or retry delay.
const geminiPlainRateLimitBody = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`

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
// any other 429 of Google; otherwise, and on any other channel, it is nil.
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
