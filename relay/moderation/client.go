package moderation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	answerMaxBytes      = 1 << 20
	failureMessageRunes = 200
	scoresKept          = 5
)

// moderationAnswer is OpenAI's moderation response. Categories are maps, so
// categories added later are kept.
type moderationAnswer struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Results []moderationResult `json:"results"`
}

type moderationResult struct {
	Flagged                   *bool               `json:"flagged"`
	Categories                map[string]bool     `json:"categories"`
	CategoryScores            map[string]float64  `json:"category_scores"`
	CategoryAppliedInputTypes map[string][]string `json:"category_applied_input_types"`
}

// moderationCall is one request to the moderation endpoint: text pieces, or
// a single image.
type moderationCall struct {
	pieces []*piece
	image  *imageCandidate
}

func (call *moderationCall) inputs() int {
	if call.image != nil {
		return 1
	}
	return len(call.pieces)
}

// callOutcome is what one call returned. A call that never ran (a flag of
// another call cancelled it first) has ran false and no failure.
type callOutcome struct {
	ran       bool
	answer    *moderationAnswer
	failure   *relaycommon.ModerationAuditError
	requestID string
}

// moderationClient sends the calls of one attempt with the attempt's key,
// organization, header overrides and proxy.
type moderationClient struct {
	c        *gin.Context
	info     *relaycommon.RelayInfo
	url      string
	http     *http.Client
	setupErr *relaycommon.ModerationAuditError // the proxy client could not be built
}

func newModerationClient(c *gin.Context, info *relaycommon.RelayInfo) *moderationClient {
	client := &moderationClient{c: c, info: info, url: moderationURL(info)}
	base, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		client.setupErr = client.failure(relaycommon.ModerationErrorClassNetwork, 0, err.Error())
		return client
	}
	noRedirects := *base
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.http = &noRedirects
	return client
}

func moderationURL(info *relaycommon.RelayInfo) string {
	return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, "/v1/moderations", info.ChannelType)
}

// moderationEndpoint is the moderation URL lower-cased without its query:
// the part of a verdict's key that names the service that gave it.
func moderationEndpoint(info *relaycommon.RelayInfo) string {
	endpoint, _, _ := strings.Cut(moderationURL(info), "?")
	return strings.ToLower(endpoint)
}

// do sends one call. A cancelled context is reported as a failure only when
// the attempt's deadline expired; the caller tells a flag's cancellation and
// a client that left apart.
func (m *moderationClient) do(ctx context.Context, call *moderationCall) callOutcome {
	if m.setupErr != nil {
		return callOutcome{failure: m.setupErr}
	}
	var input any
	if call.image != nil {
		input = []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": call.image.url}}}
	} else {
		texts := make([]string, len(call.pieces))
		for i, p := range call.pieces {
			texts[i] = p.text
		}
		input = texts
	}
	body, err := common.Marshal(map[string]any{"model": dto.ModerationModel, "input": input})
	if err != nil {
		return callOutcome{failure: m.failure(relaycommon.ModerationErrorClassConfig, 0, "invalid moderation input")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return callOutcome{failure: m.failure(relaycommon.ModerationErrorClassNetwork, 0, err.Error())}
	}
	req.Header.Set("Authorization", "Bearer "+m.info.ApiKey)
	if m.info.Organization != "" {
		req.Header.Set("OpenAI-Organization", m.info.Organization)
	}
	if err := channel.ApplyHeaderOverride(m.info, m.c, req); err != nil {
		return callOutcome{failure: m.failure(relaycommon.ModerationErrorClassConfig, 0, "invalid header override")}
	}
	// A "*" passthrough rule may have copied the client's own content headers.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Del("Content-Encoding")

	resp, err := m.http.Do(req)
	if err != nil {
		return callOutcome{ran: true, failure: m.transportFailure(ctx, err)}
	}
	defer resp.Body.Close()
	outcome := callOutcome{ran: true, requestID: resp.Header.Get("x-request-id")}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, answerMaxBytes+1))
	if err != nil {
		outcome.failure = m.transportFailure(ctx, err)
		return outcome
	}
	if resp.StatusCode != http.StatusOK {
		outcome.failure = m.statusFailure(resp.StatusCode, raw, call.image != nil)
		return outcome
	}
	answer, reason := parseAnswer(raw, call.inputs())
	if reason != "" {
		outcome.failure = m.failure(relaycommon.ModerationErrorClassDecode, http.StatusOK, reason)
		return outcome
	}
	outcome.answer = answer
	return outcome
}

func (m *moderationClient) transportFailure(ctx context.Context, err error) *relaycommon.ModerationAuditError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return m.failure(relaycommon.ModerationErrorClassTimeout, 0, "the moderation call timed out")
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return &relaycommon.ModerationAuditError{Class: errCancelled}
	}
	return m.failure(relaycommon.ModerationErrorClassNetwork, 0, err.Error())
}

// errCancelled marks a call cancelled by a flag of another call or by the
// client leaving; it never reaches an audit.
const errCancelled = "cancelled"

var tpmLimitPattern = regexp.MustCompile(`Limit (\d+), Requested (\d+)`)

func (m *moderationClient) statusFailure(status int, raw []byte, image bool) *relaycommon.ModerationAuditError {
	var errorBody struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	message := cutRunes(string(raw), failureMessageRunes)
	code, errorType := "", ""
	if common.Unmarshal(raw, &errorBody) == nil && errorBody.Error != nil {
		message = errorBody.Error.Message
		errorType = errorBody.Error.Type
		code = common.Interface2String(errorBody.Error.Code)
	}
	class := relaycommon.ModerationErrorClassHTTPOther
	switch {
	case status == http.StatusUnauthorized:
		class = relaycommon.ModerationErrorClassHTTP401
	case status == http.StatusForbidden:
		class = relaycommon.ModerationErrorClassHTTP403
	case status == http.StatusNotFound:
		class = relaycommon.ModerationErrorClassHTTP404
	case status == http.StatusTooManyRequests && (code == "insufficient_quota" || errorType == "insufficient_quota"):
		class = relaycommon.ModerationErrorClassHTTP429Quota
	case status == http.StatusTooManyRequests && requestLargerThanLimit(message):
		class = relaycommon.ModerationErrorClassHTTP429Size
	case status == http.StatusTooManyRequests:
		class = relaycommon.ModerationErrorClassHTTP429Rate
	case image && status == http.StatusBadRequest || strings.HasPrefix(code, "invalid_image"):
		class = relaycommon.ModerationErrorClassImageRejected
	case status >= 500 && status <= 599:
		class = relaycommon.ModerationErrorClassHTTP5xx
	}
	return m.failure(class, status, message)
}

// requestLargerThanLimit reports a 429 for one request larger than the
// organization's moderation tokens per minute, which no retry can pass.
func requestLargerThanLimit(message string) bool {
	match := tpmLimitPattern.FindStringSubmatch(message)
	if match == nil {
		return false
	}
	limit, err1 := strconv.Atoi(match[1])
	requested, err2 := strconv.Atoi(match[2])
	return err1 == nil && err2 == nil && requested > limit
}

// failure builds the audit of a failed call; its message never holds the key
// or the channel's base URL.
func (m *moderationClient) failure(class string, status int, message string) *relaycommon.ModerationAuditError {
	if m.info.ApiKey != "" {
		message = strings.ReplaceAll(message, m.info.ApiKey, "***")
	}
	message = common.MaskSensitiveInfo(message, m.info.ChannelBaseUrl)
	return &relaycommon.ModerationAuditError{Class: class, Status: status, Message: cutRunes(message, failureMessageRunes)}
}

// parseAnswer accepts only a complete answer: one result per input, each
// with its flag and the categories OpenAI always returns.
func parseAnswer(raw []byte, inputs int) (*moderationAnswer, string) {
	if len(raw) > answerMaxBytes {
		return nil, "the moderation answer is larger than 1 MiB"
	}
	var answer moderationAnswer
	if err := common.Unmarshal(raw, &answer); err != nil {
		return nil, "the moderation answer is not JSON: " + cutRunes(string(raw), 100)
	}
	switch {
	case len(answer.Results) == 0:
		return nil, "the moderation answer has no results"
	case len(answer.Results) != inputs:
		return nil, "the moderation answer has " + strconv.Itoa(len(answer.Results)) + " results for " + strconv.Itoa(inputs) + " inputs"
	}
	for _, result := range answer.Results {
		if result.Flagged == nil || len(result.Categories) == 0 {
			return nil, "a moderation result has no flag or no categories"
		}
	}
	return &answer, ""
}

// verdictOf keeps what a result says: the flag, the true categories, the
// five highest scores and the input types of the true categories.
func verdictOf(result moderationResult, model string) Verdict {
	verdict := Verdict{Flagged: *result.Flagged, Model: model}
	for category, on := range result.Categories {
		if on {
			verdict.Categories = append(verdict.Categories, category)
			if types := result.CategoryAppliedInputTypes[category]; len(types) > 0 {
				if verdict.AppliedInputTypes == nil {
					verdict.AppliedInputTypes = make(map[string][]string)
				}
				verdict.AppliedInputTypes[category] = types
			}
		}
	}
	slices.Sort(verdict.Categories)
	verdict.Scores = topScores(result.CategoryScores)
	return verdict
}

// topScores keeps the five highest scores, rounded to 4 decimals.
func topScores(scores map[string]float64) map[string]float64 {
	if len(scores) == 0 {
		return nil
	}
	names := make([]string, 0, len(scores))
	for name := range scores {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		if scores[a] != scores[b] {
			if scores[a] > scores[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	kept := make(map[string]float64, min(len(names), scoresKept))
	for _, name := range names[:min(len(names), scoresKept)] {
		kept[name] = math.Round(scores[name]*10000) / 10000
	}
	return kept
}

// marshalledLen is the length of text as a JSON string, for the body bound
// of a call.
func marshalledLen(text string) int {
	if b, err := common.Marshal(text); err == nil {
		return len(b)
	}
	return utf8.RuneCountInString(text) * 6
}
