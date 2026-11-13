package recall

import (
	"context"
	"errors"
	"testing"
	"time"

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
	seedSessionAt(t, st, session, 300, events...)
}

func seedSessionAt(t *testing.T, st *store.Store, session string, ended int64, events ...model.Event) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		for i := range events {
			if _, err := tx.InsertEvent(&events[i]); err != nil {
				return err
			}
		}
		return tx.TouchSession(&model.Event{TS: ended, Agent: "codex", Session: session, Project: "/p", Kind: model.SessionEnd})
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
	seedShown(t, st, "s1", "p1", shownLine{key: "npm run lint", text: "check style before commit", f: Features{Key: 0.5}})
	seedSession(t, st, "s1", promptEvent("s1", "p1"), commandEvent("s1", "c1", "npm run lint", 0))
	if _, err := Train(context.Background(), st, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), func(tx *store.Tx) error { return tx.SetMeta(weightsKey(), "") }); err != nil {
		t.Fatal(err)
	}

	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refit || res.Weights.StepRule != StepRule {
		t.Fatalf("refit=%v step=%q, want a refit to %q", res.Refit, res.Weights.StepRule, StepRule)
	}
	raw, ok, err := st.GetMeta(weightsKey())
	if err != nil || !ok || raw == "" {
		t.Fatalf("weights not saved under %s: ok=%v err=%v", weightsKey(), ok, err)
	}
}

func TestTrainDoesNotWriteWithNothingToReplay(t *testing.T) {
	st := newStore(t)
	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refit {
		t.Fatal("refit with nothing to replay")
	}
	if _, ok, err := st.GetMeta(weightsKey()); err != nil || ok {
		t.Fatalf("weights written: ok=%v err=%v", ok, err)
	}
}

func TestTrainClosesStaleUnlabelableGroups(t *testing.T) {
	st := newStore(t)
	seedShown(t, st, "old", "p-missing", shownLine{key: "npm run lint", text: "check style", f: Features{Key: 0.5}})
	seedSessionAt(t, st, "old", now.Add(-8*24*time.Hour).UnixMilli(), promptEvent("old", "p1"))
	seedShown(t, st, "new", "p-missing", shownLine{key: "npm run lint", text: "check style", f: Features{Key: 0.5}})
	seedSessionAt(t, st, "new", now.Add(-time.Hour).UnixMilli(), promptEvent("new", "p1"))

	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Open != 1 || res.Reasons["closed_unlabeled"] != 1 || res.Refit {
		t.Fatalf("result = %+v, want one closed_unlabeled, one open, no refit", res)
	}
	groups, err := st.OpenScoreGroups(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0][0].Session != "new" {
		t.Fatalf("open groups = %v, want only the young session", groups)
	}
}

func TestTrainLeavesOpenWhenPromptMissing(t *testing.T) {
	st := newStore(t)
	seedShown(t, st, "s1", "p-missing", shownLine{key: "npm run lint", text: "check style before commit", f: Features{Key: 0.5}})
	seedSessionAt(t, st, "s1", now.Add(-time.Hour).UnixMilli(), promptEvent("s1", "p1"), commandEvent("s1", "c1", "npm run lint", 0))

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
