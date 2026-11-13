package recall

import (
	"context"
	"errors"
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

type shownLine struct {
	key, text string
	f         Features
}

// seedShown records the lines shown for one prompt in a session.
func seedShown(t *testing.T, st *store.Store, session, digest string, shown ...shownLine) {
	t.Helper()
	ids := make([]string, len(shown))
	for i, s := range shown {
		ids[i] = put(t, st, model.Memory{Type: model.Command, Key: s.key, Text: s.text}).ID
	}
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		for i, s := range shown {
			raw, err := s.f.marshal()
			if err != nil {
				return err
			}
			if err := tx.InsertScoreOutcome(store.OutcomeInput{MemoryID: ids[i], Session: session, Agent: "codex", TS: 100,
				Features: raw, Score: 0.5, PromptDigest: digest, ShownKey: s.key, ShownText: s.text, LabelRule: LabelRule}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// seedSession records a session's events and ends the session at 300.
func seedSession(t *testing.T, st *store.Store, session string, events ...model.Event) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		for i := range events {
			if _, err := tx.InsertEvent(&events[i]); err != nil {
				return err
			}
		}
		return tx.TouchSession(&model.Event{TS: 300, Agent: "codex", Session: session, Project: "/p", Kind: model.SessionEnd})
	}); err != nil {
		t.Fatal(err)
	}
}

func promptEvent(session, digest string) model.Event {
	return model.Event{TS: 100, Agent: "codex", Session: session, Project: "/p", Kind: model.Prompt, Digest: digest}
}

func commandEvent(session, digest, command string, exit int) model.Event {
	return model.Event{TS: 200, Agent: "codex", Session: session, Project: "/p", Kind: model.PostTool, Digest: digest,
		Payload: map[string]any{"command": command, "exit_code": exit}}
}

func TestTrainLabelsAndRefitsOnce(t *testing.T) {
	st := newStore(t)
	seedShown(t, st, "s1", "p1", shownLine{key: "npm run lint", text: "check style before commit", f: Features{Key: 0.5}})
	seedSession(t, st, "s1", promptEvent("s1", "p1"), commandEvent("s1", "c1", "npm run lint", 0))

	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reasons) != 1 || res.Reasons["full_command"] != 1 {
		t.Fatalf("reasons = %v, want only full_command=1", res.Reasons)
	}
	if !res.Refit {
		t.Fatal("a full_command label did not refit")
	}

	again, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Reasons) != 0 {
		t.Fatalf("second pass reasons = %v, want none", again.Reasons)
	}
	if again.Refit || again.Weights != res.Weights {
		t.Fatalf("second pass refit=%v weights=%+v, want no refit and %+v", again.Refit, again.Weights, res.Weights)
	}
}

func TestTrainRefitsOnStepRuleChange(t *testing.T) {
	st := newStore(t)
	old := DefaultWeights()
	old.StepRule = "S0"
	if err := SaveWeights(context.Background(), st, old); err != nil {
		t.Fatal(err)
	}

	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refit || res.Weights.StepRule != StepRule {
		t.Fatalf("refit=%v step=%q, want a refit to %q", res.Refit, res.Weights.StepRule, StepRule)
	}
	saved, err := LoadWeights(st)
	if err != nil {
		t.Fatal(err)
	}
	if saved.StepRule != StepRule {
		t.Fatalf("stored step rule = %q, want %q", saved.StepRule, StepRule)
	}
}

func TestTrainLeavesOpenWhenPromptMissing(t *testing.T) {
	st := newStore(t)
	seedShown(t, st, "s1", "p-missing", shownLine{key: "npm run lint", text: "check style before commit", f: Features{Key: 0.5}})
	seedSession(t, st, "s1", promptEvent("s1", "p1"), commandEvent("s1", "c1", "npm run lint", 0))

	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Open != 1 || len(res.Reasons) != 0 || res.Refit {
		t.Fatalf("result = %+v, want one open group and nothing labeled", res)
	}
	groups, err := st.OpenScoreGroups(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 1 {
		t.Fatalf("open groups = %d, want the one unresolved row", len(groups))
	}
}

func TestTrainSecondWriterLoses(t *testing.T) {
	st := newStore(t)
	seedShown(t, st, "s1", "p1", shownLine{key: "npm run lint", text: "check style before commit", f: Features{Key: 0.5}})
	seedSession(t, st, "s1", promptEvent("s1", "p1"), commandEvent("s1", "c1", "npm run lint", 0))
	groups, err := st.OpenScoreGroups(10)
	if err != nil {
		t.Fatal(err)
	}
	id := groups[0][0].ID

	trainHook = func() {
		if err := st.Write(context.Background(), func(tx *store.Tx) error {
			_, err := tx.ResolveScoreOutcome(id, nil, "no_lift", 0, now.UnixMilli())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { trainHook = nil })

	_, err = Train(context.Background(), st, now)
	if !errors.Is(err, store.ErrLocked) {
		t.Fatalf("Train err = %v, want ErrLocked", err)
	}
	if _, ok, err := st.GetMeta(weightsKey()); err != nil || ok {
		t.Fatalf("weights saved after a lost write: ok=%v err=%v", ok, err)
	}
}
