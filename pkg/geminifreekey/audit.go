package geminifreekey

// Audit is what a request did on a channel in free key mode, for the
// administrators' part of its log: the bucket, its input tokens and how
// they were counted, the key it ended on and every key it tried. It never
// holds a key.
type Audit struct {
	ChannelID  int    `json:"-"`
	RetryIndex int    `json:"-"`
	Bucket     string `json:"bucket"`
	// Tokens are the request's input tokens, counted by TokenSource:
	// "count_tokens" or "local".
	Tokens      int    `json:"tokens"`
	TokenSource string `json:"token_source,omitempty"`
	// KeyIndex is the index of the key of the last attempt, -1 for none.
	KeyIndex int `json:"key_index"`
	// Rejected is why the request was answered without reaching Google, or
	// without another key once Google refused the ones it tried.
	Rejected string         `json:"rejected,omitempty"`
	Attempts []AuditAttempt `json:"attempts,omitempty"`
}

// AuditAttempt is a call on a key: the key's index, Google's status (0 when
// the call failed before an answer) and what it meant ("count_tokens" for a
// token count that rejected the key, else an Outcome or Reason).
type AuditAttempt struct {
	KeyIndex int    `json:"key_index"`
	Status   int    `json:"status"`
	Reason   string `json:"reason"`
}
