package recall

import (
	"context"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

const (
	openGroupBatch = 500
	replayGroups   = 5000
	// unlabelableAfter is how long a group whose prompt never shows up stays open.
	unlabelableAfter = 7 * 24 * time.Hour
)

// learningReasons are the labels that move the weights. closed_unlabeled and no_lift move nothing.
var learningReasons = map[string]bool{"full_command": true, "forgotten": true, "corrected": true, "failed": true, "sibling": true}

// TrainResult is one idle pass over injections whose session has already ended.
type TrainResult struct {
	Reasons map[string]int // labels this pass resolved, by reason
	Open    int            // groups left unresolved: the prompt is not in the events yet
	Refit   bool
	Weights Weights
}

// Train is the sleep-time pass. The agent is not waiting. Each group of lines
// shown for one prompt in a finished session is labeled from what the agent
// did next. The weights are what the next prompt reads. Nothing is sent to a model.
func Train(ctx context.Context, st *store.Store, now time.Time) (TrainResult, error) {
	return train(ctx, st, now, nil)
}

// train is Train with a hook that runs between the read and the write. Tests use it to make another writer win.
func train(ctx context.Context, st *store.Store, now time.Time, beforeWrite func()) (TrainResult, error) {
	w, err := LoadWeights(st)
	if err != nil {
		return TrainResult{}, err
	}
	open, err := st.OpenScoreGroups(openGroupBatch)
	if err != nil {
		return TrainResult{}, err
	}
	res := TrainResult{Reasons: map[string]int{}}
	var labeled []Labeled
	var fresh [][]store.ScoreOutcome
	events := map[string][]model.Event{}
	for _, rows := range open {
		session := rows[0].Session
		evs, cached := events[session]
		if !cached {
			if evs, err = st.SessionEvents(session); err != nil {
				return TrainResult{}, err
			}
			events[session] = evs
		}
		labels, ok, err := LabelGroup(rows, evs, rows[0].Ended, st.ForgetTime, w)
		if err != nil {
			return TrainResult{}, err
		}
		if !ok {
			if now.Sub(time.UnixMilli(rows[0].Ended)) <= unlabelableAfter {
				res.Open++
				continue
			}
			for _, r := range rows {
				res.Reasons["closed_unlabeled"]++
				labeled = append(labeled, Labeled{ID: r.ID, Reason: "closed_unlabeled"})
			}
			continue
		}
		g := make([]store.ScoreOutcome, len(rows))
		for i, r := range rows {
			l := labels[i]
			res.Reasons[l.Reason]++
			labeled = append(labeled, l)
			r.Label, r.Reason, r.Scale = l.Label, l.Reason, l.Scale
			g[i] = r
		}
		fresh = append(fresh, g)
	}

	refit := w.StepRule != StepRule
	for _, l := range labeled {
		if learningReasons[l.Reason] {
			refit = true
		}
	}
	if refit {
		replay, err := st.LabeledScoreOutcomes(LabelRule, replayGroups)
		if err != nil {
			return TrainResult{}, err
		}
		groups, err := refitGroups(replay, fresh)
		if err != nil {
			return TrainResult{}, err
		}
		if len(groups) == 0 {
			refit, w = false, DefaultWeights()
		} else {
			w = Refit(DefaultWeights(), groups)
		}
	}
	res.Refit, res.Weights = refit, w
	if len(labeled) == 0 && !refit {
		return res, nil
	}

	ts := now.UnixMilli()
	if beforeWrite != nil {
		beforeWrite()
	}
	if err := st.Write(ctx, func(tx *store.Tx) error {
		for _, l := range labeled {
			n, err := tx.ResolveScoreOutcome(l.ID, l.Label, l.Reason, l.Scale, ts)
			if err != nil {
				return err
			}
			if n == 0 {
				return store.ErrLocked
			}
		}
		if refit {
			return SaveWeightsTx(tx, w)
		}
		return nil
	}); err != nil {
		return TrainResult{}, err
	}
	return res, nil
}

// refitGroups merges this pass's labels into the replay set by (session, prompt
// digest) and keeps the last replayGroups groups that carry a learning label.
func refitGroups(replay, fresh [][]store.ScoreOutcome) ([]Group, error) {
	type key struct{ session, digest string }
	index := map[key]int{}
	var merged [][]store.ScoreOutcome
	for _, g := range append(replay, fresh...) {
		k := key{g[0].Session, g[0].PromptDigest}
		if i, ok := index[k]; ok {
			merged[i] = append(merged[i], g...)
			continue
		}
		index[k] = len(merged)
		merged = append(merged, g)
	}
	var groups []Group
	for _, g := range merged {
		var grp Group
		for _, r := range g {
			if r.Label == nil || !learningReasons[r.Reason] {
				continue
			}
			f, err := parseFeatures(r.Features)
			if err != nil {
				return nil, err
			}
			grp = append(grp, Row{F: f, Label: *r.Label, Reason: r.Reason, Scale: r.Scale})
		}
		if len(grp) > 0 {
			groups = append(groups, grp)
		}
	}
	if len(groups) > replayGroups {
		groups = groups[len(groups)-replayGroups:]
	}
	return groups, nil
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
