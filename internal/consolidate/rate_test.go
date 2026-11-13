package consolidate_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/consolidate"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// injected records that memoryID was shown in the session and no feedback came back.
func (e *env) injected(memoryID string) {
	e.t.Helper()
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		return tx.InsertScoreOutcome(store.OutcomeInput{MemoryID: memoryID, Session: session, Agent: "claude-code",
			TS: 1, Features: "{}", Score: 0.5, PromptDigest: "d", ShownKey: "k", ShownText: "k", LabelRule: "shown"})
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// outcomes returns every recorded outcome of memoryID, resolved or not.
func (e *env) outcomes(memoryID string) []store.ScoreOutcome {
	e.t.Helper()
	groups, err := e.st.RecentScoreGroups(100)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.ScoreOutcome
	for _, g := range groups {
		for _, o := range g {
			if o.MemoryID == memoryID {
				out = append(out, o)
			}
		}
	}
	return out
}

func (e *env) globalMemory(key string, evidence int64) string {
	e.t.Helper()
	var id string
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		m, err := tx.UpsertMemory(&model.Memory{Type: model.Preference, ScopeKind: model.Global, Key: key,
			Text: "global " + key, Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1}, nil, evidence)
		if err != nil {
			return err
		}
		id = m.ID
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestRateLabelsUnrated(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	m := e.memory("m", ids[0])
	e.injected(m)
	r := &fakeRunner{out: fmt.Sprintf(`{"ops":[{"op":"rate","id":%q,"used":"yes","helpful":"yes"}]}`, m)}
	rep := e.run(r, false)
	if rep.Status != "ok" || len(rep.Valid) != 1 {
		t.Fatalf("status = %s valid = %d rejected = %v", rep.Status, len(rep.Valid), rep.Rejected)
	}
	rows := e.outcomes(m)
	if len(rows) != 1 || rows[0].Label == nil || *rows[0].Label != 1 || rows[0].Reason != "review" || rows[0].Scale != 0.3 {
		t.Fatalf("outcomes = %+v, want one row labeled 1 with reason review and scale 0.3", rows)
	}
}

func TestRateWrongPenalizes(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	project := e.memory("m", ids[0])
	e.injected(project)
	global := e.globalMemory("g", ids[0])
	e.injected(global)
	r := &fakeRunner{out: fmt.Sprintf(`{"ops":[{"op":"rate","id":%q,"used":"no","helpful":"wrong"},{"op":"rate","id":%q,"used":"no","helpful":"wrong"}]}`,
		project, global)}
	rep := e.run(r, false)
	if rep.Status != "ok" || len(rep.Valid) != 2 {
		t.Fatalf("status = %s valid = %d rejected = %v; global rate must be allowed", rep.Status, len(rep.Valid), rep.Rejected)
	}
	if got := e.get(project).Penalty; math.Abs(got-0.2) > 1e-9 {
		t.Errorf("project penalty = %v, want 0.2", got)
	}
	if got := e.get(global).Penalty; math.Abs(got-0.05) > 1e-9 {
		t.Errorf("global penalty = %v, want 0.05", got)
	}
	rows := e.outcomes(project)
	if len(rows) != 1 || rows[0].Label == nil || *rows[0].Label != 0 || rows[0].Reason != "review_wrong" {
		t.Errorf("outcomes = %+v, want one row labeled 0 with reason review_wrong", rows)
	}
}

func TestRateRejectsIDNotUnrated(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	injected := e.memory("m", ids[0])
	e.injected(injected)
	notInjected := e.memory("n", ids[1])
	r := &fakeRunner{out: fmt.Sprintf(`{"ops":[{"op":"rate","id":%q,"used":"yes","helpful":"yes"},{"op":"rate","id":%q,"used":"maybe","helpful":"yes"}]}`,
		notInjected, injected)}
	rep := e.run(r, false)
	if len(rep.Valid) != 0 || len(rep.Rejected) != 2 {
		t.Fatalf("valid = %d rejected = %v, want 0 valid and 2 rejected", len(rep.Valid), rep.Rejected)
	}
	if got := e.get(notInjected).Penalty; got != 0 {
		t.Errorf("penalty of an unrated-by-input memory = %v, want 0", got)
	}
	if rows := e.outcomes(injected); len(rows) != 1 || rows[0].Label != nil {
		t.Errorf("outcomes = %+v, want the rejected rate to leave the row unlabeled", rows)
	}
}

func TestUndoRateRestores(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	m := e.memory("m", ids[0])
	e.injected(m)
	r := &fakeRunner{out: fmt.Sprintf(`{"ops":[{"op":"rate","id":%q,"used":"no","helpful":"wrong"}]}`, m)}
	rep := e.run(r, false)
	if got := e.get(m).Penalty; math.Abs(got-0.2) > 1e-9 {
		t.Fatalf("penalty after rate = %v, want 0.2", got)
	}
	undo, err := consolidate.Undo(context.Background(), e.st, rep.RunID)
	if err != nil || len(undo.Skipped) != 0 {
		t.Fatalf("undo = %+v (%v), want nothing skipped", undo, err)
	}
	if got := e.get(m).Penalty; got != 0 {
		t.Errorf("penalty after undo = %v, want 0", got)
	}
	rows := e.outcomes(m)
	if len(rows) != 1 || rows[0].Label != nil || rows[0].Reason != "" || rows[0].Scale != 1 {
		t.Errorf("outcome after undo = %+v, want unlabeled, empty reason, scale 1", rows)
	}
	open, err := e.st.UnaskedOutcomes(session, 10)
	if err != nil || len(open) != 1 || open[0].MemoryID != m {
		t.Errorf("unasked outcomes after undo = %+v (%v), want the reopened row", open, err)
	}
}

// runnerFunc adapts a function to Runner, so a test can act while the review runs.
type runnerFunc func(ctx context.Context, prompt string) (string, error)

func (f runnerFunc) Run(ctx context.Context, prompt string) (string, error) { return f(ctx, prompt) }

// resolveRows resolves the session's unresolved rows of memoryID, as train does after a session's hold.
func (e *env) resolveRows(memoryID string) {
	e.t.Helper()
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.ResolveSessionMemoryRows(session, memoryID, 1, "train", 1, 2)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestRateSkipsStaleRate(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	m := e.memory("m", ids[0])
	e.injected(m)
	out := fmt.Sprintf(`{"ops":[{"op":"rate","id":%q,"used":"no","helpful":"wrong"}]}`, m)
	r := runnerFunc(func(context.Context, string) (string, error) {
		e.resolveRows(m)
		return out, nil
	})
	rep := e.run(r, false)
	if len(rep.Rejected) != 1 || rep.Rejected[0].Reason != "memory was rated after the review" {
		t.Fatalf("rejected = %+v, want the rate skipped as rated after the review", rep.Rejected)
	}
	if got := e.get(m).Penalty; got != 0 {
		t.Errorf("penalty = %v, want 0", got)
	}
	for _, o := range e.outcomes(m) {
		if o.Reason == "review" || o.Reason == "review_wrong" {
			t.Errorf("outcome = %+v, want no review rows", o)
		}
	}
}

func TestUndoRateScopedToItsRun(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	m := e.memory("m", ids[0])
	e.injected(m)
	out := fmt.Sprintf(`{"ops":[{"op":"rate","id":%q,"used":"yes","helpful":"yes"}]}`, m)
	first := e.run(&fakeRunner{out: out}, false)
	if len(first.Valid) != 1 {
		t.Fatalf("first run valid = %d rejected = %v, want 1 valid", len(first.Valid), first.Rejected)
	}
	time.Sleep(2 * time.Millisecond) // each run stamps its resolved rows with its own clock; keep the two runs apart
	e.events(1)
	e.injected(m)
	if second := e.run(&fakeRunner{out: out}, false); len(second.Valid) != 1 {
		t.Fatalf("second run valid = %d rejected = %v, want 1 valid", len(second.Valid), second.Rejected)
	}
	undo, err := consolidate.Undo(context.Background(), e.st, first.RunID)
	if err != nil || undo.Restored != 1 || len(undo.Skipped) != 0 {
		t.Fatalf("undo = %+v (%v), want 1 restored and none skipped", undo, err)
	}
	var open, labeled int
	for _, o := range e.outcomes(m) {
		switch {
		case o.Label == nil:
			open++
		case o.Reason == "review":
			labeled++
		}
	}
	if open != 1 || labeled != 1 {
		t.Errorf("outcomes = %+v, want run 1's row reopened and run 2's row still labeled", e.outcomes(m))
	}
}
