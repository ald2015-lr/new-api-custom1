package common

import "github.com/QuantumNous/new-api/constant"

// SupportsOpenAISafetyFeatures reports whether a channel type can use the
// OpenAI safety features: content moderation, its probe and the gateway
// safety identifier. Only the OpenAI type (1) can.
func SupportsOpenAISafetyFeatures(channelType int) bool {
	return channelType == constant.ChannelTypeOpenAI
}

// Statuses of a content moderation audit.
const (
	ModerationStatusPassed   = "passed"
	ModerationStatusFailOpen = "fail_open"
	ModerationStatusRejected = "rejected"
)

// Sources of a content moderation rejection.
const (
	ModerationSourceOpenAI      = "openai"
	ModerationSourceRule        = "rule"
	ModerationSourceUnavailable = "unavailable"
	ModerationSourceUnsupported = "unsupported"
)

// Details of an unsupported request.
const (
	ModerationDetailRealtime      = "realtime"
	ModerationDetailNovelAI       = "novelai"
	ModerationDetailRequestType   = "request_type"
	ModerationDetailTokenPrompt   = "token_prompt"
	ModerationDetailAmbiguousKeys = "ambiguous_keys"
)

// Classes of a failed moderation call.
const (
	ModerationErrorClassConfig        = "config"
	ModerationErrorClassTimeout       = "timeout"
	ModerationErrorClassNetwork       = "network"
	ModerationErrorClassHTTP401       = "http_401"
	ModerationErrorClassHTTP403       = "http_403"
	ModerationErrorClassHTTP404       = "http_404"
	ModerationErrorClassHTTP429Quota  = "http_429_quota"
	ModerationErrorClassHTTP429Size   = "http_429_size"
	ModerationErrorClassHTTP429Rate   = "http_429_rate"
	ModerationErrorClassImageRejected = "image_rejected"
	ModerationErrorClassHTTP5xx       = "http_5xx"
	ModerationErrorClassHTTPOther     = "http_other"
	ModerationErrorClassDecode        = "decode"
)

// Kinds of the rule that rejected a request.
const (
	ModerationRuleKindPreset = "preset"
	ModerationRuleKindCustom = "custom"
)

// ModerationAudit is what content moderation did on one attempt, stored under
// admin_info.moderation of that attempt's log row.
type ModerationAudit struct {
	ChannelID         int                   `json:"channel_id"`
	RetryIndex        int                   `json:"retry_index"`
	Status            string                `json:"status"`           // ModerationStatus*
	Source            string                `json:"source,omitempty"` // rejected only: ModerationSource*
	Detail            string                `json:"detail,omitempty"` // unsupported only: ModerationDetail*
	Model             string                `json:"model,omitempty"`  // as OpenAI echoed it
	ID                string                `json:"id,omitempty"`     // modr-…
	Categories        []string              `json:"categories,omitempty"`
	Scores            map[string]float64    `json:"scores,omitempty"`
	AppliedInputTypes map[string][]string   `json:"applied_input_types,omitempty"`
	Rule              *ModerationAuditRule  `json:"rule,omitempty"`
	Match             string                `json:"match,omitempty"` // ≤ 64 runes, rule hits only
	Error             *ModerationAuditError `json:"error,omitempty"` // fail_open and unavailable
	ElapsedMs         int64                 `json:"elapsed_ms"`
	CheckedChars      int                   `json:"checked_chars"`
	Truncated         bool                  `json:"truncated,omitempty"`
	RuleScanTruncated bool                  `json:"rule_scan_truncated,omitempty"`
	Pieces            int                   `json:"pieces,omitempty"` // text pieces sent on this attempt
	CachedPieces      int                   `json:"cached_pieces,omitempty"`
	Calls             int                   `json:"calls"` // moderation requests made on this attempt; 0 for a cached flag
	ImagesChecked     int                   `json:"images_checked,omitempty"`
	ImagesSkipped     int                   `json:"images_skipped,omitempty"`
	UncheckedParts    int                   `json:"unchecked_parts,omitempty"`     // parts seen but not extractable (files, audio, …)
	UpstreamRequestID string                `json:"upstream_request_id,omitempty"` // x-request-id of the moderation answer
	// The attempts of the request whose moderation call said the key or its
	// OpenAI account is unusable; on the request's rejection row only.
	KeyFailures []ModerationKeyFailure `json:"key_failures,omitempty"`
}

// ModerationKeyFailure is an attempt whose moderation call failed with
// ModerationErrorClassHTTP401, ModerationErrorClassHTTP403 or
// ModerationErrorClassHTTP429Quota on a channel that rejects on failure: the
// relay handled it as that channel's error (disabling, retry).
type ModerationKeyFailure struct {
	ChannelID     int    `json:"channel_id"`
	RetryIndex    int    `json:"retry_index"`
	MultiKeyIndex *int   `json:"multi_key_index,omitempty"` // multi-key channels only
	Class         string `json:"class"`
	Status        int    `json:"status,omitempty"`
}

// ModerationAuditRule names the rule that rejected a request.
type ModerationAuditRule struct {
	Kind  string `json:"kind"`            // ModerationRuleKind*
	ID    string `json:"id,omitempty"`    // preset id
	Index *int   `json:"index,omitempty"` // custom rule index, 0-based
	Name  string `json:"name,omitempty"`
	Type  string `json:"type"` // "keyword" | "regex"
}

// ModerationAuditError is the failure of a moderation call.
type ModerationAuditError struct {
	Class   string `json:"class"` // ModerationErrorClass*
	Status  int    `json:"status,omitempty"`
	Message string `json:"message,omitempty"` // masked, ≤ 200 runes
}
