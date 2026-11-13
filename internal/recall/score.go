package recall

import (
	"math"
	"slices"
	"time"

	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// promptScore scores one memory against a prompt. It is a local Jev: a few
// yes/no checks, then one graded score. Nothing is sent to a model.
//
//	relevant?  no shared word            → 0, and the memory is not injected
//	phrase?    prompt words occur in order → grade 1
//	covered?   every prompt word matches   → grade 0.85
//	otherwise                              → grade = share of prompt words that match
//
// Confidence, recency, and scope then scale the grade, the same way a Jev
// score is combined with signals the code already knows.
func promptScore(groups [][]string, m model.Memory, now time.Time, halfLife float64, files []string) float64 {
	return grade(groups, text.Terms(m.Text)) * m.EffectiveConfidence() * recency(m, now, halfLife) * scopeWeight(m, files)
}

// grade is the leaf shared by the text and by every stored column.
// No shared word is 0. Prompt words in order is 1. Every word in any order
// is 0.85. Otherwise it is the share of prompt words that match.
func grade(groups [][]string, terms []string) float64 {
	if len(groups) == 0 || len(terms) == 0 {
		return 0
	}
	matched := 0
	for _, g := range groups {
		if countMatched(g, terms) > 0 {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	g := float64(matched) / float64(len(groups))
	switch {
	case phraseHit(groups, terms):
		g = 1
	case matched == len(groups):
		g = 0.85
	}
	return g
}

// phraseHit reports whether the prompt words occur in the memory in order.
// test matches tests, the same prefix rule as the rest of recall.
func phraseHit(groups [][]string, memTerms []string) bool {
	at := 0
	for _, g := range groups {
		found := false
		for at < len(memTerms) {
			if countMatched(g, memTerms[at:at+1]) > 0 {
				found = true
				at++
				break
			}
			at++
		}
		if !found {
			return false
		}
	}
	return true
}

// countMatched counts terms in a that match some term in b.
func countMatched(a, b []string) int {
	n := 0
	for _, x := range a {
		if slices.ContainsFunc(b, func(y string) bool { return text.Matches(x, y) }) {
			n++
		}
	}
	return n
}

// recency halves a memory's weight every halfLifeDays since it was last seen
// or used.
func recency(m model.Memory, now time.Time, halfLifeDays float64) float64 {
	last := max(m.LastSeen, m.LastUsed)
	ageDays := float64(now.UnixMilli()-last) / float64(24*time.Hour/time.Millisecond)
	if ageDays <= 0 || halfLifeDays <= 0 {
		return 1
	}
	return math.Pow(0.5, ageDays/halfLifeDays)
}

// scopeWeight prefers memories about the files in hand, then the project,
// then global ones.
func scopeWeight(m model.Memory, files []string) float64 {
	switch m.ScopeKind {
	case model.File:
		if slices.Contains(files, m.Scope) {
			return 1.0
		}
		return 0.5
	case model.ProjectScope:
		return 0.8
	default:
		return 0.5
	}
}
