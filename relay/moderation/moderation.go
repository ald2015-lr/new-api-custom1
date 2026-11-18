// Package moderation checks requests on OpenAI channels that turn on
// content moderation (the channel's moderation_* settings) before anything
// is sent upstream: the channel's keyword and regex rules, then OpenAI's
// moderation model with the attempt's own key. A rejected request is never
// forwarded, retried or billed, and is recorded as a moderation log row.
package moderation

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

const maxParallelCalls = 4

// probeText is the harmless text the probe sends.
const probeText = "new-api moderation probe: the quick brown fox jumps over the lazy dog"

// requestState is the moderation work of one request that its later
// attempts reuse: the extraction per conversion path and every verdict the
// request obtained (keyed as the cache, http(s) images included).
type requestState struct {
	extractions map[extractionKey]*extraction
	task        *extraction
	verdicts    map[string]Verdict
}

// extractionKey is what an extraction depends on besides the request: a
// retry on another channel may convert to Responses or pass the body through
// where the first did not.
type extractionKey struct {
	viaResponses bool
	passthrough  bool
}

func stateOf(c *gin.Context) *requestState {
	if state, ok := common.GetContextKeyType[*requestState](c, constant.ContextKeyModerationState); ok && state != nil {
		return state
	}
	state := &requestState{extractions: make(map[extractionKey]*extraction), verdicts: make(map[string]Verdict)}
	common.SetContextKey(c, constant.ContextKeyModerationState, state)
	return state
}

// moderated reports whether the attempt's channel, as the context holds it,
// is an OpenAI channel with moderation on.
func moderated(c *gin.Context) bool {
	if !relaycommon.SupportsOpenAISafetyFeatures(common.GetContextKeyInt(c, constant.ContextKeyChannelType)) {
		return false
	}
	settings, _ := common.GetContextKeyType[dto.ChannelOtherSettings](c, constant.ContextKeyChannelOtherSetting)
	return settings.ModerationEnabled
}

// CheckAttempt moderates the attempt that is about to be sent on the channel
// currently in the context (HTTP relay loop and Responses WebSocket). nil
// means send. A non-nil error was already recorded (policy event, moderation
// log row, server line) and must end the request without retry.
func CheckAttempt(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if !moderated(c) {
		return nil
	}
	info.InitChannelMeta(c)
	started := time.Now()
	audit := newAudit(info)
	switch info.RelayFormat {
	case types.RelayFormatOpenAIRealtime:
		return rejectUnsupported(c, info, audit, started, relaycommon.ModerationDetailRealtime)
	case relayconstant.RelayFormatNovelAI:
		return rejectUnsupported(c, info, audit, started, relaycommon.ModerationDetailNovelAI)
	}
	if skipsModeration(info) {
		return nil
	}
	passthrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled
	viaResponses := false
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		viaResponses = info.RelayMode == relayconstant.RelayModeChatCompletions && !passthrough &&
			service.ShouldChatCompletionsUseResponsesGlobal(info.ChannelId, info.ChannelType, info.OriginModelName)
	case *dto.ClaudeRequest:
		viaResponses = !passthrough && service.ShouldChatCompletionsUseResponsesGlobal(info.ChannelId, info.ChannelType, info.OriginModelName)
	}
	state := stateOf(c)
	key := extractionKey{viaResponses: viaResponses, passthrough: passthrough}
	ex, ok := state.extractions[key]
	if !ok {
		var handled bool
		ex, handled = extractRequest(c, info, viaResponses, passthrough)
		if !handled {
			return rejectUnsupported(c, info, audit, started, relaycommon.ModerationDetailRequestType)
		}
		state.extractions[key] = ex
	}
	if info.ApiKey == "" {
		return nil // nothing to protect: the upstream call fails anyway
	}
	return check(c, info, ex, state, audit, started)
}

// CheckTask is CheckAttempt for a task submission (Sora on an OpenAI
// channel): it reads the prompt that ValidateRequestAndSetAction stored as
// "task_request". info must already hold the attempt's channel.
func CheckTask(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	if !moderated(c) || info.ChannelMeta == nil || info.ApiKey == "" {
		return nil
	}
	state := stateOf(c)
	if state.task == nil {
		state.task = extractTask(c)
	}
	apiErr := check(c, info, state.task, state, newAudit(info), time.Now())
	if apiErr == nil {
		return nil
	}
	taskErr := service.TaskErrorFromAPIError(apiErr)
	taskErr.LocalError = true
	return taskErr
}

// skipsModeration lists the requests that carry nothing to moderate or that
// an OpenAI channel refuses anyway with a retryable error.
func skipsModeration(info *relaycommon.RelayInfo) bool {
	if info.RelayMode == relayconstant.RelayModeModerations {
		return true // the user's own moderation call; a base URL pointing here cannot loop
	}
	switch req := info.Request.(type) {
	case *dto.EmbeddingRequest, *dto.GeminiEmbeddingRequest, *dto.GeminiBatchEmbeddingRequest, *dto.RerankRequest,
		*dto.AlphaSearchRequest, *dto.DeepSeekToolsRequest:
		return true
	case *dto.AudioRequest:
		return req == nil || info.RelayMode != relayconstant.RelayModeAudioSpeech
	}
	return false
}

func newAudit(info *relaycommon.RelayInfo) *relaycommon.ModerationAudit {
	return &relaycommon.ModerationAudit{ChannelID: info.ChannelId, RetryIndex: info.RetryIndex}
}

// check runs the rules and then OpenAI on an extraction and records the
// outcome.
func check(c *gin.Context, info *relaycommon.RelayInfo, ex *extraction, state *requestState, audit *relaycommon.ModerationAudit, started time.Time) *types.NewAPIError {
	settings := &info.ChannelOtherSettings
	audit.UncheckedParts = ex.uncheckedParts
	audit.Truncated = ex.truncated
	if ex.unsupported != "" {
		return rejectUnsupported(c, info, audit, started, ex.unsupported)
	}
	if rules := channelRules(c, info.ChannelId, settings); len(rules) > 0 {
		rule, hit := ex.firstRuleHit(rules)
		_, audit.RuleScanTruncated = ex.ruleScan()
		if hit != nil {
			audit.Source = relaycommon.ModerationSourceRule
			ruleAudit := rule.audit
			audit.Rule = &ruleAudit
			audit.Match = hit.match
			return reject(c, info, audit, started, types.ErrorCodeContentModerationFlagged)
		}
	}

	outcome := moderate(c, info, settings, ex, state, audit)
	switch {
	case outcome.flagged:
		audit.Source = relaycommon.ModerationSourceOpenAI
		return reject(c, info, audit, started, types.ErrorCodeContentModerationFlagged)
	case outcome.clientGone:
		return types.NewErrorWithStatusCode(fmt.Errorf("content moderation: %w", context.Canceled), types.ErrorCodeContentModerationUnavailable,
			http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	case audit.Error != nil && !settings.ModerationFailOpen:
		audit.Source = relaycommon.ModerationSourceUnavailable
		return reject(c, info, audit, started, types.ErrorCodeContentModerationUnavailable)
	case audit.Error != nil:
		audit.Status = relaycommon.ModerationStatusFailOpen
		logger.LogWarn(c, common.LogText("content moderation failed on channel #%d (%s); the request was sent because the channel lets requests through on failure", info.ChannelId, audit.Error.Class))
	default:
		audit.Status = relaycommon.ModerationStatusPassed
	}
	audit.ElapsedMs = time.Since(started).Milliseconds()
	common.SetContextKey(c, constant.ContextKeyModerationAudit, audit)
	return nil
}

type moderationOutcome struct {
	flagged    bool
	clientGone bool
}

// moderate sends the attempt's uncached text within its budget and its images
// to OpenAI and fills audit. A flag, cached or fresh, wins over failures of
// other calls and over calls that never ran.
func moderate(c *gin.Context, info *relaycommon.RelayInfo, settings *dto.ChannelOtherSettings, ex *extraction, state *requestState, audit *relaycommon.ModerationAudit) moderationOutcome {
	deadline, cancel := context.WithTimeout(c.Request.Context(), settings.EffectiveModerationTimeout())
	defer cancel()
	endpoint := moderationEndpoint(info)
	budget := settings.EffectiveModerationMaxChars()
	candidates, piecesTruncated := candidatePieces(ex, endpoint, budget)
	images, skipped := selectImages(ex.images, endpoint, settings.ModerationSkipImages)
	audit.ImagesSkipped = skipped
	if piecesTruncated || normalisationTruncated(ex) {
		audit.Truncated = true
	}

	var lookup []string
	for _, p := range candidates {
		if _, ok := state.verdicts[p.key]; !ok {
			lookup = append(lookup, p.key)
		}
	}
	for _, image := range images {
		if _, ok := state.verdicts[image.key]; !ok && image.shareable {
			lookup = append(lookup, image.key)
		}
	}
	maps.Copy(state.verdicts, verdicts().lookup(deadline, lookup))

	var cachedFlags []Verdict
	for _, key := range candidateKeys(candidates, images) {
		if verdict, ok := state.verdicts[key]; ok && flaggedFor(settings, verdict) {
			cachedFlags = append(cachedFlags, verdict)
		}
	}
	if len(cachedFlags) > 0 {
		fillFlagAudit(audit, settings, cachedFlags)
		return moderationOutcome{flagged: true}
	}

	plan := planBudget(candidates, func(key string) bool { _, ok := state.verdicts[key]; return ok }, budget, endpoint)
	audit.CheckedChars, audit.Pieces, audit.CachedPieces = plan.checked, len(plan.send), plan.cached
	if plan.truncated {
		audit.Truncated = true
	}
	var uncachedImages []*imageCandidate
	for _, image := range images {
		if _, ok := state.verdicts[image.key]; ok {
			audit.ImagesChecked++
		} else {
			uncachedImages = append(uncachedImages, image)
		}
	}
	calls := packCalls(plan.send, uncachedImages)
	if len(calls) == 0 {
		return moderationOutcome{}
	}

	client := newModerationClient(c, info)
	outcomes := runCalls(deadline, client, calls, func(call *moderationCall, answer *moderationAnswer) bool {
		for _, result := range answer.Results {
			if flaggedFor(settings, verdictOf(result, answer.Model)) {
				return true
			}
		}
		return false
	})

	var flags []Verdict
	var flagAnswer *moderationAnswer
	flagRequestID, firstRequestID, failureRequestID := "", "", ""
	clientGone := false
	fresh := make(map[string]Verdict)
	for i, call := range calls {
		o := outcomes[i]
		if o.ran {
			audit.Calls++
			if firstRequestID == "" {
				firstRequestID = o.requestID
			}
		}
		if o.answer != nil {
			callFlagged := false
			for j, result := range o.answer.Results {
				verdict := verdictOf(result, o.answer.Model)
				key, shareable := "", true
				if call.image != nil {
					key, shareable = call.image.key, call.image.shareable
					audit.ImagesChecked++
				} else {
					key = call.pieces[j].key
				}
				state.verdicts[key] = verdict
				if shareable {
					fresh[key] = verdict
				}
				if flaggedFor(settings, verdict) {
					flags = append(flags, verdict)
					callFlagged = true
				}
			}
			if callFlagged && flagAnswer == nil {
				flagAnswer, flagRequestID = o.answer, o.requestID
			}
			continue
		}
		switch {
		case o.failure == nil:
			// Never ran: a flag of another call stopped it.
		case o.failure.Class == errCancelled:
			if c.Request.Context().Err() != nil {
				clientGone = true
			}
		case audit.Error == nil:
			audit.Error, failureRequestID = o.failure, o.requestID
		}
	}
	verdicts().save(fresh)

	switch {
	case len(flags) > 0:
		fillFlagAudit(audit, settings, flags)
		audit.Model, audit.ID, audit.UpstreamRequestID = flagAnswer.Model, flagAnswer.ID, flagRequestID
		return moderationOutcome{flagged: true}
	case clientGone:
		return moderationOutcome{clientGone: true}
	case audit.Error != nil:
		audit.UpstreamRequestID = failureRequestID
	default:
		audit.UpstreamRequestID = firstRequestID
		for _, o := range outcomes {
			if o.answer != nil {
				audit.Model = o.answer.Model
				break
			}
		}
	}
	return moderationOutcome{}
}

func candidateKeys(candidates []*piece, images []*imageCandidate) []string {
	keys := make([]string, 0, len(candidates)+len(images))
	for _, p := range candidates {
		keys = append(keys, p.key)
	}
	for _, image := range images {
		keys = append(keys, image.key)
	}
	return keys
}

func normalisationTruncated(ex *extraction) bool {
	return slices.ContainsFunc(ex.segments, func(seg *segment) bool { return seg.norm != nil && seg.norm.truncated })
}

// runCalls sends the calls, at most maxParallelCalls at a time. Calls never
// cancel each other except on a flag, so a fast failure cannot stop a call
// that would flag; a call that had not started when the deadline passed is a
// timeout.
func runCalls(deadline context.Context, client *moderationClient, calls []*moderationCall, flagged func(*moderationCall, *moderationAnswer) bool) []callOutcome {
	callCtx, cancelCalls := context.WithCancel(deadline)
	defer cancelCalls()
	outcomes := make([]callOutcome, len(calls))
	var g errgroup.Group
	g.SetLimit(maxParallelCalls)
	for i := range calls {
		g.Go(func() error {
			if callCtx.Err() != nil {
				switch {
				case errors.Is(deadline.Err(), context.DeadlineExceeded):
					outcomes[i] = callOutcome{failure: client.failure(relaycommon.ModerationErrorClassTimeout, 0, "the moderation call timed out")}
				case deadline.Err() != nil:
					outcomes[i] = callOutcome{failure: &relaycommon.ModerationAuditError{Class: errCancelled}}
				}
				return nil
			}
			outcomes[i] = client.do(callCtx, calls[i])
			if outcomes[i].answer != nil && flagged(calls[i], outcomes[i].answer) {
				cancelCalls()
			}
			return nil
		})
	}
	_ = g.Wait()
	return outcomes
}

// flaggedFor applies the channel's category filter: without one OpenAI's
// flag decides, with one only a true listed category does.
func flaggedFor(settings *dto.ChannelOtherSettings, verdict Verdict) bool {
	if len(settings.ModerationCategories) == 0 {
		return verdict.Flagged
	}
	return slices.ContainsFunc(settings.ModerationCategories, func(category string) bool { return slices.Contains(verdict.Categories, category) })
}

// countedCategories are the true categories of a verdict that the channel's
// filter counts.
func countedCategories(settings *dto.ChannelOtherSettings, verdict Verdict) []string {
	if len(settings.ModerationCategories) == 0 {
		return verdict.Categories
	}
	var counted []string
	for _, category := range verdict.Categories {
		if slices.Contains(settings.ModerationCategories, category) {
			counted = append(counted, category)
		}
	}
	return counted
}

func fillFlagAudit(audit *relaycommon.ModerationAudit, settings *dto.ChannelOtherSettings, flags []Verdict) {
	categories := make(map[string]bool)
	scores := make(map[string]float64)
	applied := make(map[string][]string)
	for _, verdict := range flags {
		for _, category := range countedCategories(settings, verdict) {
			categories[category] = true
			for _, inputType := range verdict.AppliedInputTypes[category] {
				if !slices.Contains(applied[category], inputType) {
					applied[category] = append(applied[category], inputType)
				}
			}
		}
		for category, score := range verdict.Scores {
			scores[category] = max(scores[category], score)
		}
		if audit.Model == "" {
			audit.Model = verdict.Model
		}
	}
	audit.Categories = slices.Sorted(maps.Keys(categories))
	audit.Scores = topScores(scores)
	if len(applied) > 0 {
		for category := range applied {
			slices.Sort(applied[category])
		}
		audit.AppliedInputTypes = applied
	}
}

// rejection texts per code: the client's message key and the log content.
var rejectionTexts = map[types.ErrorCode]struct {
	status  int
	message string
	content string
}{
	types.ErrorCodeContentModerationFlagged:     {http.StatusBadRequest, i18n.MsgModerationFlagged, "Rejected by content moderation"},
	types.ErrorCodeContentModerationUnavailable: {http.StatusServiceUnavailable, i18n.MsgModerationUnavailable, "Rejected: content moderation unavailable"},
	types.ErrorCodeContentModerationUnsupported: {http.StatusBadRequest, i18n.MsgModerationUnsupported, "Rejected: endpoint not available with content moderation"},
}

func rejectUnsupported(c *gin.Context, info *relaycommon.RelayInfo, audit *relaycommon.ModerationAudit, started time.Time, detail string) *types.NewAPIError {
	audit.Source = relaycommon.ModerationSourceUnsupported
	audit.Detail = detail
	return reject(c, info, audit, started, types.ErrorCodeContentModerationUnsupported)
}

// reject records a rejection — the policy event, then the moderation log
// row and a server line — and returns the error the client gets: a generic
// message without categories, rules or URLs.
func reject(c *gin.Context, info *relaycommon.RelayInfo, audit *relaycommon.ModerationAudit, started time.Time, code types.ErrorCode) *types.NewAPIError {
	texts := rejectionTexts[code]
	messageKey, content := texts.message, texts.content
	inputLevel := audit.Detail == relaycommon.ModerationDetailTokenPrompt || audit.Detail == relaycommon.ModerationDetailAmbiguousKeys
	if inputLevel {
		messageKey, content = i18n.MsgModerationUnsupportedInput, "Rejected: request content cannot be moderated"
	}
	info.PerformanceBusinessRejection = true
	apiErr := types.NewErrorWithStatusCode(errors.New(i18n.T(c, messageKey)), code, texts.status,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	service.RecordPolicyFailure(c, info.ChannelId, apiErr, service.PolicyDecision{Action: "stop", Reason: "local_rejection", Source: "channel"})

	audit.Status = relaycommon.ModerationStatusRejected
	audit.ElapsedMs = time.Since(started).Milliseconds()
	other := model.NewLogOther()
	other.SetPublic("request_path", requestPath(c, info))
	other.SetPublic("error_code", string(code))
	other.SetPublic("status_code", texts.status)
	service.AppendRelayLogAdminInfo(c, info, other)
	other.SetAdmin("moderation", audit)
	model.RecordModerationLog(c, model.ModerationLogParams{
		UserId:         info.UserId,
		ChannelId:      info.ChannelId,
		ModelName:      info.OriginModelName,
		TokenName:      c.GetString("token_name"),
		TokenId:        info.TokenId,
		Group:          info.UsingGroup,
		IsStream:       info.IsStream,
		UseTimeSeconds: int(time.Since(info.StartTime).Seconds()),
		Content:        common.NewMessage(content),
		Other:          other,
	})
	logger.LogWarn(c, common.LogText("content moderation rejected a request on channel #%d: source=%s, detail=%s", info.ChannelId, audit.Source, rejectionDetail(audit)))
	return apiErr
}

// rejectionDetail names what rejected a request for the server line: never
// text, a pattern or a match.
func rejectionDetail(audit *relaycommon.ModerationAudit) string {
	switch audit.Source {
	case relaycommon.ModerationSourceOpenAI:
		return strings.Join(audit.Categories, ",")
	case relaycommon.ModerationSourceRule:
		if audit.Rule == nil {
			return ""
		}
		if audit.Rule.Kind == relaycommon.ModerationRuleKindPreset {
			return "preset:" + audit.Rule.ID
		}
		if audit.Rule.Index != nil {
			return "custom:" + strconv.Itoa(*audit.Rule.Index)
		}
	case relaycommon.ModerationSourceUnavailable:
		if audit.Error != nil {
			return audit.Error.Class
		}
	case relaycommon.ModerationSourceUnsupported:
		return audit.Detail
	}
	return ""
}

func requestPath(c *gin.Context, info *relaycommon.RelayInfo) string {
	path := info.RequestURLPath
	if path == "" && c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	path, _, _ = strings.Cut(path, "?")
	return path
}

// ProbeResult is the outcome of one moderation call made by the probe.
type ProbeResult struct {
	OK           bool   `json:"ok"`
	Status       int    `json:"status"` // HTTP status of the moderation answer; 0 when none arrived
	LatencyMs    int64  `json:"latency_ms"`
	Model        string `json:"model"` // as OpenAI echoed it, when ok
	Flagged      bool   `json:"flagged"`
	ErrorClass   string `json:"error_class"`   // a ModerationErrorClass*, "" when ok
	ErrorMessage string `json:"error_message"` // masked, "" when ok
}

// Probe makes one moderation call with a fixed harmless text through the
// same request builder, headers, client, timeout, parser and classifier as a
// moderated request. It reads no cache and records nothing, whether or not
// the channel has moderation on. info carries the channel (IsChannelTest set,
// so no client header is copied); ctx is the caller's request context.
func Probe(ctx context.Context, c *gin.Context, info *relaycommon.RelayInfo) ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, info.ChannelOtherSettings.EffectiveModerationTimeout())
	defer cancel()
	started := time.Now()
	outcome := newModerationClient(c, info).do(ctx, &moderationCall{pieces: []*piece{{text: probeText}}})
	result := ProbeResult{LatencyMs: time.Since(started).Milliseconds()}
	switch {
	case outcome.answer != nil:
		result.OK, result.Status, result.Model = true, http.StatusOK, outcome.answer.Model
		result.Flagged = *outcome.answer.Results[0].Flagged
	case outcome.failure != nil && outcome.failure.Class == errCancelled:
		result.ErrorClass = relaycommon.ModerationErrorClassNetwork
		result.ErrorMessage = "the probe was cancelled"
	case outcome.failure != nil:
		result.Status, result.ErrorClass, result.ErrorMessage = outcome.failure.Status, outcome.failure.Class, outcome.failure.Message
	}
	return result
}
