// Package geminifreekey limits the keys of a Gemini (AI Studio) channel in
// free key mode to the free tier's limits: requests per minute (RPM), input
// tokens per minute (TPM) and requests per Pacific day (RPD), per key and per
// model, and learns from Google's 429 answers when a key must pause. It
// keeps its state in the process and never stores a key in clear: keys are
// known by the SHA-256 of the key. It does not depend on the relay, the
// service layer or the database, so channel selection can use it too.
package geminifreekey

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// Limits are the free tier's limits of a model for one key:
// dto.GeminiFreeKeyUnlimited, dto.GeminiFreeKeyNotOnFreeTier or a positive
// number.
type Limits struct {
	RPM int
	TPM int
	RPD int
}

// Bucket is the quota a request counts in: a row of the channel's limits per
// model, found by the upstream model name, or the model alone when no row
// names it.
type Bucket struct {
	// ID is the row's model in lower case, or the normalized model name of a
	// model no row names.
	ID string
	// Listed tells whether a row names the model. A model no row names has
	// no local limits; only Google's 429 answers pause its keys.
	Listed bool
	Limits Limits
}

// NotOnFreeTier tells whether the free tier does not serve the bucket's
// model: a row with a zero limit.
func (b Bucket) NotOnFreeTier() bool {
	return b.Listed && (b.Limits.RPM == 0 || b.Limits.TPM == 0 || b.Limits.RPD == 0)
}

// NormalizeModel is model as rows are matched against it: trimmed, in lower
// case and without a leading "models/".
func NormalizeModel(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	name, _ = strings.CutPrefix(name, "models/")
	return name
}

// ResolveBucket finds the bucket of the upstream model name model: the row
// that names it as its model or an alias, else the row with the longest
// name (model or alias) that model starts with followed by "-", so that
// dated and suffixed variants count with their model; a model no row names
// is its own bucket without limits.
func ResolveBucket(rows []dto.GeminiFreeKeyModelLimit, model string) Bucket {
	name := NormalizeModel(model)
	prefix, prefixLength := -1, 0
	consider := func(i int, candidate string) bool {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			return false
		}
		if candidate == name {
			return true
		}
		if len(candidate) > prefixLength && len(name) > len(candidate) && name[len(candidate)] == '-' && strings.HasPrefix(name, candidate) {
			prefix, prefixLength = i, len(candidate)
		}
		return false
	}
	for i, row := range rows {
		if consider(i, row.Model) {
			return bucketOf(row)
		}
		for _, alias := range row.Aliases {
			if consider(i, alias) {
				return bucketOf(row)
			}
		}
	}
	if prefix >= 0 {
		return bucketOf(rows[prefix])
	}
	unlimited := dto.GeminiFreeKeyUnlimited
	return Bucket{ID: name, Limits: Limits{RPM: unlimited, TPM: unlimited, RPD: unlimited}}
}

func bucketOf(row dto.GeminiFreeKeyModelLimit) Bucket {
	return Bucket{
		ID:     strings.ToLower(strings.TrimSpace(row.Model)),
		Listed: true,
		Limits: Limits{RPM: row.RPM, TPM: row.TPM, RPD: row.RPD},
	}
}
