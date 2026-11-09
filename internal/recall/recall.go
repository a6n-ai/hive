// Package recall picks the few memories worth injecting at a given moment.
package recall

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/internal/text"
	"github.com/a6n-ai/hive/model"
)

type Mode int

const (
	SessionStart Mode = iota
	Prompt
	PostToolError
)

const (
	sessionCandidates = 200 // most recently seen memories scored at session start
	promptCandidates  = 50  // FTS hits scored for a prompt
)

type Query struct {
	Project        string
	Files          []string
	Text           string
	ErrorSignature string
	Budget         int // tokens; 0 uses the configured default for the mode
	Types          []model.MemoryType
	Mode           Mode
}

type Line struct {
	MemoryID string
	Type     model.MemoryType
	Text     string
	Score    float64
}

// Recall returns ranked lines for q, and marks them used.
func Recall(ctx context.Context, st *store.Store, cfg config.RecallCfg, q Query, now time.Time) ([]Line, error) {
	if !cfg.Inject {
		return nil, nil
	}
	filter := store.ListFilter{Types: q.Types, Project: q.Project, Statuses: []model.Status{model.Active}}
	var lines []Line
	var err error
	switch q.Mode {
	case SessionStart:
		filter.Limit = sessionCandidates
		lines, err = scoreAll(st, filter, cfg, q, now, nil)
		lines = withinBudget(lines, firstNonZero(q.Budget, cfg.SessionBudget), 0)
	case Prompt:
		terms := text.Terms(q.Text)
		if len(terms) == 0 {
			return nil, nil
		}
		lines, err = scoreAll(st, filter, cfg, q, now, terms)
		lines = aboveThreshold(lines, cfg.PromptThreshold)
		lines = withinBudget(lines, q.Budget, cfg.PromptMaxLines)
	case PostToolError:
		if q.ErrorSignature == "" {
			return nil, nil
		}
		filter.Types, filter.Key = []model.MemoryType{model.Fix}, q.ErrorSignature
		lines, err = scoreAll(st, filter, cfg, q, now, nil)
		lines = withinBudget(lines, 0, cfg.PromptMaxLines)
	}
	if err != nil {
		return nil, err
	}
	return lines, markUsed(ctx, st, lines, now)
}

// scoreAll fetches candidates (FTS when terms are given) and scores them.
func scoreAll(st *store.Store, f store.ListFilter, cfg config.RecallCfg, q Query, now time.Time, terms []string) ([]Line, error) {
	var ms []model.Memory
	var err error
	if terms != nil {
		ms, _, err = st.SearchFTS(terms, f, promptCandidates)
	} else {
		ms, err = st.ListMemories(f)
	}
	if err != nil {
		return nil, err
	}
	lines := make([]Line, 0, len(ms))
	for _, m := range ms {
		rel := 1.0
		if terms != nil {
			rel = relevance(terms, m.Text)
		}
		score := rel * m.Confidence * recency(m, now, cfg.HalfLifeDays) * scopeWeight(m, q.Files)
		lines = append(lines, Line{MemoryID: m.ID, Type: m.Type, Text: m.Text, Score: score})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Score > lines[j].Score })
	return lines, nil
}

func aboveThreshold(lines []Line, min float64) []Line {
	out := lines[:0]
	for _, l := range lines {
		if l.Score >= min {
			out = append(out, l)
		}
	}
	return out
}

// withinBudget keeps lines in order until the token budget or line cap
// (0 = none) is reached.
func withinBudget(lines []Line, budget, maxLines int) []Line {
	used := 0
	for i, l := range lines {
		cost := estimateTokens(renderLine(l))
		if (budget > 0 && used+cost > budget) || (maxLines > 0 && i >= maxLines) {
			return lines[:i]
		}
		used += cost
	}
	return lines
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// markUsed records that lines were injected. A locked database only skips the
// bookkeeping; recall itself still succeeds.
func markUsed(ctx context.Context, st *store.Store, lines []Line, now time.Time) error {
	if len(lines) == 0 || st.HitLock() {
		return nil
	}
	ids := make([]string, len(lines))
	for i, l := range lines {
		ids[i] = l.MemoryID
	}
	err := st.Write(ctx, func(tx *store.Tx) error { return tx.TouchUsed(ids, now.UnixMilli()) })
	if errors.Is(err, store.ErrLocked) || errors.Is(err, store.ErrSchemaNewer) || errors.Is(err, store.ErrReadOnly) {
		return nil
	}
	return err
}
