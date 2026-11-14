package stats

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// Draw reports whether a prompt is held out. A variable so tests can force the outcome.
var Draw = func(session, digest string, index int, share float64) bool {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%d", session, digest, index)
	return h.Sum64()%10000 < uint64(share*10000)
}

type GroupStats struct {
	N                    int     `json:"n"`
	MeanTokens           float64 `json:"mean_tokens"`
	FailedPerPrompt      float64 `json:"failed_per_prompt"`
	CorrectionsPerPrompt float64 `json:"corrections_per_prompt"`
	WouldBeHitRate       float64 `json:"would_be_hit_rate"`
}

type HoldoutResult struct {
	State     string     `json:"state"`
	Holdout   GroupStats `json:"holdout"`
	Normal    GroupStats `json:"normal"`
	Diff      float64    `json:"diff"`
	Lo        float64    `json:"lo"`
	Hi        float64    `json:"hi"`
	Expected  float64    `json:"expected"`
	MinSample int        `json:"min_sample"`
	Verdict   string     `json:"verdict"`
}

// WouldBeHit reports whether some command reaches the shown line: it has content
// words and every one appears in the line's terms (the recall label rule).
func WouldBeHit(commands []string, key, line string) bool {
	have := map[string]bool{}
	for _, t := range text.Terms(key + " " + line) {
		have[t] = true
	}
	for _, c := range commands {
		words := recall.ContentWords(c)
		if len(words) > 0 && !slices.ContainsFunc(words, func(w string) bool { return !have[w] }) {
			return true
		}
	}
	return false
}

type promptMeasure struct {
	tokens, failed, corrected float64
	hit                       bool
}

// Compare measures holdout against normal prompts that had at least one line shown.
func Compare(st *store.Store, w store.Window, minSample int, expected float64) (HoldoutResult, error) {
	res := HoldoutResult{Expected: expected, MinSample: minSample}
	injs, err := st.InjectionsIn(w)
	if err != nil {
		return res, err
	}
	prompts, err := st.PromptsIn(w)
	if err != nil {
		return res, err
	}
	byDigest := make(map[string]store.PromptRef, len(prompts))
	for _, p := range prompts {
		byDigest[p.Digest] = p
	}
	var hold, norm []promptMeasure
	for _, in := range injs {
		p, ok := byDigest[in.PromptDigest]
		if in.Hook != "UserPromptSubmit" || in.LineCount == 0 || !ok {
			continue
		}
		m, err := measure(st, w, in, p)
		if err != nil {
			return res, err
		}
		if in.Holdout {
			hold = append(hold, m)
		} else {
			norm = append(norm, m)
		}
	}
	res.Holdout, res.Normal = groupStats(hold), groupStats(norm)
	if len(hold) < minSample || len(norm) < minSample {
		res.State = "collecting"
		return res, nil
	}
	res.State = "compared"
	res.Diff = res.Holdout.MeanTokens - res.Normal.MeanTokens
	res.Lo, res.Hi = bootstrap(hold, norm, w)
	switch {
	case expected == 0:
		res.Verdict = "inconclusive"
	case expected < res.Lo || expected > res.Hi:
		res.Verdict = "contradicted"
	case res.Lo > 0 || res.Hi < 0:
		res.Verdict = "consistent"
	default:
		res.Verdict = "inconclusive"
	}
	return res, nil
}

func measure(st *store.Store, w store.Window, in store.Injection, p store.PromptRef) (promptMeasure, error) {
	end := cmp.Or(p.TurnEndTS, p.NextPromptTS, w.To)
	var m promptMeasure
	u, err := st.UsageInRange(p.Session, p.TS, end)
	if err != nil {
		return m, err
	}
	m.tokens = float64(u.Input + u.Output + u.CacheCreation)
	evs, err := st.EventsIn(p.Session, p.TS, end)
	if err != nil {
		return m, err
	}
	for _, e := range evs {
		if e.Kind == model.PostTool {
			if _, failed := e.Payload["error"]; failed {
				m.failed++
			}
		}
		if e.Kind == model.Correction {
			m.corrected++
		}
	}
	if len(in.MemoryIDs) == 0 {
		return m, nil
	}
	key, line, err := st.MemoryKeyText(in.MemoryIDs[0])
	if err != nil {
		return m, err
	}
	cmdEnd := cmp.Or(p.NextPromptTS, end)
	if cmdEnd != end {
		if evs, err = st.EventsIn(p.Session, p.TS, cmdEnd); err != nil {
			return m, err
		}
	}
	var cmds []string
	for _, e := range evs {
		if c, ok := e.Payload["command"].(string); ok && e.Kind == model.PostTool && e.Tool == "shell" {
			cmds = append(cmds, c)
		}
	}
	m.hit = WouldBeHit(cmds, key, line)
	return m, nil
}

func groupStats(ms []promptMeasure) GroupStats {
	g := GroupStats{N: len(ms)}
	if len(ms) == 0 {
		return g
	}
	for _, m := range ms {
		g.MeanTokens += m.tokens
		g.FailedPerPrompt += m.failed
		g.CorrectionsPerPrompt += m.corrected
		if m.hit {
			g.WouldBeHitRate++
		}
	}
	n := float64(len(ms))
	g.MeanTokens /= n
	g.FailedPerPrompt /= n
	g.CorrectionsPerPrompt /= n
	g.WouldBeHitRate /= n
	return g
}

func bootstrap(hold, norm []promptMeasure, w store.Window) (lo, hi float64) {
	const resamples = 1000
	rng := rand.New(rand.NewPCG(uint64(w.From), uint64(w.To))) //nolint:gosec // G404: a seeded bootstrap must repeat for the same window; not security
	mean := func(ms []promptMeasure) float64 {
		var s float64
		for range ms {
			s += ms[rng.IntN(len(ms))].tokens
		}
		return s / float64(len(ms))
	}
	diffs := make([]float64, resamples)
	for i := range diffs {
		diffs[i] = mean(hold) - mean(norm)
	}
	slices.Sort(diffs)
	rank := func(p float64) float64 { return diffs[int(math.Ceil(p*resamples))-1] }
	return rank(0.025), rank(0.975)
}
