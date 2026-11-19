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
	text    string
	runes   int
	key     string
	group   int
	fromEnd bool // taken from the end of its segment, so a cut keeps its end
}

type imageCandidate struct {
	url       string // trimmed, as sent
	key       string
	shareable bool // a data URL holds its bytes, so its verdict may be cached for everyone
}

// pieceSource walks segments piece by piece: each segment is split from its
// start (so a message splits the same way in every turn) and its pieces are
// taken from the start and the end alternately. It ends at the first segment
// that the extraction's normalisation budget no longer covers.
type pieceSource struct {
	ex       *extraction
	segments []*segment
	pieces   []string
	order    []int
	next     int
}

func (s *pieceSource) take() (text string, fromEnd, ok bool) {
	for {
		if s.next < len(s.order) {
			p := s.pieces[s.order[s.next]]
			fromEnd = s.next%2 == 1
			s.next++
			if strings.TrimSpace(p) == "" {
				continue
			}
			return p, fromEnd, true
		}
		if len(s.segments) == 0 {
			return "", false, false
		}
		norm := s.ex.normalize(s.segments[0])
		if norm == nil {
			s.segments = nil
			return "", false, false
		}
		s.pieces = splitRunes(norm.text, pieceMaxRunes)
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
// the budget: system text up to half the budget and half of the pieces, the
// newest user message, the rest of the system text, then the older user
// messages from the newest. At most maxPiecesConsidered are listed;
// truncated reports that more existed, or that the normalisation budget left
// text out. The first step is capped in pieces as well as in runes, so many
// short system messages cannot use up the pieces before the newest user
// message is listed, and the newest user message is normalised before any
// system text, so system text cannot use up the normalisation budget either.
func candidatePieces(ex *extraction, endpoint string, budget int) (candidates []*piece, truncated bool) {
	newest, system, older := ex.budgetOrder()
	systemSource := &pieceSource{ex: ex, segments: system}
	newestSource, olderSource := &pieceSource{ex: ex}, &pieceSource{ex: ex, segments: older}
	if newest != nil {
		ex.normalize(newest)
		newestSource.segments = []*segment{newest}
	}
	seen := make(map[string]bool)
	full := false
	add := func(text string, group int, fromEnd bool) {
		key := textKey(endpoint, text)
		if seen[key] {
			return
		}
		if len(candidates) == maxPiecesConsidered {
			full = true
			return
		}
		seen[key] = true
		candidates = append(candidates, &piece{text: text, runes: utf8.RuneCountInString(text), key: key, group: group, fromEnd: fromEnd})
	}
	// drain stops at runeLimit runes taken or pieceLimit pieces listed (-1:
	// no limit); only a piece refused by the overall cap makes the list full.
	drain := func(source *pieceSource, group, runeLimit, pieceLimit int) {
		used := 0
		for !full && (runeLimit < 0 || used < runeLimit) && (pieceLimit < 0 || len(candidates) < pieceLimit) {
			text, fromEnd, ok := source.take()
			if !ok {
				return
			}
			add(text, group, fromEnd)
			used += utf8.RuneCountInString(text)
		}
	}
	drain(systemSource, groupSystem, budget/2, maxPiecesConsidered/2)
	drain(newestSource, groupNewestUser, -1, -1)
	drain(systemSource, groupSystem, -1, -1)
	drain(olderSource, groupOlderUser, -1, -1)
	return candidates, full || ex.normalizeStopped
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
// system text, then the older user messages. Cached pieces are free. When a
// piece is longer than what is left, its start is sent, or its end for a
// piece taken from the end of its segment, so no budget goes unused while
// unchecked text remains. A system piece cut at half the budget is completed
// first in the third step, so a piece goes out cut only when the whole budget
// is spent: text that fits is sent in the same pieces every time and its
// verdicts hit the cache.
func planBudget(candidates []*piece, known func(key string) bool, budget int, endpoint string) budgetPlan {
	var plan budgetPlan
	cached := make([]bool, len(candidates))
	for i, p := range candidates {
		if known(p.key) {
			cached[i] = true
			plan.cached++
		}
	}
	granted := make([]int, len(candidates)) // runes of each piece the budget covers
	var order []int                         // pieces in the order the budget first reached them
	used := 0
	grant := func(group, limit int) {
		given := 0
		for i, p := range candidates {
			left := min(limit-given, budget-used)
			if left <= 0 {
				return
			}
			if p.group != group || cached[i] || granted[i] == p.runes {
				continue
			}
			if granted[i] == 0 {
				order = append(order, i)
			}
			n := min(p.runes-granted[i], left)
			granted[i] += n
			given += n
			used += n
		}
	}
	grant(groupSystem, budget/2)
	grant(groupNewestUser, budget)
	grant(groupSystem, budget)
	grant(groupOlderUser, budget)

	for i, p := range candidates {
		if !cached[i] && granted[i] < p.runes {
			plan.truncated = true
		}
	}
	for _, i := range order {
		p := candidates[i]
		if n := granted[i]; n < p.runes {
			runes := []rune(p.text)
			text := string(runes[:n])
			if p.fromEnd {
				text = string(runes[len(runes)-n:])
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			p = &piece{text: text, runes: n, key: textKey(endpoint, text), group: p.group, fromEnd: p.fromEnd}
		}
		plan.send = append(plan.send, p)
		plan.checked += p.runes
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
