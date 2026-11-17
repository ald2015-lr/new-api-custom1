package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/geminifreekey"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// A Gemini (AI Studio) channel in free key mode sends each request to a key
// with room for it under the free tier's limits of the model
// (geminifreekey): requests per minute, input tokens per minute (counted
// with Google's countTokens or locally) and requests per Pacific day, all
// per key. A request Google answers with 429 moves to another key of the
// channel, up to the channel's attempts; one no key can take is answered
// with a 429 another channel may take (ErrorCodeGeminiFreeKeyBusy, never a
// reason to disable a key).

const (
	// countTokensTimeout bounds a countTokens call.
	countTokensTimeout = 10 * time.Second
	// countTokensAnswerMax bounds the countTokens answer read.
	countTokensAnswerMax = 1 << 20
	// tierErrorTextMax bounds the upstream text an administrator sees in a
	// free tier error answered as a rate limit.
	tierErrorTextMax = 1000
)

// freeKeyMode tells whether info's channel is a Gemini channel in free key
// mode. The adaptors that embed Adaptor (Vertex and others) are never in it.
func freeKeyMode(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && info.ChannelType == appconstant.ChannelTypeGemini && info.ChannelOtherSettings.GeminiFreeKeyEnabled
}

// freeKeyAnswer is an answer Google gave a key of the request, kept until
// the next key is tried or the request ends on it.
type freeKeyAnswer struct {
	key     geminifreekey.Key
	verdict geminifreekey.Verdict
	resp    *http.Response
	body    []byte
}

// freeKeyRequest is a request on a channel in free key mode.
type freeKeyRequest struct {
	c       *gin.Context
	info    *relaycommon.RelayInfo
	limiter *geminifreekey.Limiter
	pool    geminifreekey.Pool
	audit   *geminifreekey.Audit
	// tried are the keys the request used, countTokens calls that rejected
	// a key included; at most attempts.
	tried    []geminifreekey.KeyID
	attempts int
	// last is Google's last answer to a key that refused the request.
	last *freeKeyAnswer
}

// doFreeKeyRequest is DoRequest on a channel in free key mode.
func (a *Adaptor) doFreeKeyRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	settings := info.ChannelOtherSettings
	rows := settings.EffectiveGeminiFreeKeyModelLimits()
	r := &freeKeyRequest{
		c:        c,
		info:     info,
		limiter:  geminifreekey.Default(),
		attempts: settings.EffectiveGeminiFreeKeyMaxAttempts(),
	}
	// Selection from the database skips a channel that cannot serve the
	// model only once the limiter is active, which a request the channel
	// answers on its own makes it as well.
	r.limiter.MarkActive()
	r.pool = geminifreekey.Pool{ChannelID: info.ChannelId, Bucket: geminifreekey.ResolveBucket(rows, info.UpstreamModelName), Rows: rows}
	r.pool.Keys, r.pool.Random = freeKeyPoolKeys(info)
	r.audit = &geminifreekey.Audit{ChannelID: info.ChannelId, RetryIndex: info.RetryIndex, Bucket: r.pool.Bucket.ID, KeyIndex: -1}
	common.SetContextKey(c, appconstant.ContextKeyGeminiFreeKeyAudit, r.audit)
	bucket, limits := r.pool.Bucket.ID, r.pool.Bucket.Limits

	if r.pool.Bucket.NotOnFreeTier() {
		return nil, r.reject("not_free", i18n.T(c, i18n.MsgGeminiFreeKeyNotFree, map[string]any{"Model": info.UpstreamModelName, "Bucket": bucket}))
	}
	if report := r.limiter.Inspect(r.pool, 0, 0, nil); report.Usable() == 0 {
		return nil, r.reject("no_key", i18n.T(c, i18n.MsgGeminiFreeKeyNoKey, map[string]any{"Bucket": bucket, "Keys": describeFreeKeys(c, report)}))
	}

	body, err := newFreeKeyBody(requestBody)
	if err != nil {
		return nil, err
	}
	tokens, units, err := r.countTokens(body)
	if err != nil {
		return nil, err
	}
	if r.last != nil && r.nextKey() == nil {
		// countTokens rejected every key the request could use.
		return r.end()
	}
	if limits.TPM > 0 && tokens > limits.TPM || limits.RPM > 0 && units > limits.RPM || limits.RPD > 0 && units > limits.RPD {
		return nil, r.reject("too_large", i18n.T(c, i18n.MsgGeminiFreeKeyTooLarge, map[string]any{
			"Tokens": tokens, "Source": r.audit.TokenSource, "Requests": units, "Bucket": bucket,
			"RPM": limits.RPM, "TPM": limits.TPM, "RPD": limits.RPD,
		}))
	}

	for len(r.tried) < r.attempts {
		ticket, report := r.limiter.Reserve(r.pool, tokens, units, r.tried)
		if ticket == nil {
			if r.last == nil {
				return nil, r.reject("no_capacity", i18n.T(c, i18n.MsgGeminiFreeKeyNoCapacity, map[string]any{"Bucket": bucket, "Tokens": tokens, "Keys": describeFreeKeys(c, report)}))
			}
			break
		}
		r.useKey(ticket.Key)
		reader, err := body.next()
		if err != nil {
			ticket.Rollback()
			return nil, err
		}
		// Whether the whole request went out: Google counts a request it
		// received even when its answer never comes back.
		var sent atomic.Bool
		trace := &httptrace.ClientTrace{WroteRequest: func(wrote httptrace.WroteRequestInfo) { sent.Store(wrote.Err == nil) }}
		resp, err := channel.DoApiRequestWithContext(httptrace.WithClientTrace(c.Request.Context(), trace), a, c, info, reader)
		if err != nil {
			if sent.Load() {
				ticket.Finish(0, nil)
				r.audit.Attempts = append(r.audit.Attempts, geminifreekey.AuditAttempt{KeyIndex: ticket.Key.Index, Reason: "no_answer"})
				return nil, err
			}
			// The request never reached Google.
			ticket.Rollback()
			r.audit.Attempts = append(r.audit.Attempts, geminifreekey.AuditAttempt{KeyIndex: ticket.Key.Index, Reason: "transport_error"})
			return nil, err
		}
		if resp.StatusCode == http.StatusOK {
			ticket.Finish(resp.StatusCode, nil)
			r.audit.Attempts = append(r.audit.Attempts, geminifreekey.AuditAttempt{KeyIndex: ticket.Key.Index, Status: resp.StatusCode, Reason: string(geminifreekey.OutcomeAccepted)})
			return resp, nil
		}
		// Whoever handles the answer next reads the same bytes.
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(data))
		answer := &freeKeyAnswer{key: ticket.Key, resp: resp, body: data, verdict: ticket.Finish(resp.StatusCode, data)}
		attempt := geminifreekey.AuditAttempt{KeyIndex: ticket.Key.Index, Status: resp.StatusCode, Reason: string(answer.verdict.Reason)}
		if attempt.Reason == "" {
			attempt.Reason = string(answer.verdict.Outcome)
		}
		r.audit.Attempts = append(r.audit.Attempts, attempt)
		switch answer.verdict.Outcome {
		case geminifreekey.OutcomeRateLimited, geminifreekey.OutcomeKeyRejected:
			r.last = answer
			continue
		}
		if !settings.GeminiFreeKeyRevealTierErrors && geminifreekey.RevealsFreeTier(string(data)) {
			// Google's message, else its body, for administrators.
			text := string(data)
			if googleError, ok := geminifreekey.ParseGoogleError(data); ok && googleError.Message != "" {
				text = googleError.Message
			}
			if runes := []rune(text); len(runes) > tierErrorTextMax {
				text = string(runes[:tierErrorTextMax])
			}
			return nil, r.reject("tier_error", i18n.T(c, i18n.MsgGeminiFreeKeyTierError, map[string]any{"Status": resp.StatusCode, "Error": text}))
		}
		return resp, nil
	}
	return r.end()
}

// end answers the request with Google's last refusal once no other key may
// take it: a rejected key's answer as Google sent it, with the request
// pointing at that key (countTokens may have rejected it) so that the
// automatic disable rules act on it, and a 429 as the error the relay makes
// of it (the same JSON for the client) under ErrorCodeGeminiFreeKeyBusy.
// The request keeps that 429 for its later attempts on the channel
// (ContextKeyGeminiFreeKeyRateLimits).
func (r *freeKeyRequest) end() (*http.Response, error) {
	last := r.last
	if last.verdict.Outcome == geminifreekey.OutcomeKeyRejected {
		r.pointAt(last.key)
		// The relay disables the key by its own rules; a key without a free
		// tier, which those rules need not cover, is disabled here.
		if !r.info.IsChannelTest && geminifreekey.IsFreeTierUnavailable(last.resp.StatusCode, last.body) {
			if apiErr := r.answerError(last); !service.ShouldDisableChannel(apiErr) {
				r.disableKey(last, apiErr, true)
			}
		}
		return last.resp, nil
	}
	r.audit.Rejected = "rate_limited"
	apiErr := service.RelayErrorHandler(r.c.Request.Context(), last.resp, false)
	service.ResetStatusCode(apiErr, r.c.GetString("status_code_mapping"))
	types.ErrOptionWithErrorCode(types.ErrorCodeGeminiFreeKeyBusy)(apiErr)
	rateLimits, _ := common.GetContextKeyType[map[int]*types.NewAPIError](r.c, appconstant.ContextKeyGeminiFreeKeyRateLimits)
	if rateLimits == nil {
		rateLimits = map[int]*types.NewAPIError{}
		common.SetContextKey(r.c, appconstant.ContextKeyGeminiFreeKeyRateLimits, rateLimits)
	}
	rateLimits[r.info.ChannelId] = apiErr
	return nil, apiErr
}

// reject answers the request without Google with a 429 another channel may
// take; message is for administrators only (see DisguiseGeminiFreeKeyError).
// A stream gets the headers a request sent to Google gets, so that the
// answer reads like Google's own rate limit.
func (r *freeKeyRequest) reject(reason, message string) error {
	r.audit.Rejected = reason
	if r.info.IsStream {
		helper.SetEventStreamHeaders(r.c)
	}
	return types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeGeminiFreeKeyBusy, http.StatusTooManyRequests)
}

// useKey makes key the attempt's key: the upstream call, the automatic
// disable of a failing key and the logs' key index use it. Google's
// rejection of the key before it, which the request leaves behind, disables
// that key as the relay would have.
func (r *freeKeyRequest) useKey(key geminifreekey.Key) {
	r.disableRejectedKey()
	r.tried = append(r.tried, geminifreekey.IDOf(key.Secret))
	r.pointAt(key)
}

// pointAt makes key the one the request's answer comes from, as
// novelai.holdGeneration does: the upstream call, the automatic disable of a
// failing key and the logs' key index use it.
func (r *freeKeyRequest) pointAt(key geminifreekey.Key) {
	r.info.ApiKey, r.info.ChannelMultiKeyIndex = key.Secret, key.Index
	if r.info.ChannelIsMultiKey {
		common.SetContextKey(r.c, appconstant.ContextKeyChannelKey, key.Secret)
		common.SetContextKey(r.c, appconstant.ContextKeyChannelMultiKeyIndex, key.Index)
	}
	r.audit.KeyIndex = key.Index
}

// nextKey is the key the request would try next, or nil when it may try no
// other.
func (r *freeKeyRequest) nextKey() *geminifreekey.Key {
	if len(r.tried) >= r.attempts {
		return nil
	}
	key, ok := r.limiter.Pick(r.pool, r.tried)
	if !ok {
		return nil
	}
	return &key
}

// disableRejectedKey disables the key Google rejected on the request's last
// answer, as ProcessChannelError does for the answer a request ends on
// (respecting the channel's automatic disable), once the request moves on to
// another key. A key whose Google project has no free tier can never serve in
// free key mode and is disabled whatever the relay's rules say. A channel test
// disables nothing here: the test acts on the answer it ends on, as it would
// without the mode, and the key sits out in the limiter all the same.
func (r *freeKeyRequest) disableRejectedKey() {
	last := r.last
	r.last = nil
	if last == nil || last.verdict.Outcome != geminifreekey.OutcomeKeyRejected || r.info.IsChannelTest {
		return
	}
	apiErr := r.answerError(last)
	noFreeTier := geminifreekey.IsFreeTierUnavailable(last.resp.StatusCode, last.body)
	if !noFreeTier && !service.ShouldDisableChannel(apiErr) {
		return
	}
	r.disableKey(last, apiErr, noFreeTier)
}

// answerError is the error the relay makes of answer.
func (r *freeKeyRequest) answerError(answer *freeKeyAnswer) *types.NewAPIError {
	apiErr := service.RelayErrorHandler(r.c.Request.Context(), &http.Response{StatusCode: answer.resp.StatusCode, Header: answer.resp.Header, Body: io.NopCloser(bytes.NewReader(answer.body))}, false)
	service.ResetStatusCode(apiErr, r.c.GetString("status_code_mapping"))
	return apiErr
}

// disableKey disables answer's key for apiErr, the error the relay made of
// answer, when the channel disables failing keys automatically; noFreeTier
// tells that its Google project has no free tier.
func (r *freeKeyRequest) disableKey(answer *freeKeyAnswer, apiErr *types.NewAPIError, noFreeTier bool) {
	autoBan := common.GetContextKeyBool(r.c, appconstant.ContextKeyChannelAutoBan)
	if !autoBan {
		return
	}
	channelError := *types.NewChannelError(r.info.ChannelId, r.info.ChannelType, common.GetContextKeyString(r.c, appconstant.ContextKeyChannelName), r.info.ChannelIsMultiKey, answer.key.Secret, autoBan)
	reason := apiErr.MaskSensitiveErrorWithStatusCode(common.UpstreamAddresses(r.c)...)
	if noFreeTier {
		logger.LogWarn(r.c, common.LogText("Gemini free key channel #%d key #%d has no free tier on its Google project, disabling it", r.info.ChannelId, answer.key.Index+1))
	} else {
		logger.LogWarn(r.c, common.LogText("Gemini free key channel #%d key #%d was rejected by Google, disabling it and trying another key", r.info.ChannelId, answer.key.Index+1))
	}
	gopool.Go(func() {
		service.DisableChannel(channelError, reason)
	})
}

// countTokens counts the request's input tokens once, with Google's
// countTokens on the key the request would use when the channel says so and
// the body is a generate or embed request, else (or when countTokens fails)
// with the local estimate; units is how many requests the body makes (the
// items of a batch embedding). A countTokens answer that rejects the key
// pauses it and moves to another key, as an attempt; once no other key may
// be tried, the request ends on that answer (doFreeKeyRequest).
func (r *freeKeyRequest) countTokens(body *freeKeyBody) (tokens int, units int, err error) {
	data, err := body.bytes()
	if err != nil {
		return 0, 0, err
	}
	payload, units := countTokensPayload(data, r.info.UpstreamModelName)
	if payload != nil && r.info.ChannelOtherSettings.EffectiveGeminiFreeKeyTokenCounter() == dto.GeminiFreeKeyTokenCounterCountTokens && !r.info.ChannelSetting.PassThroughBodyEnabled {
		key := r.nextKey()
		for key != nil {
			r.disableRejectedKey()
			total, answer := r.callCountTokens(*key, payload)
			if total >= 0 {
				r.audit.Tokens, r.audit.TokenSource = total, dto.GeminiFreeKeyTokenCounterCountTokens
				return total, units, nil
			}
			if answer == nil {
				break
			}
			status := answer.resp.StatusCode
			if !geminifreekey.IsKeyRejection(status, answer.body) {
				logger.LogWarn(r.c, common.LogText("Gemini free key channel #%d: countTokens answered %d, using the local estimate", r.info.ChannelId, status))
				break
			}
			answer.verdict = r.limiter.Observe(r.pool, *key, status, answer.body)
			r.tried = append(r.tried, geminifreekey.IDOf(key.Secret))
			r.audit.Attempts = append(r.audit.Attempts, geminifreekey.AuditAttempt{KeyIndex: key.Index, Status: status, Reason: "count_tokens_" + string(answer.verdict.Reason)})
			r.last = answer
			// Key numbers count from 1, as the channel's key list shows them.
			rejected := key.Index + 1
			if key = r.nextKey(); key == nil {
				logger.LogWarn(r.c, common.LogText("Gemini free key channel #%d: Google rejected key #%d on countTokens (status %d) and no other key may be tried, answering with that rejection", r.info.ChannelId, rejected, status))
				break
			}
			logger.LogWarn(r.c, common.LogText("Gemini free key channel #%d: Google rejected key #%d on countTokens (status %d), trying key #%d", r.info.ChannelId, rejected, status, key.Index+1))
		}
	}
	tokens = freeKeyLocalTokens(r.c, r.info)
	r.audit.Tokens, r.audit.TokenSource = tokens, dto.GeminiFreeKeyTokenCounterLocal
	return tokens, units, nil
}

// callCountTokens asks Google to count payload on key. It returns the count,
// or -1 with Google's answer when it gave one that is not a count, or -1
// alone when it got no answer (the request then uses the local estimate).
func (r *freeKeyRequest) callCountTokens(key geminifreekey.Key, payload []byte) (int, *freeKeyAnswer) {
	info := r.info
	version := model_setting.GetGeminiVersionSetting(info.UpstreamModelName)
	url := fmt.Sprintf("%s/%s/models/%s:countTokens", info.ChannelBaseUrl, version, info.UpstreamModelName)
	ctx, cancel := context.WithTimeout(r.c.Request.Context(), countTokensTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return -1, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key.Secret)
	// The channel's header overrides apply as on the request itself, an
	// {api_key} placeholder meaning this key.
	sentKey := info.ApiKey
	info.ApiKey = key.Secret
	err = channel.ApplyHeaderOverride(info, r.c, req)
	info.ApiKey = sentKey
	if err != nil {
		return -1, nil
	}
	client, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return -1, nil
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.LogWarn(r.c, common.LogText("Gemini free key channel #%d: countTokens failed, using the local estimate: %s", info.ChannelId, err.Error()))
		return -1, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, countTokensAnswerMax))
	_ = resp.Body.Close()
	if err != nil {
		return -1, nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if resp.StatusCode == http.StatusOK {
		var counted struct {
			TotalTokens *int `json:"totalTokens"`
		}
		if common.Unmarshal(data, &counted) == nil && counted.TotalTokens != nil && *counted.TotalTokens >= 0 {
			return *counted.TotalTokens, nil
		}
	}
	return -1, &freeKeyAnswer{key: key, resp: resp, body: data}
}

// countTokensPayload is the countTokens body for an outbound request body:
// {"generateContentRequest": body with its model} for a generate request,
// {"contents": [...]} with each request's content for an embed or batch
// embed request, nil for anything else (an Imagen prediction, a body that is
// not JSON). units is how many requests the body makes: its embed requests,
// else 1.
func countTokensPayload(data []byte, model string) (payload []byte, units int) {
	units = 1
	var fields map[string]json.RawMessage
	if common.Unmarshal(data, &fields) != nil {
		return nil, units
	}
	var requests []struct {
		Content json.RawMessage `json:"content"`
	}
	if raw, ok := fields["requests"]; ok && common.GetJsonType(raw) == "array" && common.Unmarshal(raw, &requests) == nil && len(requests) > 0 {
		units = len(requests)
	}
	if contents, ok := fields["contents"]; ok && common.GetJsonType(contents) == "array" {
		name, err := common.Marshal("models/" + model)
		if err != nil {
			return nil, units
		}
		fields["model"] = name
		inner, err := common.Marshal(fields)
		if err != nil {
			return nil, units
		}
		payload, err = common.Marshal(map[string]json.RawMessage{"generateContentRequest": inner})
		if err != nil {
			return nil, units
		}
		return payload, units
	}
	var contents []json.RawMessage
	if content, ok := fields["content"]; ok && common.GetJsonType(content) == "object" {
		contents = append(contents, content)
	}
	for _, request := range requests {
		if common.GetJsonType(request.Content) == "object" {
			contents = append(contents, request.Content)
		}
	}
	if len(contents) == 0 {
		return nil, units
	}
	payload, err := common.Marshal(map[string][]json.RawMessage{"contents": contents})
	if err != nil {
		return nil, units
	}
	return payload, units
}

// describeFreeKeys tells administrators, in their language, how many keys
// the channel has and what keeps them from the request.
func describeFreeKeys(c *gin.Context, report geminifreekey.Report) string {
	parts := []string{i18n.T(c, i18n.MsgGeminiFreeKeyKeys, map[string]any{"Count": report.Keys})}
	for _, count := range []struct {
		keys    int
		message string
	}{
		{report.Tried, i18n.MsgGeminiFreeKeyKeysTried},
		{report.KeyError, i18n.MsgGeminiFreeKeyKeysRejected},
		{report.PausedNotFree, i18n.MsgGeminiFreeKeyKeysNotFree},
		{report.PausedDay, i18n.MsgGeminiFreeKeyKeysPausedDay},
		{report.DayFull, i18n.MsgGeminiFreeKeyKeysDayFull},
		{report.PausedMinute, i18n.MsgGeminiFreeKeyKeysPausedMinute},
		{report.PausedUnknown, i18n.MsgGeminiFreeKeyKeysPausedUnknown},
		{report.RPMFull, i18n.MsgGeminiFreeKeyKeysRPMFull},
		{report.TPMFull, i18n.MsgGeminiFreeKeyKeysTPMFull},
		{report.Ready, i18n.MsgGeminiFreeKeyKeysReady},
	} {
		if count.keys > 0 {
			parts = append(parts, i18n.T(c, count.message, map[string]any{"Count": count.keys}))
		}
	}
	return strings.Join(parts, i18n.T(c, i18n.MsgGeminiFreeKeyKeysSeparator))
}

// freeKeyLocalTokens is the local estimate of the request's input tokens:
// the one billing made, else a count of the request, else 0.
func freeKeyLocalTokens(c *gin.Context, info *relaycommon.RelayInfo) int {
	if tokens := info.GetEstimatePromptTokens(); tokens > 0 {
		return tokens
	}
	if info.Request == nil {
		return 0
	}
	tokens, err := service.CountRequestToken(c, info.Request.GetTokenCountMeta(), info)
	if err != nil {
		return 0
	}
	return max(tokens, 0)
}

// freeKeyPoolKeys are the enabled keys of info's channel and whether it picks
// them at random. A single-key channel's key is index 0.
func freeKeyPoolKeys(info *relaycommon.RelayInfo) ([]geminifreekey.Key, bool) {
	if !info.ChannelIsMultiKey {
		return []geminifreekey.Key{{Index: 0, Secret: info.ApiKey}}, false
	}
	keys := []geminifreekey.Key{{Index: info.ChannelMultiKeyIndex, Secret: info.ApiKey}}
	cached, err := model.CacheGetChannel(info.ChannelId)
	if err != nil || !cached.ChannelInfo.IsMultiKey {
		return keys, false
	}
	all, enabled := cached.EnabledMultiKeys()
	if len(enabled) == 0 {
		return keys, false
	}
	keys = make([]geminifreekey.Key, 0, len(enabled))
	for _, index := range enabled {
		keys = append(keys, geminifreekey.Key{Index: index, Secret: all[index]})
	}
	return keys, cached.ChannelInfo.MultiKeyMode == appconstant.MultiKeyModeRandom
}

// freeKeyBody sends the request body again on each attempt: a replayable
// body through a fresh reader of the same bytes, any other body (the channel
// test's buffer) read once.
type freeKeyBody struct {
	replayable common.ReplayableBody
	data       []byte
	sent       bool
}

func newFreeKeyBody(body io.Reader) (*freeKeyBody, error) {
	if replayable, ok := body.(common.ReplayableBody); ok {
		return &freeKeyBody{replayable: replayable}, nil
	}
	if body == nil {
		return &freeKeyBody{}, nil
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return &freeKeyBody{data: data}, nil
}

// bytes is the whole body, read without moving the reader the first attempt
// sends.
func (b *freeKeyBody) bytes() ([]byte, error) {
	if b.replayable == nil {
		return b.data, nil
	}
	reader, err := b.replayable.NewReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// next is the body for the next attempt: the original replayable body the
// first time, then a fresh reader that keeps its length and GetBody.
func (b *freeKeyBody) next() (io.Reader, error) {
	if b.replayable == nil {
		return bytes.NewReader(b.data), nil
	}
	if !b.sent {
		b.sent = true
		return b.replayable, nil
	}
	reader, err := b.replayable.NewReader()
	if err != nil {
		return nil, err
	}
	return &replayedBody{ReadCloser: reader, source: b.replayable}, nil
}

// replayedBody is a fresh reader of a replayable body that is itself
// replayable, so that the request it is sent with gets the body's length and
// GetBody (channel.ApplyUpstreamBodyMetadata).
type replayedBody struct {
	io.ReadCloser
	source common.ReplayableBody
}

func (b *replayedBody) Size() int64 { return b.source.Size() }

func (b *replayedBody) NewReader() (io.ReadCloser, error) { return b.source.NewReader() }
