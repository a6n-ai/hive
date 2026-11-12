package recall

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

const (
	// shortPromptGroups is the largest prompt still scored as one query.
	// Past this, the grade's denominator is the whole prompt, so a memory
	// that matches one sentence is diluted under the injection line.
	shortPromptGroups = 12
	windowGroups      = 4
	windowStep        = 4
	maxPromptWindows  = 8
)

// promptWindows splits a long prompt into short stretches of content words.
// A short prompt is one window, so its score is unchanged. A single leftover
// word stays on the previous window: alone it would score 1 for one shared word.
func promptWindows(groups [][]string) [][][]string {
	if len(groups) <= shortPromptGroups {
		return [][][]string{groups}
	}
	var all [][][]string
	for start := 0; start < len(groups); start += windowStep {
		end := start + windowGroups
		if end > len(groups) {
			end = len(groups)
		}
		if end-start < 2 && len(all) > 0 {
			all[len(all)-1] = append(all[len(all)-1], groups[start:end]...)
			break
		}
		all = append(all, groups[start:end])
		if end == len(groups) {
			break
		}
	}
	if len(all) <= maxPromptWindows {
		return all
	}
	out := make([][][]string, 0, maxPromptWindows)
	seen := map[int]bool{}
	for i := 0; i < maxPromptWindows; i++ {
		idx := i * (len(all) - 1) / (maxPromptWindows - 1)
		if seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, all[idx])
	}
	return out
}

// promptLineCap lets a long prompt return one line per window, up to twice
// the short-prompt cap. A short prompt keeps the configured cap.
func promptLineCap(windows, cfgMax int) int {
	if windows <= 1 || cfgMax <= 0 {
		return cfgMax
	}
	cap := cfgMax + windows - 1
	if cap > cfgMax*2 {
		return cfgMax * 2
	}
	return cap
}

// scoreWindows runs one candidate query per window at the same time, then
// keeps each memory's best window. The queries are reads; scoring happens
// after they all return.
func scoreWindows(ctx context.Context, src Source, f store.ListFilter, cfg config.RecallCfg, q Query, now time.Time, windows [][][]string, facets Facets, w Weights) ([]hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type batch struct {
		ms  []model.Memory
		err error
	}
	batches := make([]batch, len(windows))
	var wg sync.WaitGroup
	for i, win := range windows {
		wg.Add(1)
		go func(i int, win [][]string) {
			defer wg.Done()
			if ctx.Err() != nil {
				batches[i].err = ctx.Err()
				return
			}
			ms, err := src.Candidates(flatten(win), f, promptCandidates)
			batches[i] = batch{ms: ms, err: err}
		}(i, win)
	}
	wg.Wait()

	seen := map[string]model.Memory{}
	for _, b := range batches {
		if b.err != nil {
			return nil, b.err
		}
		for _, m := range b.ms {
			seen[m.ID] = m
		}
	}
	extra, err := mergeColumns(src, f, facets, nil)
	if err != nil {
		return nil, err
	}
	for _, m := range extra {
		seen[m.ID] = m
	}
	var all [][]string
	for _, win := range windows {
		all = append(all, win...)
	}
	cited, traj, err := mergeTrajectory(src, f, all, nil)
	if err != nil {
		return nil, err
	}
	for _, m := range cited {
		seen[m.ID] = m
	}
	hits := make([]hit, 0, len(seen))
	for _, m := range seen {
		score, feat := bestWindow(windows, q.Agent, m, traj[m.ID], now, cfg.HalfLifeDays, q.Files, w)
		if score == 0 {
			continue
		}
		hits = append(hits, hit{Line: Line{MemoryID: m.ID, Type: m.Type, Text: m.Text, Score: score}, feat: feat})
	}
	sortHits(hits)
	return hits, nil
}

// bestWindow scores the memory against each window and keeps the strongest.
// The window is what "length" means here: a long prompt is not one fraction.
func bestWindow(windows [][][]string, agent string, m model.Memory, traj string, now time.Time, halfLife float64, files []string, w Weights) (float64, Features) {
	var best float64
	var feat Features
	scale := m.Confidence * recency(m, now, halfLife) * scopeWeight(m, files)
	for _, win := range windows {
		f := applyTrajectory(fieldFeatures(win, chunkPrompt(strings.Join(flatten(win), " "), agent), m), win, traj)
		score := blend(f, w) * scale
		if score > best {
			best, feat = score, f
		}
	}
	return best, feat
}
