package recall

import (
	"sort"
	"strings"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// Labeled is the label one shown line earned. A nil Label means nothing to learn.
type Labeled struct {
	ID     int64
	Label  *int
	Reason string
	Scale  float64
}

// ForgetFunc reports when a memory was forgotten, if it was.
type ForgetFunc func(memoryID string) (int64, bool, error)

// ContentWords are the words of a command that can name a shown line. Tokens
// with a slash or a leading . or ~ are paths, so they are dropped.
func ContentWords(command string) []string {
	var keep []string
	for _, tok := range strings.Fields(command) {
		if strings.Contains(tok, "/") || strings.HasPrefix(tok, ".") || strings.HasPrefix(tok, "~") {
			continue
		}
		keep = append(keep, tok)
	}
	return text.Terms(strings.Join(keep, " "))
}

// LabelGroup labels every line shown for one prompt. ok is false when the
// prompt event is not in events yet, so the group stays open.
func LabelGroup(rows []store.ScoreOutcome, events []model.Event, ended int64, forget ForgetFunc, w Weights) ([]Labeled, bool, error) {
	if len(rows) == 0 {
		return nil, true, nil
	}
	evs := make([]model.Event, 0, len(events))
	for _, e := range events {
		if e.TS <= ended {
			evs = append(evs, e)
		}
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })

	var promptTS int64
	found := false
	for _, e := range evs {
		if e.Kind == model.Prompt && e.Digest == rows[0].PromptDigest {
			promptTS, found = e.TS, true
			break
		}
	}
	if !found {
		return nil, false, nil
	}
	noLift := make([]bool, len(rows))
	for i, r := range rows {
		f, err := parseFeatures(r.Features)
		if err != nil {
			return nil, false, err
		}
		_, _, d := blendRaw(f, w)
		noLift[i] = true
		for _, v := range d {
			if v > 0 {
				noLift[i] = false
			}
		}
	}

	var nextTS int64
	hasNext := false
	for _, e := range evs {
		if e.Kind == model.Prompt && e.TS > promptTS {
			nextTS, hasNext = e.TS, true
			break
		}
	}
	inGroup := func(ts int64) bool { return ts > promptTS && (!hasNext || ts < nextTS) }

	var closing *model.Event
	for i := range evs {
		e := evs[i]
		if (e.Kind != model.PostTool && e.Kind != model.CommandResult) || !inGroup(e.TS) {
			continue
		}
		if _, ok := payloadCode(e); !ok && strings.TrimSpace(payloadString(e, "error")) == "" {
			continue
		}
		closing = &evs[i]
		break
	}
	var cmdOK, cmdFailed bool
	var words []string
	if closing != nil {
		cmdOK, cmdFailed = succeeded(*closing), failed(*closing)
		words = ContentWords(payloadString(*closing, "command"))
	}
	canHit := len(words) >= 2

	n := len(rows)
	out := make([]Labeled, n)
	for i, r := range rows {
		out[i] = Labeled{ID: r.ID, Reason: "closed_unlabeled"}
		if noLift[i] {
			out[i].Reason = "no_lift"
		}
	}
	active := func(i int) bool { return !noLift[i] }

	forgotten := make([]bool, n)
	nForgot := 0
	for i, r := range rows {
		if !active(i) {
			continue
		}
		at, gone, err := forget(r.MemoryID)
		if err != nil {
			return nil, false, err
		}
		if gone && at <= ended {
			forgotten[i] = true
			nForgot++
		}
	}

	corrected := make([]bool, n)
	corrScale := make([]float64, n)
	for _, e := range evs {
		if e.Kind != model.Correction || e.TS <= promptTS {
			continue
		}
		var marked []int
		for i, r := range rows {
			if active(i) && !forgotten[i] && related(model.Memory{Key: r.ShownKey, Text: r.ShownText}, e) {
				marked = append(marked, i)
			}
		}
		for _, i := range marked {
			corrected[i] = true
			if s := 1 / float64(len(marked)); s > corrScale[i] {
				corrScale[i] = s
			}
		}
	}

	hitCand := make([]bool, n)
	if canHit {
		for i, r := range rows {
			if !active(i) || forgotten[i] || corrected[i] {
				continue
			}
			seen := map[string]bool{}
			for _, t := range text.Terms(r.ShownKey + " " + r.ShownText) {
				seen[t] = true
			}
			hitCand[i] = true
			for _, word := range words {
				if !seen[word] {
					hitCand[i] = false
					break
				}
			}
		}
	}

	setLabel := func(i int, label int, reason string, scale float64) {
		out[i] = Labeled{ID: rows[i].ID, Label: &label, Reason: reason, Scale: scale}
	}
	for i := range rows {
		if forgotten[i] {
			setLabel(i, 0, "forgotten", 1/float64(nForgot))
		}
	}
	for i := range rows {
		if corrected[i] {
			setLabel(i, 0, "corrected", corrScale[i])
		}
	}

	var failedIdx, hitIdx []int
	for i := range rows {
		if !active(i) || forgotten[i] || corrected[i] || !hitCand[i] {
			continue
		}
		if cmdFailed {
			failedIdx = append(failedIdx, i)
		} else if cmdOK {
			hitIdx = append(hitIdx, i)
		}
	}
	for _, i := range failedIdx {
		setLabel(i, 0, "failed", 1/float64(len(failedIdx)))
	}
	for _, i := range hitIdx {
		setLabel(i, 1, "full_command", 1/float64(len(hitIdx)))
	}

	if len(hitIdx) > 0 {
		var sib []int
		for i := range rows {
			if active(i) && !forgotten[i] && !corrected[i] && !hitCand[i] {
				sib = append(sib, i)
			}
		}
		for _, i := range sib {
			setLabel(i, 0, "sibling", 1/float64(len(sib)))
		}
	}
	return out, true, nil
}
