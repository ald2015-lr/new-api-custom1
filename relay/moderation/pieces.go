package moderation

import (
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	pieceMaxRunes       = 2000
	maxPiecesConsidered = 256
	maxImages           = 3
	maxImageBytes       = 20_000_000
	maxInputsPerCall    = 32
	maxRunesPerCall     = 8000
	maxCallBodyBytes    = 512 << 10
)

// Budget groups of a piece, in the order that the budget is filled.
const (
	groupSystem = iota
	groupNewestUser
	groupOlderUser
)

// piece is at most pieceMaxRunes of normalised text: what one moderation
// input carries and one verdict describes.
type piece struct {
	text  string
	runes int
	key   string
	group int
}

type imageCandidate struct {
	url       string // trimmed, as sent
	key       string
	shareable bool // a data URL holds its bytes, so its verdict may be cached for everyone
}

// pieceSource walks segments piece by piece: each segment is split from its
// start (so a message splits the same way in every turn) and its pieces are
// taken from the start and the end alternately.
type pieceSource struct {
	segments []*segment
	pieces   []string
	order    []int
	next     int
}

func (s *pieceSource) take() (string, bool) {
	for {
		if s.next < len(s.order) {
			p := s.pieces[s.order[s.next]]
			s.next++
			if strings.TrimSpace(p) == "" {
				continue
			}
			return p, true
		}
		if len(s.segments) == 0 {
			return "", false
		}
		s.pieces = splitRunes(s.segments[0].normalized().text, pieceMaxRunes)
		s.order = alternate(len(s.pieces))
		s.next = 0
		s.segments = s.segments[1:]
	}
}

func splitRunes(text string, n int) []string {
	var out []string
	for text != "" {
		end, count := 0, 0
		for end < len(text) && count < n {
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
			count++
		}
		out = append(out, text[:end])
		text = text[end:]
	}
	return out
}

// alternate is 0, n-1, 1, n-2, …
func alternate(n int) []int {
	order := make([]int, 0, n)
	for lo, hi := 0, n-1; lo <= hi; lo, hi = lo+1, hi-1 {
		order = append(order, lo)
		if hi != lo {
			order = append(order, hi)
		}
	}
	return order
}

// candidatePieces lists the distinct pieces worth looking up, in the order of
// the budget: system text up to half the budget, the newest user message, the
// rest of the system text, then the older user messages from the newest. At
// most maxPiecesConsidered are listed; truncated reports that more existed.
func candidatePieces(ex *extraction, endpoint string, budget int) (candidates []*piece, truncated bool) {
	var system, users []*segment
	for _, seg := range ex.segments {
		if seg.role == roleSystem {
			system = append(system, seg)
		} else {
			users = append(users, seg)
		}
	}
	slices.Reverse(users)
	systemSource := &pieceSource{segments: system}
	var newestSource, olderSource *pieceSource
	if len(users) > 0 {
		newestSource = &pieceSource{segments: users[:1]}
		olderSource = &pieceSource{segments: users[1:]}
	} else {
		newestSource, olderSource = &pieceSource{}, &pieceSource{}
	}
	seen := make(map[string]bool)
	full := false
	add := func(text string, group int) {
		key := textKey(endpoint, text)
		if seen[key] {
			return
		}
		if len(candidates) == maxPiecesConsidered {
			full = true
			return
		}
		seen[key] = true
		candidates = append(candidates, &piece{text: text, runes: utf8.RuneCountInString(text), key: key, group: group})
	}
	drain := func(source *pieceSource, group int, limit int) {
		used := 0
		for !full && (limit < 0 || used < limit) {
			text, ok := source.take()
			if !ok {
				return
			}
			add(text, group)
			used += utf8.RuneCountInString(text)
		}
	}
	drain(systemSource, groupSystem, budget/2)
	drain(newestSource, groupNewestUser, -1)
	drain(systemSource, groupSystem, -1)
	drain(olderSource, groupOlderUser, -1)
	return candidates, full
}

// budgetPlan is which uncached pieces an attempt sends.
type budgetPlan struct {
	send      []*piece
	checked   int // runes sent
	cached    int // candidates that already have a verdict
	truncated bool
}

// planBudget fills the budget of uncached runes: system text up to half of
// it (rounded down), then the newest user message, then the rest of the
// system text, then the older user messages. Cached pieces are free; a piece
// that does not fit is skipped, as a later, shorter one may still fit.
func planBudget(candidates []*piece, known func(key string) bool, budget int) budgetPlan {
	var plan budgetPlan
	taken := make([]bool, len(candidates))
	systemCap, systemChecked := budget/2, 0
	take := func(i int, p *piece) {
		taken[i] = true
		plan.send = append(plan.send, p)
		plan.checked += p.runes
	}
	for i, p := range candidates {
		if known(p.key) {
			taken[i] = true
			plan.cached++
		}
	}
	for i, p := range candidates {
		if p.group == groupSystem && !taken[i] && systemChecked+p.runes <= systemCap && plan.checked+p.runes <= budget {
			systemChecked += p.runes
			take(i, p)
		}
	}
	for _, group := range []int{groupNewestUser, groupSystem, groupOlderUser} {
		for i, p := range candidates {
			if p.group != group || taken[i] {
				continue
			}
			if plan.checked+p.runes > budget {
				plan.truncated = true
				continue
			}
			take(i, p)
		}
	}
	return plan
}

// selectImages picks the images an attempt checks: the user images from the
// newest, at most maxImages, http(s) URLs and data URLs of an image type no
// larger than maxImageBytes. Every other image counts as skipped.
func selectImages(images []imageRef, endpoint string, skipAll bool) (accepted []*imageCandidate, skipped int) {
	ordered := slices.Clone(images)
	slices.SortStableFunc(ordered, func(a, b imageRef) int { return b.order - a.order })
	seen := make(map[string]bool)
	for _, image := range ordered {
		if skipAll || len(accepted) == maxImages {
			skipped++
			continue
		}
		candidate, ok := acceptImage(image.url, endpoint)
		if !ok {
			skipped++
			continue
		}
		if seen[candidate.key] {
			continue
		}
		seen[candidate.key] = true
		accepted = append(accepted, candidate)
	}
	return accepted, skipped
}

func acceptImage(raw, endpoint string) (*imageCandidate, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) >= 5 && strings.EqualFold(trimmed[:5], "data:") {
		mediaType, rest, found := strings.Cut(trimmed[5:], ",")
		if !found || len(mediaType) < 6 || !strings.EqualFold(mediaType[:6], "image/") {
			return nil, false
		}
		if len(rest)*3/4 > maxImageBytes {
			return nil, false
		}
		return &imageCandidate{url: trimmed, key: imageKey(endpoint, trimmed), shareable: true}, true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, false
	}
	return &imageCandidate{url: trimmed, key: imageKey(endpoint, trimmed)}, true
}

// packCalls groups the pieces to send into calls of at most maxInputsPerCall
// inputs, maxRunesPerCall runes and maxCallBodyBytes of JSON, and adds one
// call per image.
func packCalls(pieces []*piece, images []*imageCandidate) []*moderationCall {
	var calls []*moderationCall
	var current *moderationCall
	runes, bodyBytes := 0, 0
	for _, p := range pieces {
		size := marshalledLen(p.text) + 1
		if current == nil || len(current.pieces) == maxInputsPerCall || runes+p.runes > maxRunesPerCall || bodyBytes+size > maxCallBodyBytes {
			current = &moderationCall{}
			calls = append(calls, current)
			runes, bodyBytes = 0, 0
		}
		current.pieces = append(current.pieces, p)
		runes += p.runes
		bodyBytes += size
	}
	for _, image := range images {
		calls = append(calls, &moderationCall{image: image})
	}
	return calls
}
