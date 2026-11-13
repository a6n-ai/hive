package recall

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// TrainResult is one idle pass over injections whose session has already ended.
type TrainResult struct {
	Labeled int
	Hits    int
	Misses  int
	Quiet   int
	Weights Weights
}

// Train is the sleep-time pass. The agent is not waiting. Each injection from
// a finished session is labeled from what the agent did next: a later command
// that shared the memory and succeeded, a correction or a failure, or a
// memory the user forgot. The weights are what the next prompt reads.
// Nothing is sent to a model.
func Train(ctx context.Context, st *store.Store, now time.Time) (TrainResult, error) {
	w, err := LoadWeights(st)
	if err != nil {
		return TrainResult{}, err
	}
	open, err := st.OpenScoreOutcomes(500)
	if err != nil {
		return TrainResult{}, err
	}
	var res TrainResult
	for _, o := range open {
		f, err := parseFeatures(o.Features)
		if err != nil {
			return TrainResult{}, err
		}
		y, ok, err := labelOutcome(st, o)
		if err != nil {
			return TrainResult{}, err
		}
		var label *int
		if ok {
			bit := 0
			if y >= 0.5 {
				bit = 1
				res.Hits++
				w.Hits++
			} else {
				res.Misses++
				w.Misses++
			}
			label = &bit
			w = learn(w, f, y)
			w.Seen++
			res.Labeled++
		} else {
			res.Quiet++
		}
		id, lab, ts := o.ID, label, now.UnixMilli()
		if err := st.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.ResolveScoreOutcome(id, lab, "", 1, ts)
			return err
		}); err != nil {
			return TrainResult{}, err
		}
	}
	if res.Labeled > 0 {
		if err := SaveWeights(ctx, st, w); err != nil {
			return TrainResult{}, err
		}
	}
	res.Weights = w
	return res, nil
}

func learn(w Weights, f Features, y float64) Weights {
	pred := blend(f, w)
	step := 0.25 * (y - pred)
	w.Text = clamp(w.Text+step*f.Text, 0.5, 2)
	w.Key = clamp(w.Key+step*f.Key, 0, 2)
	w.Body = clamp(w.Body+step*f.Body, 0, 2)
	w.Type = clamp(w.Type+step*f.Type, 0, 2)
	w.Scope = clamp(w.Scope+step*f.Scope, 0, 2)
	w.Agent = clamp(w.Agent+step*f.Agent, 0, 2)
	w.Command = clamp(w.Command+step*f.Command, 0, 2)
	w.Trajectory = clamp(w.Trajectory+step*f.Trajectory, 0, 2)
	return w
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// labelOutcome reports whether the injection worked. ok is false when the
// session ended without a related command, correction, or forget.
func labelOutcome(st *store.Store, o store.ScoreOutcome) (float64, bool, error) {
	m, err := st.GetMemory(o.MemoryID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, true, nil
		}
		return 0, false, err
	}
	if m.Status == model.Forgotten {
		return 0, true, nil
	}
	events, err := st.SessionEvents(o.Session)
	if err != nil {
		return 0, false, err
	}
	var last *bool
	for _, e := range events {
		if e.TS < o.TS || !related(*m, e) {
			continue
		}
		if e.Kind == model.Correction {
			return 0, true, nil
		}
		if e.Kind == model.PostTool || e.Kind == model.CommandResult {
			ok := succeeded(e)
			if !ok && !failed(e) {
				continue
			}
			last = &ok
		}
	}
	if last == nil {
		return 0, false, nil
	}
	if *last {
		return 1, true, nil
	}
	return 0, true, nil
}

func related(m model.Memory, e model.Event) bool {
	var b strings.Builder
	for _, k := range []string{"command", "path", "error", "text", "rule", "skill"} {
		if s, ok := e.Payload[k].(string); ok {
			b.WriteString(s)
			b.WriteByte(' ')
		}
	}
	b.WriteString(e.PromptText)
	return grade(text.Groups(b.String()), text.Terms(m.Key+" "+m.Text)) > 0
}

func succeeded(e model.Event) bool {
	if strings.TrimSpace(payloadString(e, "error")) != "" {
		return false
	}
	code, ok := payloadCode(e)
	return ok && code == 0
}

func failed(e model.Event) bool {
	if strings.TrimSpace(payloadString(e, "error")) != "" {
		return true
	}
	code, ok := payloadCode(e)
	return ok && code != 0
}

func payloadString(e model.Event, k string) string {
	s, _ := e.Payload[k].(string)
	return s
}

func payloadCode(e model.Event) (int, bool) {
	switch n := e.Payload["exit_code"].(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
