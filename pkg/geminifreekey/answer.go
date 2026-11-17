package geminifreekey

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// GoogleError is the error object of a Google API answer.
type GoogleError struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Status  string              `json:"status"`
	Details []GoogleErrorDetail `json:"details"`
}

// GoogleErrorDetail is an entry of GoogleError.Details; only the fields of
// QuotaFailure, RetryInfo and ErrorInfo are read.
type GoogleErrorDetail struct {
	Type       string           `json:"@type"`
	Violations []QuotaViolation `json:"violations"`
	RetryDelay string           `json:"retryDelay"`
	Reason     string           `json:"reason"`
}

// QuotaViolation is a QuotaFailure violation. QuotaValue is an int64, which
// Google writes as a string, and leaves out when it is 0.
type QuotaViolation struct {
	QuotaMetric     string            `json:"quotaMetric"`
	QuotaID         string            `json:"quotaId"`
	QuotaDimensions map[string]string `json:"quotaDimensions"`
	QuotaValue      json.RawMessage   `json:"quotaValue"`
}

// ParseGoogleError reads the error of a Google answer body: an object
// {"error": {...}}, or the array [{"error": {...}}] that streaming and the
// OpenAI-compatible endpoints answer with.
func ParseGoogleError(body []byte) (GoogleError, bool) {
	type envelope struct {
		Error *GoogleError `json:"error"`
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var list []envelope
		if common.Unmarshal(trimmed, &list) != nil || len(list) == 0 || list[0].Error == nil {
			return GoogleError{}, false
		}
		return *list[0].Error, true
	}
	var single envelope
	if common.Unmarshal(trimmed, &single) != nil || single.Error == nil {
		return GoogleError{}, false
	}
	return *single.Error, true
}

// violations are the QuotaFailure violations of e.
func (e GoogleError) violations() []QuotaViolation {
	var all []QuotaViolation
	for _, detail := range e.Details {
		if strings.HasSuffix(detail.Type, "google.rpc.QuotaFailure") {
			all = append(all, detail.Violations...)
		}
	}
	return all
}

// quotaModels are the models e's violations name.
func (e GoogleError) quotaModels() []string {
	var models []string
	for _, v := range e.violations() {
		if model := strings.TrimSpace(v.QuotaDimensions["model"]); model != "" {
			models = append(models, model)
		}
	}
	return models
}

// quotaValue is the violation's limit, and whether it has one above 0.
func (v QuotaViolation) quotaValue() (int64, bool) {
	text := strings.Trim(strings.TrimSpace(string(v.QuotaValue)), `"`)
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil && value > 0
}

var (
	// retryInPattern is the delay at the end of Google's quota messages:
	// "Please retry in 14h7m39.282233548s." or "... in 293.298519ms."
	retryInPattern = regexp.MustCompile(`(?i)retry in ((?:[0-9]+(?:\.[0-9]+)?(?:h|ms|us|µs|ns|m|s))+)`)
	// limitZeroPattern is a quota line of a model the key's tier does not
	// serve: "limit: 0," or "limit: 0" at the end.
	limitZeroPattern = regexp.MustCompile(`limit: 0(?:[^0-9]|$)`)
)

const (
	minuteRetryMin     = time.Second
	minuteRetryMax     = 10 * time.Minute
	minuteRetryDefault = time.Minute
	// dayResetSlack is how long before the next Pacific midnight Google's
	// retry delay for a per-day quota may end and still mean that midnight:
	// Google gives the delay in whole seconds, and clocks differ a little.
	dayResetSlack = time.Minute
)

// Pause is how a key pauses for its bucket after Google's 429.
type Pause struct {
	Reason Reason
	Until  time.Time
	// Models are the models Google names in the quota it reports, which may
	// count in another bucket than the request's.
	Models []string
}

// RateLimitPause reads a 429 body Google answered at now:
//   - every quota line with "limit: 0", or every violation without a
//     quotaValue: the model is not on the key's free tier (ReasonNotFree),
//     until the next Pacific midnight;
//   - a violation of a per-day quota: ReasonDay, until the next Pacific
//     midnight, or only for Google's retry delay when that ends well before
//     it: Google's day has not ended yet although ours has (its reset came
//     late, or the request was answered across midnight);
//   - a violation of a per-minute quota: ReasonMinute, for Google's retry
//     delay (RetryInfo, else the message's "retry in", else a minute) within
//     a second and ten minutes;
//   - no details at all: ReasonUnknown for a minute; details without a quota
//     the same, for Google's retry delay.
func RateLimitPause(body []byte, now time.Time) Pause {
	googleError, ok := ParseGoogleError(body)
	if ok && limitZeroPattern.MatchString(googleError.Message) {
		return Pause{Reason: ReasonNotFree, Until: NextReset(now), Models: googleError.quotaModels()}
	}
	if !ok || len(googleError.Details) == 0 {
		return Pause{Reason: ReasonUnknown, Until: now.Add(minuteRetryDefault)}
	}
	violations := googleError.violations()
	pause := Pause{Models: googleError.quotaModels()}
	withoutValue := len(violations) > 0
	perDay, perMinute := false, false
	for _, v := range violations {
		if _, hasValue := v.quotaValue(); hasValue {
			withoutValue = false
		}
		perDay = perDay || strings.Contains(v.QuotaID, "PerDay")
		perMinute = perMinute || strings.Contains(v.QuotaID, "PerMinute")
	}
	switch {
	case withoutValue:
		pause.Reason, pause.Until = ReasonNotFree, NextReset(now)
	case perDay:
		pause.Reason, pause.Until = ReasonDay, NextReset(now)
		if delay, given := googleRetryDelay(googleError); given && now.Add(delay).Before(pause.Until.Add(-dayResetSlack)) {
			pause.Until = now.Add(max(delay, minuteRetryMin))
		}
	case perMinute:
		pause.Reason, pause.Until = ReasonMinute, now.Add(retryDelay(googleError))
	default:
		pause.Reason, pause.Until = ReasonUnknown, now.Add(retryDelay(googleError))
	}
	return pause
}

// googleRetryDelay is the retry delay Google gives: RetryInfo's, else the
// message's "retry in", and whether it gives one above 0.
func googleRetryDelay(e GoogleError) (time.Duration, bool) {
	for _, detail := range e.Details {
		if strings.HasSuffix(detail.Type, "google.rpc.RetryInfo") {
			if parsed, err := time.ParseDuration(strings.TrimSpace(detail.RetryDelay)); err == nil && parsed > 0 {
				return parsed, true
			}
		}
	}
	if match := retryInPattern.FindStringSubmatch(e.Message); match != nil {
		if parsed, err := time.ParseDuration(match[1]); err == nil && parsed > 0 {
			return parsed, true
		}
	}
	return 0, false
}

// retryDelay is Google's retry delay (googleRetryDelay), else a minute,
// within a second and ten minutes.
func retryDelay(e GoogleError) time.Duration {
	delay, given := googleRetryDelay(e)
	if !given {
		return minuteRetryDefault
	}
	return min(max(delay, minuteRetryMin), minuteRetryMax)
}

// keyRejectionReasons are the ErrorInfo reasons of answers about the key or
// its project rather than the request.
var keyRejectionReasons = []string{
	"API_KEY_INVALID",
	"API_KEY_EXPIRED",
	"API_KEY_SERVICE_BLOCKED",
	"API_KEY_HTTP_REFERRER_BLOCKED",
	"API_KEY_IP_ADDRESS_BLOCKED",
	"API_KEY_ANDROID_APP_BLOCKED",
	"API_KEY_IOS_APP_BLOCKED",
	"CONSUMER_SUSPENDED",
	"CONSUMER_INVALID",
	"SERVICE_DISABLED",
	"ACCESS_DENIED",
}

// keyRejectionPhrases, lower case, are the messages of those answers.
var keyRejectionPhrases = []string{
	"api key not valid",
	"api key expired",
	"has been suspended",
	"denied access",
	"permission denied on the service",
	freeTierUnavailablePhrase,
}

// freeTierUnavailablePhrase, lower case, is Google's answer for a key whose
// project has no free tier at all: "The Gemini API free tier is not
// available for this project. To use the Gemini API, please set up billing
// to upgrade to the paid tier …". Unlike a free tier closed to the caller's
// country, it is about the key's project and lasts until billing is set up.
const freeTierUnavailablePhrase = "free tier is not available for this project"

// IsFreeTierUnavailable tells Google's 400 or 403 for a key whose project has
// no free tier. Such a key can never serve in free key mode, so the relay
// disables it rather than letting it sit out and come back.
func IsFreeTierUnavailable(status int, body []byte) bool {
	if status != 400 && status != 403 {
		return false
	}
	message := string(body)
	if googleError, ok := ParseGoogleError(body); ok && googleError.Message != "" {
		message = googleError.Message
	}
	return strings.Contains(strings.ToLower(message), freeTierUnavailablePhrase)
}

// IsKeyRejection tells Google's answers that reject the key itself: any
// 401, and a 400 or 403 that names the key or its project (an invalid,
// expired, blocked or suspended key, a project denied access). A 403 about
// the request, such as a file the key may not read, is not one.
func IsKeyRejection(status int, body []byte) bool {
	switch status {
	case 401:
		return true
	case 400, 403:
	default:
		return false
	}
	googleError, ok := ParseGoogleError(body)
	if !ok {
		return false
	}
	for _, detail := range googleError.Details {
		if strings.HasSuffix(detail.Type, "google.rpc.ErrorInfo") {
			for _, reason := range keyRejectionReasons {
				if strings.EqualFold(detail.Reason, reason) {
					return true
				}
			}
		}
	}
	message := strings.ToLower(googleError.Message)
	for _, phrase := range keyRejectionPhrases {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// freeTierGiveaways, lower case, are the parts of Google's answers that tell
// a key is on the free tier.
var freeTierGiveaways = []string{
	"free_tier",
	"freetier",
	"free tier",
	"please check your plan and billing details",
	"please enable billing",
	"ai.dev/rate-limit",
	"ai.dev/usage",
	"aistudio.google.com/docs/rate-limits",
	"ai.google.dev/gemini-api/docs/rate-limits",
	"precondition check failed",
	"no longer available to new users",
	"consumer 'api_key:",
}

// RevealsFreeTier tells whether text, an answer of Google or the error made
// from it, shows that the key is on the free tier.
func RevealsFreeTier(text string) bool {
	lower := strings.ToLower(text)
	for _, giveaway := range freeTierGiveaways {
		if strings.Contains(lower, giveaway) {
			return true
		}
	}
	return limitZeroPattern.MatchString(lower)
}
