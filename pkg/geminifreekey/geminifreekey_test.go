package geminifreekey

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerTPMSample is the owner's own free tier 429 for input tokens per
// minute, verbatim.
const ownerTPMSample = `{
  "error": {
    "code": 429,
    "message": "You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 250000, model: gemini-3.6-flash, description: Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 49.477199395s.",
    "status": "RESOURCE_EXHAUSTED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.Help",
        "links": [
          {
            "description": "Learn more about Gemini API quotas",
            "url": "https://aistudio.google.com/docs/rate-limits"
          }
        ]
      },
      {
        "@type": "type.googleapis.com/google.rpc.QuotaFailure",
        "violations": [
          {
            "description": "Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.",
            "quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_input_token_count",
            "quotaId": "GenerateContentInputTokensPerModelPerMinute-FreeTier",
            "quotaDimensions": {
              "location": "global",
              "model": "gemini-3.6-flash"
            },
            "quotaValue": "250000"
          }
        ]
      },
      {
        "@type": "type.googleapis.com/google.rpc.RetryInfo",
        "retryDelay": "49s"
      }
    ]
  }
}`

// Google's free tier 429 for requests per day, from the research notes.
const rpdSample = `{
  "error": {
    "code": 429,
    "message": "You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 20, model: gemini-3.6-flash, description: Generate content requests per day limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 14h7m39.282233548s.",
    "status": "RESOURCE_EXHAUSTED",
    "details": [
      {"@type": "type.googleapis.com/google.rpc.Help",
       "links": [{"description": "Learn more about Gemini API quotas", "url": "https://aistudio.google.com/docs/rate-limits"}]},
      {"@type": "type.googleapis.com/google.rpc.QuotaFailure",
       "violations": [{
         "description": "Generate content requests per day limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.",
         "quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests",
         "quotaId": "GenerateRequestsPerDayPerProjectPerModel-FreeTier",
         "quotaDimensions": {"model": "gemini-3.6-flash", "location": "global"},
         "quotaValue": "20"}]},
      {"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "50859s"}
    ]
  }
}`

// Google's free tier 429 for requests per minute.
const rpmSample = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 5, model: gemini-3.6-flash, description: Generate content requests per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 59.446356225s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more about Gemini API quotas","url":"https://aistudio.google.com/docs/rate-limits"}]},{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"description":"Generate content requests per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.","quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-3.6-flash","location":"global"},"quotaValue":"5"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"59s"}]}}`

// Google's 429 for a model the key's free tier does not serve: every
// violation at once, each without a quotaValue.
const limitZeroSample = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://aistudio.google.com/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 0, model: gemini-2.5-pro, description: Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-2.5-pro, description: Generate content requests per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-2.5-pro, description: Generate content requests per day limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 0, model: gemini-2.5-pro, description: Generate content input tokens per day limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.\nPlease retry in 1h59m22.929891566s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more about Gemini API quotas","url":"https://aistudio.google.com/docs/rate-limits"}]},{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"description":"Generate content input tokens per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.","quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_input_token_count","quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaDimensions":{"model":"gemini-2.5-pro","location":"global"}},{"description":"Generate content requests per minute limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.","quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-2.5-pro","location":"global"}},{"description":"Generate content requests per day limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.","quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-2.5-pro","location":"global"}},{"description":"Generate content input tokens per day limit exceeded on Free Tier. See https://aistudio.google.com/docs/rate-limits.","quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_input_token_count","quotaId":"GenerateContentInputTokensPerModelPerDay-FreeTier","quotaDimensions":{"location":"global","model":"gemini-2.5-pro"}}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7162s"}]}}`

// The same through the OpenAI-compatible endpoint: an array wrapper, the
// earlier message format without descriptions.
const arrayWrappedSample = `[{
  "error": {
    "code": 429,
    "message": "You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 0, model: gemini-2.5-pro\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-2.5-pro\nPlease retry in 55.905378193s.",
    "status": "RESOURCE_EXHAUSTED",
    "details": [
      {"@type": "type.googleapis.com/google.rpc.Help", "links": [{"description": "Learn more about Gemini API quotas", "url": "https://ai.google.dev/gemini-api/docs/rate-limits"}]},
      {"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": [
        {"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_input_token_count", "quotaId": "GenerateContentInputTokensPerModelPerDay-FreeTier", "quotaDimensions": {"model": "gemini-2.5-pro", "location": "global"}},
        {"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests", "quotaId": "GenerateRequestsPerMinutePerProjectPerModel-FreeTier", "quotaDimensions": {"model": "gemini-2.5-pro", "location": "global"}}
      ]},
      {"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "55s"}
    ]
  }
}
]`

// The 2025 format, without details.
const noDetailsSample = `{"error": {"code": 429, "message": "You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits.", "status": "RESOURCE_EXHAUSTED"}}`

// A paid tier 429 for input tokens per minute, without RetryInfo.
const paidTierSample = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits. To monitor your current usage, head to: https://ai.dev/rate-limit. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_paid_tier_input_token_count, limit: 1000000, model: gemini-3-flash\nPlease retry in 26.083945754s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more about Gemini API quotas","url":"https://ai.google.dev/gemini-api/docs/rate-limits"}]},{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_paid_tier_input_token_count","quotaId":"GenerateContentPaidTierInputTokensPerModelPerMinute","quotaDimensions":{"model":"gemini-3-flash","location":"global"},"quotaValue":"1000000"}]}]}}`

// Google's plain rate limit.
const plainSample = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`

var (
	pst = time.FixedZone("PST", -8*60*60)
	pdt = time.FixedZone("PDT", -7*60*60)
)

// fakeClock is a settable time for a limiter.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestLimiter(start time.Time) (*Limiter, *fakeClock) {
	clock := &fakeClock{now: start}
	return New(clock.Now), clock
}

func testKeys(n int) []Key {
	keys := make([]Key, n)
	for i := range keys {
		keys[i] = Key{Index: i, Secret: fmt.Sprintf("AIzaSyTestKey%02d-0123456789abcdefghijklmn", i)}
	}
	return keys
}

func testPool(keys []Key, rpm, tpm, rpd int) Pool {
	rows := []dto.GeminiFreeKeyModelLimit{{Model: "gemini-test-flash", RPM: rpm, TPM: tpm, RPD: rpd}}
	return Pool{ChannelID: 1, Keys: keys, Bucket: ResolveBucket(rows, "gemini-test-flash"), Rows: rows}
}

func TestResolveBucket(t *testing.T) {
	rows := dto.DefaultGeminiFreeKeyModelLimits()
	for _, tc := range []struct {
		model, bucket string
		listed        bool
		limits        Limits
	}{
		{"gemini-2.5-flash", "gemini-2.5-flash", true, Limits{5, 250000, 20}},
		{" Models/GEMINI-2.5-Flash ", "gemini-2.5-flash", true, Limits{5, 250000, 20}},
		{"gemini-3.5-flash", "gemini-3.6-flash", true, Limits{5, 250000, 20}},
		{"gemma-4-26b", "gemma-4-26b-a4b-it", true, Limits{30, 16000, 14400}},
		{"gemini-2.5-flash-lite-preview-09-2025", "gemini-2.5-flash-lite", true, Limits{10, 250000, 20}},
		{"gemini-2.5-flash-thinking", "gemini-2.5-flash", true, Limits{5, 250000, 20}},
		{"gemini-3.1-flash-lite-preview", "gemini-3.1-flash-lite", true, Limits{15, 250000, 500}},
		{"gemini-3.1-flash-lite-image", "gemini-3.1-flash-lite-image", true, Limits{0, 0, 0}},
		{"gemini-3.1-flash-lite-image-preview", "gemini-3.1-flash-lite-image", true, Limits{0, 0, 0}},
		{"gemini-2.5-flash-native-audio-latest", "gemini-2.5-flash-native-audio-preview-09-2025", true, Limits{-1, 1000000, -1}},
		{"gemini-2.5-flashy", "gemini-2.5-flashy", false, Limits{-1, -1, -1}},
		{"imagen-4.0-generate-001", "imagen-4.0-generate-001", false, Limits{-1, -1, -1}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			bucket := ResolveBucket(rows, tc.model)
			assert.Equal(t, Bucket{ID: tc.bucket, Listed: tc.listed, Limits: tc.limits}, bucket)
			assert.Equal(t, tc.limits.RPM == 0, bucket.NotOnFreeTier())
		})
	}
}

// The free tier's day is a day in Los Angeles, 23 or 25 hours long across
// the daylight saving changes.
func TestPacificDayAndNextReset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		at    time.Time
		day   string
		reset time.Time
	}{
		{"winter evening", time.Date(2026, 1, 15, 23, 59, 0, 0, pst), "2026-01-15", time.Date(2026, 1, 16, 0, 0, 0, 0, pst)},
		{"spring forward day", time.Date(2026, 3, 8, 1, 30, 0, 0, pst), "2026-03-08", time.Date(2026, 3, 9, 0, 0, 0, 0, pdt)},
		{"after spring forward", time.Date(2026, 3, 8, 23, 59, 0, 0, pdt), "2026-03-08", time.Date(2026, 3, 9, 0, 0, 0, 0, pdt)},
		{"fall back day", time.Date(2026, 11, 1, 0, 30, 0, 0, pdt), "2026-11-01", time.Date(2026, 11, 2, 0, 0, 0, 0, pst)},
		{"after fall back", time.Date(2026, 11, 1, 23, 30, 0, 0, pst), "2026-11-01", time.Date(2026, 11, 2, 0, 0, 0, 0, pst)},
		{"Beijing 16:00 in winter", time.Date(2026, 11, 16, 16, 0, 0, 0, time.FixedZone("CST", 8*60*60)), "2026-11-16", time.Date(2026, 11, 17, 0, 0, 0, 0, pst)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.day, PacificDay(tc.at))
			assert.True(t, tc.reset.Equal(NextReset(tc.at)), "%s != %s", tc.reset, NextReset(tc.at))
		})
	}
}

// Requests and input tokens per minute count the reservations of the last
// 60 seconds, requests per day those of the Pacific day.
func TestLimiterWindows(t *testing.T) {
	t.Run("requests per minute", func(t *testing.T) {
		start := time.Date(2026, 1, 15, 10, 0, 0, 0, pst)
		limiter, clock := newTestLimiter(start)
		pool := testPool(testKeys(1), 2, -1, -1)
		for range 2 {
			ticket, _ := limiter.Reserve(pool, 10, 1, nil)
			require.NotNil(t, ticket)
		}
		ticket, report := limiter.Reserve(pool, 10, 1, nil)
		assert.Nil(t, ticket)
		assert.Equal(t, Report{Keys: 1, RPMFull: 1}, report)
		clock.Set(start.Add(59 * time.Second))
		ticket, _ = limiter.Reserve(pool, 10, 1, nil)
		assert.Nil(t, ticket)
		clock.Set(start.Add(60 * time.Second))
		ticket, _ = limiter.Reserve(pool, 10, 1, nil)
		assert.NotNil(t, ticket)
	})
	t.Run("input tokens per minute", func(t *testing.T) {
		start := time.Date(2026, 1, 15, 10, 0, 0, 0, pst)
		limiter, clock := newTestLimiter(start)
		pool := testPool(testKeys(1), -1, 100, -1)
		ticket, _ := limiter.Reserve(pool, 60, 1, nil)
		require.NotNil(t, ticket)
		ticket, report := limiter.Reserve(pool, 41, 1, nil)
		assert.Nil(t, ticket)
		assert.Equal(t, Report{Keys: 1, TPMFull: 1}, report)
		ticket, _ = limiter.Reserve(pool, 40, 1, nil)
		require.NotNil(t, ticket)
		clock.Add(30 * time.Second)
		ticket, _ = limiter.Reserve(pool, 1, 1, nil)
		assert.Nil(t, ticket)
		clock.Add(30 * time.Second)
		ticket, _ = limiter.Reserve(pool, 100, 1, nil)
		assert.NotNil(t, ticket)
	})
	t.Run("a batch counts each item", func(t *testing.T) {
		limiter, _ := newTestLimiter(time.Date(2026, 1, 15, 10, 0, 0, 0, pst))
		pool := testPool(testKeys(1), 5, -1, 8)
		ticket, _ := limiter.Reserve(pool, 0, 4, nil)
		require.NotNil(t, ticket)
		ticket, report := limiter.Reserve(pool, 0, 2, nil)
		assert.Nil(t, ticket)
		assert.Equal(t, Report{Keys: 1, RPMFull: 1}, report)
		ticket, _ = limiter.Reserve(pool, 0, 1, nil)
		require.NotNil(t, ticket)
		assert.Equal(t, 5, limiter.Usage(pool.Keys, pool.Rows, []string{"gemini-test-flash"})[0].Models[0].RPDUsed)
	})
	for _, tc := range []struct {
		name                   string
		lastOfDay, firstOfNext time.Time
	}{
		{"requests per day restart at Pacific midnight", time.Date(2026, 1, 15, 23, 59, 59, 0, pst), time.Date(2026, 1, 16, 0, 0, 0, 0, pst)},
		{"on the 23-hour spring forward day", time.Date(2026, 3, 8, 23, 59, 0, 0, pdt), time.Date(2026, 3, 9, 0, 0, 0, 0, pdt)},
		{"on the 25-hour fall back day", time.Date(2026, 11, 1, 23, 59, 0, 0, pst), time.Date(2026, 11, 2, 0, 0, 0, 0, pst)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Two requests at the start of the day, both still counted at its end.
			start := time.Date(tc.lastOfDay.Year(), tc.lastOfDay.Month(), tc.lastOfDay.Day(), 0, 30, 0, 0, time.UTC).Add(8 * time.Hour)
			limiter, clock := newTestLimiter(start)
			pool := testPool(testKeys(1), -1, -1, 2)
			for range 2 {
				ticket, _ := limiter.Reserve(pool, 0, 1, nil)
				require.NotNil(t, ticket)
			}
			clock.Set(tc.lastOfDay)
			ticket, report := limiter.Reserve(pool, 0, 1, nil)
			assert.Nil(t, ticket)
			assert.Equal(t, Report{Keys: 1, DayFull: 1}, report)
			assert.False(t, limiter.CanServe(pool))
			clock.Set(tc.firstOfNext)
			assert.True(t, limiter.CanServe(pool))
			ticket, _ = limiter.Reserve(pool, 0, 1, nil)
			assert.NotNil(t, ticket)
		})
	}
}

// -1 is no limit; a 0 limit is a model the free tier does not serve.
func TestLimiterUnlimitedAndZero(t *testing.T) {
	limiter, _ := newTestLimiter(time.Date(2026, 1, 15, 10, 0, 0, 0, pst))
	unlimited := testPool(testKeys(1), -1, -1, -1)
	for range 1000 {
		ticket, _ := limiter.Reserve(unlimited, 1_000_000, 1, nil)
		require.NotNil(t, ticket)
	}
	assert.True(t, limiter.CanServe(unlimited))
	for _, zero := range []Pool{testPool(testKeys(1), 0, 10, 10), testPool(testKeys(1), 10, 0, 10), testPool(testKeys(1), 10, 10, 0)} {
		assert.True(t, zero.Bucket.NotOnFreeTier())
		assert.False(t, limiter.CanServe(zero))
	}
}

// Google's 429 pauses the key for the model: a per-minute quota for its
// retry delay (within 1 s and 10 min), a per-day quota or a model the free
// tier does not serve until the next Pacific midnight, unless Google's day
// ends well before it.
func TestRateLimitPause(t *testing.T) {
	// When Google answered the per-day sample: its 50859 s end at midnight.
	now := time.Date(2026, 11, 16, 17, 52, 20, 0, time.UTC)
	midnight := time.Date(2026, 11, 17, 0, 0, 0, 0, pst)
	for _, tc := range []struct {
		name   string
		body   string
		reason Reason
		until  time.Time
		models []string
	}{
		{"owner's input tokens per minute", ownerTPMSample, ReasonMinute, now.Add(49 * time.Second), []string{"gemini-3.6-flash"}},
		{"requests per day", rpdSample, ReasonDay, midnight, []string{"gemini-3.6-flash"}},
		{"requests per minute", rpmSample, ReasonMinute, now.Add(59 * time.Second), []string{"gemini-3.6-flash"}},
		{"limit 0 on every quota", limitZeroSample, ReasonNotFree, midnight, []string{"gemini-2.5-pro", "gemini-2.5-pro", "gemini-2.5-pro", "gemini-2.5-pro"}},
		{"OpenAI-compatible array wrapper", arrayWrappedSample, ReasonNotFree, midnight, []string{"gemini-2.5-pro", "gemini-2.5-pro"}},
		{"old format without details", noDetailsSample, ReasonUnknown, now.Add(time.Minute), nil},
		{"plain rate limit", plainSample, ReasonUnknown, now.Add(time.Minute), nil},
		{"paid tier, delay from the message", paidTierSample, ReasonMinute, now.Add(26083945754 * time.Nanosecond), []string{"gemini-3-flash"}},
		{"not JSON", "upstream connect error", ReasonUnknown, now.Add(time.Minute), nil},
		{"a delay under a second", `{"error":{"code":429,"message":"* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 250000\nPlease retry in 293.298519ms.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaValue":"250000"}]}]}}`, ReasonMinute, now.Add(time.Second), nil},
		{"a delay over ten minutes", `{"error":{"code":429,"message":"x","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaValue":"5"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3600s"}]}}`, ReasonMinute, now.Add(10 * time.Minute), nil},
		{"violations without a quotaValue", `{"error":{"code":429,"message":"Resource has been exhausted.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-2.5-pro"}},{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier","quotaValue":"0"}]}]}}`, ReasonNotFree, midnight, []string{"gemini-2.5-pro"}},
		{"limit 0 in the message alone", `{"error":{"code":429,"message":"* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-2.5-pro\nPlease retry in 55s.","status":"RESOURCE_EXHAUSTED"}}`, ReasonNotFree, midnight, nil},
		{"per day, Google's delay a little short of midnight", dayQuotaAnswer("", "Please retry in 14h7m38s."), ReasonDay, midnight, nil},
		{"per day, Google's day ending later than ours", dayQuotaAnswer("1s", "Please retry in 1.282233548s."), ReasonDay, now.Add(time.Second), nil},
		{"per day, Google's delay from the message", dayQuotaAnswer("", "Please retry in 4m30s."), ReasonDay, now.Add(4*time.Minute + 30*time.Second), nil},
		{"per day, a delay under a second", dayQuotaAnswer("", "Please retry in 120ms."), ReasonDay, now.Add(time.Second), nil},
		{"per day without a delay", dayQuotaAnswer("", ""), ReasonDay, midnight, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pause := RateLimitPause([]byte(tc.body), now)
			assert.Equal(t, tc.reason, pause.Reason)
			assert.True(t, tc.until.Equal(pause.Until), "%s != %s", tc.until, pause.Until)
			assert.Equal(t, tc.models, pause.Models)
		})
	}
}

// dayQuotaAnswer is Google's per-day 429 for gemini-2.5-flash with the
// RetryInfo delay retryInfo and the message's ending retryIn (each left out
// when empty).
func dayQuotaAnswer(retryInfo, retryIn string) string {
	details := `{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"20"}]}`
	if retryInfo != "" {
		details += `,{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"` + retryInfo + `"}`
	}
	message := `You exceeded your current quota. \n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 20`
	if retryIn != "" {
		message += `\n` + retryIn
	}
	return `{"error":{"code":429,"message":"` + message + `","status":"RESOURCE_EXHAUSTED","details":[` + details + `]}}`
}

// Observe pauses the key for the request's bucket and for the bucket of the
// model Google names, and a daily pause shows the day as used up.
func TestObservePausesTheKey(t *testing.T) {
	start := time.Date(2026, 11, 16, 17, 52, 20, 0, time.UTC)
	limiter, clock := newTestLimiter(start)
	rows := dto.DefaultGeminiFreeKeyModelLimits()
	keys := testKeys(2)
	lite := Pool{ChannelID: 7, Keys: keys, Bucket: ResolveBucket(rows, "gemini-3.5-flash-lite"), Rows: rows}
	flash := Pool{ChannelID: 7, Keys: keys, Bucket: ResolveBucket(rows, "gemini-3.6-flash"), Rows: rows}

	verdict := limiter.Observe(lite, keys[0], 429, []byte(rpdSample))
	assert.Equal(t, OutcomeRateLimited, verdict.Outcome)
	assert.Equal(t, ReasonDay, verdict.Reason)
	assert.True(t, time.Date(2026, 11, 17, 0, 0, 0, 0, pst).Equal(verdict.Until))
	assert.Equal(t, Report{Keys: 2, PausedDay: 1, Ready: 1}, limiter.Inspect(lite, 0, 1, nil))
	assert.Equal(t, Report{Keys: 2, PausedDay: 1, Ready: 1}, limiter.Inspect(flash, 0, 1, nil), "Google named gemini-3.6-flash")
	usage := limiter.Usage(keys[:1], rows, []string{"gemini-3.6-flash"})
	require.Len(t, usage, 1)
	require.Len(t, usage[0].Models, 2, "the configured model and the one the key used")
	assert.Equal(t, "gemini-3.5-flash-lite", usage[0].Models[0].Model)
	assert.Equal(t, 500, usage[0].Models[0].RPDUsed, "a daily pause shows the day used up")
	assert.Equal(t, ReasonDay, usage[0].Models[0].CooldownReason)
	assert.Equal(t, "gemini-3.6-flash", usage[0].Models[1].Model)
	assert.Equal(t, 20, usage[0].Models[1].RPDUsed)

	limiter.Observe(lite, keys[1], 429, []byte(ownerTPMSample))
	assert.Equal(t, Report{Keys: 2, PausedDay: 1, PausedMinute: 1}, limiter.Inspect(flash, 0, 1, nil))
	assert.False(t, limiter.CanServe(flash))
	clock.Add(49 * time.Second)
	assert.Equal(t, Report{Keys: 2, PausedDay: 1, Ready: 1}, limiter.Inspect(flash, 0, 1, nil))

	// Midnight in Los Angeles is 16:00 in Beijing in winter.
	clock.Set(time.Date(2026, 11, 17, 16, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	assert.Equal(t, Report{Keys: 2, Ready: 2}, limiter.Inspect(flash, 0, 1, nil))
}

// A per-day 429 that Google answers just after Pacific midnight for its own
// day, which has not ended yet (a late reset, or a request sent before
// midnight), pauses the key only for Google's delay and leaves the new day's
// count as it is.
func TestDayPauseAfterMidnightFollowsGoogle(t *testing.T) {
	rows := dto.DefaultGeminiFreeKeyModelLimits()
	keys := testKeys(3)
	pool := Pool{ChannelID: 1, Keys: keys, Bucket: ResolveBucket(rows, "gemini-2.5-flash"), Rows: rows}

	t.Run("a late reset", func(t *testing.T) {
		limiter, clock := newTestLimiter(time.Date(2026, 11, 17, 0, 0, 0, 500_000_000, pst))
		for _, key := range keys {
			verdict := limiter.Observe(pool, key, 429, []byte(dayQuotaAnswer("1s", "Please retry in 1.282233548s.")))
			assert.Equal(t, ReasonDay, verdict.Reason)
		}
		assert.False(t, limiter.CanServe(pool))
		clock.Add(time.Second)
		assert.True(t, limiter.CanServe(pool), "Google said to retry in a second")
		usage := limiter.Usage(keys[:1], rows, []string{"gemini-2.5-flash"})
		assert.Zero(t, usage[0].Models[0].RPDUsed, "the new day is not used up")
	})

	t.Run("a request sent before midnight", func(t *testing.T) {
		limiter, clock := newTestLimiter(time.Date(2026, 11, 16, 23, 59, 59, 900_000_000, pst))
		ticket, _ := limiter.Reserve(pool, 10, 1, nil)
		require.NotNil(t, ticket)
		clock.Set(time.Date(2026, 11, 17, 0, 0, 0, 100_000_000, pst))
		verdict := ticket.Finish(429, []byte(dayQuotaAnswer("1s", "Please retry in 900ms.")))
		assert.True(t, time.Date(2026, 11, 17, 0, 0, 1, 100_000_000, pst).Equal(verdict.Until), verdict.Until.String())
		clock.Add(time.Second)
		ready, _ := limiter.Reserve(pool, 10, 1, nil)
		assert.NotNil(t, ready)
		usage := limiter.Usage(keys, rows, []string{"gemini-2.5-flash"})
		assert.Equal(t, 1, usage[0].Models[0].RPDUsed+usage[1].Models[0].RPDUsed+usage[2].Models[0].RPDUsed, "only the new request counts today")
	})

	t.Run("Google's delay a little short of our midnight", func(t *testing.T) {
		limiter, clock := newTestLimiter(time.Date(2026, 11, 16, 23, 59, 30, 0, pst))
		verdict := limiter.Observe(pool, keys[0], 429, []byte(dayQuotaAnswer("29s", "Please retry in 29.5s.")))
		midnight := time.Date(2026, 11, 17, 0, 0, 0, 0, pst)
		assert.True(t, midnight.Equal(verdict.Until), verdict.Until.String())
		usage := limiter.Usage(keys[:1], rows, []string{"gemini-2.5-flash"})
		assert.Equal(t, 20, usage[0].Models[0].RPDUsed, "the day shows used up")
		clock.Set(midnight)
		assert.Equal(t, Report{Keys: 3, Ready: 3}, limiter.Inspect(pool, 0, 1, nil))
	})
}

// A key Google rejects sits out every model for an hour; a 403 about the
// request does not.
// projectUnavailableSample is Google's answer for a key whose project has no
// free tier.
const projectUnavailableSample = `{"error":{"code":403,"message":"The Gemini API free tier is not available for this project. To use the Gemini API, please set up billing to upgrade to the paid tier at https://aistudio.google.com/billing. For more information, see https://ai.google.dev/gemini-api/docs/troubleshooting#project-unavailable.","status":"PERMISSION_DENIED"}}`

func TestKeyRejection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"invalid key", 400, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com","metadata":{"service":"generativelanguage.googleapis.com"}},{"@type":"type.googleapis.com/google.rpc.LocalizedMessage","locale":"en-US","message":"API key not valid. Please pass a valid API key."}]}}`, true},
		{"invalid key in the array wrapper", 400, `[{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}]`, true},
		{"unauthenticated", 401, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`, true},
		{"suspended", 403, `{"error":{"code":403,"message":"Permission denied: Consumer 'api_key:AIzaSyTest' has been suspended.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"CONSUMER_SUSPENDED","domain":"googleapis.com","metadata":{"consumer":"projects/123"}}]}}`, true},
		{"project denied access", 403, `{"error":{"code":403,"message":"Your project has been denied access. Please contact support.","status":"PERMISSION_DENIED"}}`, true},
		{"project without a free tier", 403, projectUnavailableSample, true},
		{"a file the key may not read", 403, `{"error":{"code":403,"message":"You do not have permission to access the File abc or it may not exist.","status":"PERMISSION_DENIED"}}`, false},
		{"bad request", 400, `{"error":{"code":400,"message":"* GenerateContentRequest.contents: contents is not specified\n","status":"INVALID_ARGUMENT"}}`, false},
		{"rate limit", 429, ownerTPMSample, false},
		{"server error", 500, `{"error":{"code":500,"message":"Internal error","status":"INTERNAL"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.rejected, IsKeyRejection(tc.status, []byte(tc.body)))
		})
	}

	for _, tc := range []struct {
		name        string
		status      int
		body        string
		unavailable bool
	}{
		{"project without a free tier", 403, projectUnavailableSample, true},
		{"the same answer as a 400", 400, projectUnavailableSample, true},
		{"a body that is not Google's JSON", 403, "The Gemini API free tier is not available for this project.", true},
		{"a 429 naming it", 429, projectUnavailableSample, false},
		{"free tier closed to the country", 400, `{"error":{"code":400,"message":"Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio.","status":"FAILED_PRECONDITION"}}`, false},
		{"suspended", 403, `{"error":{"code":403,"message":"Permission denied: Consumer 'api_key:AIzaSyTest' has been suspended.","status":"PERMISSION_DENIED"}}`, false},
	} {
		t.Run("free tier unavailable: "+tc.name, func(t *testing.T) {
			assert.Equal(t, tc.unavailable, IsFreeTierUnavailable(tc.status, []byte(tc.body)))
		})
	}

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, pst)
	limiter, clock := newTestLimiter(start)
	keys := testKeys(2)
	pool := testPool(keys, -1, -1, -1)
	other := pool
	other.Bucket = ResolveBucket(pool.Rows, "gemini-other")
	verdict := limiter.Observe(pool, keys[0], 401, nil)
	assert.Equal(t, Verdict{Outcome: OutcomeKeyRejected, Reason: ReasonKeyError, Until: start.Add(time.Hour)}, verdict)
	assert.Equal(t, Report{Keys: 2, KeyError: 1, Ready: 1}, limiter.Inspect(pool, 0, 1, nil))
	assert.Equal(t, Report{Keys: 2, KeyError: 1, Ready: 1}, limiter.Inspect(other, 0, 1, nil), "every model")
	assert.Equal(t, Verdict{Outcome: OutcomeOther}, limiter.Observe(pool, keys[1], 500, nil))
	assert.Equal(t, Verdict{Outcome: OutcomeAccepted}, limiter.Observe(pool, keys[1], 200, nil))
	clock.Add(time.Hour)
	assert.Equal(t, Report{Keys: 2, Ready: 2}, limiter.Inspect(pool, 0, 1, nil))
}

func TestRevealsFreeTier(t *testing.T) {
	for text, reveals := range map[string]bool{
		ownerTPMSample:  true,
		noDetailsSample: true,
		paidTierSample:  true,
		plainSample:     false,
		"Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio.": true,
		"Quota exceeded for metric: x, limit: 0, model: gemini-2.5-pro":                                                     true,
		"Quota exceeded for metric: x, limit: 0":                                                                            true,
		"limit: 05 requests":                                                                                                false,
		"Precondition check failed.":                                                                                        true,
		"Permission denied: Consumer 'api_key:AIzaSyTestKey00' has been suspended.":                                         true,
		"This model models/gemini-2.5-flash-lite is no longer available to new users.":                                      true,
		"This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later.": false,
	} {
		assert.Equal(t, reveals, RevealsFreeTier(text), text)
	}
}

// Among the keys with room, the one with the fewest requests this minute
// goes first, then the one with the fewest today; ties take turns by key
// index, or are drawn at random on a channel in random mode.
func TestLimiterChoosesTheKey(t *testing.T) {
	start := time.Date(2026, 1, 15, 10, 0, 0, 0, pst)
	reserve := func(t *testing.T, limiter *Limiter, pool Pool, tried ...KeyID) int {
		t.Helper()
		ticket, report := limiter.Reserve(pool, 1, 1, tried)
		require.NotNil(t, ticket, "%+v", report)
		return ticket.Key.Index
	}
	t.Run("fewest requests this minute, then today, then in turn", func(t *testing.T) {
		limiter, clock := newTestLimiter(start)
		keys := testKeys(3)
		pool := testPool(keys, 10, -1, 100)
		assert.Equal(t, []int{0, 1, 2, 0, 1, 2}, []int{reserve(t, limiter, pool), reserve(t, limiter, pool), reserve(t, limiter, pool), reserve(t, limiter, pool), reserve(t, limiter, pool), reserve(t, limiter, pool)})
		// Key 1 gets a third request; a minute later every window is empty
		// and the keys with fewer requests today go first.
		assert.Equal(t, 0, reserve(t, limiter, pool, IDOf(keys[1].Secret), IDOf(keys[2].Secret)))
		assert.Equal(t, 1, reserve(t, limiter, pool))
		clock.Add(time.Minute)
		assert.Equal(t, 2, reserve(t, limiter, pool), "key 2 had two requests today, the others three")
		assert.Equal(t, 0, reserve(t, limiter, pool))
		assert.Equal(t, 1, reserve(t, limiter, pool))
	})
	t.Run("the rotation is per channel and model", func(t *testing.T) {
		limiter, _ := newTestLimiter(start)
		pool := testPool(testKeys(3), 10, -1, -1)
		otherChannel, otherModel := pool, pool
		otherChannel.ChannelID = 2
		otherModel.Bucket = ResolveBucket(pool.Rows, "gemini-other")
		// Each reservation is taken back, so that only the rotation decides.
		turn := func(pool Pool) int {
			ticket, _ := limiter.Reserve(pool, 1, 1, nil)
			require.NotNil(t, ticket)
			ticket.Rollback()
			return ticket.Key.Index
		}
		assert.Equal(t, []int{0, 1, 0, 0, 2, 1, 0}, []int{turn(pool), turn(pool), turn(otherChannel), turn(otherModel), turn(pool), turn(otherChannel), turn(pool)})
	})
	t.Run("random mode draws among the ties", func(t *testing.T) {
		limiter, _ := newTestLimiter(start)
		var drawn []int
		limiter.intN = func(n int) int {
			drawn = append(drawn, n)
			return n - 1
		}
		pool := testPool(testKeys(3), 10, -1, -1)
		pool.Random = true
		assert.Equal(t, 2, reserve(t, limiter, pool))
		assert.Equal(t, 1, reserve(t, limiter, pool))
		assert.Equal(t, []int{3, 2}, drawn)
	})
	t.Run("Pick names the next key without taking it", func(t *testing.T) {
		limiter, _ := newTestLimiter(start)
		keys := testKeys(2)
		pool := testPool(keys, 1, -1, -1)
		key, ok := limiter.Pick(pool, nil)
		require.True(t, ok)
		assert.Equal(t, 0, key.Index)
		assert.Equal(t, 0, reserve(t, limiter, pool))
		key, ok = limiter.Pick(pool, nil)
		require.True(t, ok)
		assert.Equal(t, 1, key.Index)
		assert.Equal(t, 1, reserve(t, limiter, pool))
		_, ok = limiter.Pick(pool, nil)
		assert.True(t, ok, "a key at its requests per minute can still count tokens")
		_, ok = limiter.Pick(pool, []KeyID{IDOf(keys[0].Secret), IDOf(keys[1].Secret)})
		assert.False(t, ok)
	})
}

// A rollback takes the request back from every count; Finish keeps it.
func TestTicketRollback(t *testing.T) {
	limiter, _ := newTestLimiter(time.Date(2026, 1, 15, 10, 0, 0, 0, pst))
	pool := testPool(testKeys(1), 1, 100, 1)
	ticket, _ := limiter.Reserve(pool, 100, 1, nil)
	require.NotNil(t, ticket)
	assert.False(t, limiter.CanServe(pool))
	ticket.Rollback()
	ticket.Rollback()
	assert.Equal(t, Report{Keys: 1, Ready: 1}, limiter.Inspect(pool, 100, 1, nil))
	ticket, _ = limiter.Reserve(pool, 100, 1, nil)
	require.NotNil(t, ticket)
	assert.Equal(t, Verdict{Outcome: OutcomeOther}, ticket.Finish(503, nil))
	ticket.Rollback()
	assert.Equal(t, Report{Keys: 1, DayFull: 1}, limiter.Inspect(pool, 1, 1, nil), "an answered request stays counted")
}

// Concurrent requests never get more reservations than the limits allow.
func TestLimiterUnderConcurrentRequests(t *testing.T) {
	limiter, _ := newTestLimiter(time.Date(2026, 1, 15, 10, 0, 0, 0, pst))
	keys := testKeys(4)
	pool := testPool(keys, 5, -1, 3)
	var mu sync.Mutex
	perKey := map[int]int{}
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 8 {
				ticket, _ := limiter.Reserve(pool, 1, 1, nil)
				if ticket == nil {
					continue
				}
				mu.Lock()
				perKey[ticket.Key.Index]++
				mu.Unlock()
				limiter.Inspect(pool, 0, 1, nil)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, map[int]int{0: 3, 1: 3, 2: 3, 3: 3}, perKey)
}

// State that limits nothing any more is dropped.
func TestLimiterForgetsIdleState(t *testing.T) {
	start := time.Date(2026, 1, 15, 23, 0, 0, 0, pst)
	limiter, clock := newTestLimiter(start)
	keys := testKeys(50)
	pool := testPool(keys, -1, -1, -1)
	for range len(keys) {
		ticket, _ := limiter.Reserve(pool, 1, 1, nil)
		require.NotNil(t, ticket)
	}
	limiter.Observe(pool, keys[0], 401, nil)
	limiter.Observe(pool, keys[1], 429, []byte(rpmSample))
	assert.Len(t, limiter.usages, 51, "the keys' and the model Google named")
	clock.Add(2 * time.Hour)
	limiter.Inspect(pool, 0, 1, nil)
	assert.Empty(t, limiter.usages)
	assert.Empty(t, limiter.pauses)
	clock.Add(25 * time.Hour)
	limiter.Inspect(pool, 0, 1, nil)
	assert.Empty(t, limiter.cursors)
}

func TestKeyHint(t *testing.T) {
	assert.Equal(t, "…a1B2", KeyHint("AIzaSyExampleExampleExampleExamplea1B2"))
	assert.Equal(t, "…", KeyHint("short"))
}
