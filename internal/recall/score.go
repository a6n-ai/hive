package recall

import (
	"math"
	"slices"
	"time"

	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// relevance is the share of terms that match, taking the better of the
// query's view and the memory's view. It does not depend on corpus size,
// unlike bm25, so one threshold works for a new and an old database.
func relevance(queryGroups [][]string, memText string) float64 {
	memTerms := text.Terms(memText)
	if len(queryGroups) == 0 || len(memTerms) == 0 {
		return 0
	}
	var queryTerms []string
	matchedWords := 0
	for _, g := range queryGroups {
		queryTerms = append(queryTerms, g...)
		if countMatched(g, memTerms) > 0 {
			matchedWords++
		}
	}
	m := countMatched(memTerms, queryTerms)
	return math.Max(float64(matchedWords)/float64(len(queryGroups)), float64(m)/float64(len(memTerms)))
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
