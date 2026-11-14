// Package recall picks the few memories worth injecting at a given moment.
package recall

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

type Mode int

const (
	SessionStart Mode = iota
	Prompt
	PostToolError
)

const (
	sessionCandidates = 200 // most recently seen memories scored at session start
	promptCandidates  = 200 // best matches scored for a prompt, the same on the FTS and index paths
)

type Query struct {
	Project        string
	Files          []string
	Text           string
	ErrorSignature string
	Budget         int // tokens; 0 uses the configured default for the mode
	Types          []model.MemoryType
	Mode           Mode
	Agent          string // runtime that submitted the prompt; scored, not used as a filter
	Session        string // set by the hook so an injection can be labeled after the session ends
	PromptDigest   string // digest of the prompt event; the group key its shown lines are labeled by
}

// hit is a scored line plus the column features that produced it.
type hit struct {
	Line
	feat Features
}

type Line struct {
	MemoryID string
	Key      string
	Type     model.MemoryType
	Text     string
	Score    float64
}

// Recall returns ranked lines for q, and marks them used. A nil src reads the store.
func Recall(ctx context.Context, st *store.Store, src Source, cfg config.RecallCfg, q Query, now time.Time) ([]Line, error) {
	if !cfg.Inject {
		return nil, nil
	}
	if src == nil {
		src = StoreSource{st}
	}
	filter := store.ListFilter{Types: q.Types, Project: q.Project, Statuses: []model.Status{model.Active}}
	var hits []hit
	var err error
	switch q.Mode {
	case SessionStart:
		filter.Limit = sessionCandidates
		hits, err = scoreAll(src, filter, cfg, q, now, nil, nil, Facets{}, DefaultWeights())
		hits = trimHits(hits, firstNonZero(q.Budget, cfg.SessionBudget), 0)
	case Prompt:
		groups := text.Groups(q.Text)
		if len(groups) == 0 {
			return nil, nil
		}
		w, loadErr := LoadWeights(st)
		if loadErr != nil {
			return nil, loadErr
		}
		facets := chunkPrompt(q.Text, q.Agent)
		windows := promptWindows(groups)
		if len(windows) == 1 {
			hits, err = scoreAll(src, filter, cfg, q, now, groups, flatten(groups), facets, w)
		} else {
			hits, err = scoreWindows(ctx, src, filter, cfg, q, now, windows, facets, w)
		}
		hits = dropBelow(hits, cfg.PromptThreshold)
		hits = trimHits(hits, q.Budget, promptLineCap(len(windows), cfg.PromptMaxLines))
	case PostToolError:
		if q.ErrorSignature == "" {
			return nil, nil
		}
		filter.Types, filter.Key = []model.MemoryType{model.Fix}, q.ErrorSignature
		hits, err = scoreAll(src, filter, cfg, q, now, nil, nil, Facets{}, DefaultWeights())
		hits = trimHits(hits, 0, cfg.PromptMaxLines)
	}
	if err != nil {
		return nil, err
	}
	lines := make([]Line, len(hits))
	for i, h := range hits {
		lines[i] = h.Line
	}
	return lines, markUsed(ctx, st, hits, q, now)
}

// scoreAll fetches candidates from src and scores them.
func scoreAll(src Source, f store.ListFilter, cfg config.RecallCfg, q Query, now time.Time, groups [][]string, terms []string, facets Facets, w Weights) ([]hit, error) {
	ms, err := src.Candidates(terms, f, promptCandidates)
	if err != nil {
		return nil, err
	}
	traj := map[string]string{}
	if groups != nil {
		ms, err = mergeColumns(src, f, facets, ms)
		if err != nil {
			return nil, err
		}
		ms, traj, err = mergeTrajectory(src, f, groups, ms)
		if err != nil {
			return nil, err
		}
	}
	hits := make([]hit, 0, len(ms))
	for _, m := range ms {
		score := m.EffectiveConfidence() * recency(m, now, cfg.HalfLifeDays) * scopeWeight(m, q.Files)
		feat := Features{}
		if groups != nil {
			feat = applyTrajectory(fieldFeatures(groups, facets, m), groups, traj[m.ID])
			score = blend(feat, w) * m.EffectiveConfidence() * recency(m, now, cfg.HalfLifeDays) * scopeWeight(m, q.Files)
			if score == 0 {
				continue
			}
		}
		hits = append(hits, hit{Line: Line{MemoryID: m.ID, Key: m.Key, Type: m.Type, Text: m.Text, Score: score}, feat: feat})
	}
	sortHits(hits)
	return hits, nil
}

func sortHits(hits []hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].MemoryID < hits[j].MemoryID // ties must not depend on the candidate source's order
	})
}

// mergeColumns adds memories the full-text index cannot see: the named memory
// types, and rows whose scope, key, or body contains a path or agent chunk.
func mergeColumns(src Source, f store.ListFilter, facets Facets, ms []model.Memory) ([]model.Memory, error) {
	seen := make(map[string]bool, len(ms))
	for _, m := range ms {
		seen[m.ID] = true
	}
	add := func(extra []model.Memory) {
		for _, m := range extra {
			if !seen[m.ID] {
				seen[m.ID] = true
				ms = append(ms, m)
			}
		}
	}
	if len(facets.Types) > 0 {
		f2 := f
		f2.Types = facets.Types
		f2.Key = ""
		f2.Limit = 50
		extra, err := src.Candidates(nil, f2, 50)
		if err != nil {
			return nil, err
		}
		add(extra)
	}
	for _, token := range append(append([]string{}, facets.Paths...), facets.Agents...) {
		extra, err := touching(src, f.Project, token, f.Statuses)
		if err != nil {
			return nil, err
		}
		add(extra)
	}
	return ms, nil
}

func touching(src Source, project, token string, statuses []model.Status) ([]model.Memory, error) {
	var st *store.Store
	switch s := src.(type) {
	case StoreSource:
		st = s.Store
	case IndexSource:
		st = s.Store
	default:
		return nil, nil
	}
	if st == nil {
		return nil, nil
	}
	return st.MemoriesTouching(project, token, statuses, 20)
}

func dropBelow(hits []hit, min float64) []hit {
	out := hits[:0]
	for _, h := range hits {
		if h.Score >= min {
			out = append(out, h)
		}
	}
	return out
}

// withinBudget keeps lines in order until the token budget or line cap
// (0 = none) is reached.
// FitBudget keeps the leading lines that fit in budget tokens.
func FitBudget(lines []Line, budget int) []Line { return withinBudget(lines, budget, 0) }

func withinBudget(lines []Line, budget, maxLines int) []Line {
	used := 0
	for i, l := range lines {
		cost := EstimateTokens(renderLine(l))
		if (budget > 0 && used+cost > budget) || (maxLines > 0 && i >= maxLines) {
			return lines[:i]
		}
		used += cost
	}
	return lines
}

func trimHits(hits []hit, budget, maxLines int) []hit {
	used := 0
	for i, h := range hits {
		cost := EstimateTokens(renderLine(h.Line))
		if (budget > 0 && used+cost > budget) || (maxLines > 0 && i >= maxLines) {
			return hits[:i]
		}
		used += cost
	}
	return hits
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// markUsed records that lines were injected. A prompt with a session also
// stores the column features so the idle pass can learn from them. A locked
// database only skips the bookkeeping; recall itself still succeeds.
func markUsed(ctx context.Context, st *store.Store, hits []hit, q Query, now time.Time) error {
	if len(hits) == 0 {
		return nil
	}
	outcomes := q.Mode == Prompt && q.Session != ""
	if st.HitLock() {
		if outcomes {
			st.NoteDroppedOutcomes(len(hits))
		}
		return nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.MemoryID
	}
	err := st.Write(ctx, func(tx *store.Tx) error {
		ts := now.UnixMilli()
		if err := tx.TouchUsed(ids, ts); err != nil {
			return err
		}
		if !outcomes {
			return nil
		}
		for _, h := range hits {
			raw, err := h.feat.marshal()
			if err != nil {
				return err
			}
			if err := tx.InsertScoreOutcome(store.OutcomeInput{MemoryID: h.MemoryID, Session: q.Session, Agent: q.Agent,
				TS: ts, Features: raw, Score: h.Score, PromptDigest: q.PromptDigest, ShownKey: h.Key, ShownText: h.Text, LabelRule: LabelRule}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrLocked) && outcomes {
		st.NoteDroppedOutcomes(len(hits))
	}
	if errors.Is(err, store.ErrLocked) || errors.Is(err, store.ErrSchemaNewer) || errors.Is(err, store.ErrReadOnly) {
		return nil
	}
	return err
}

func flatten(groups [][]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
