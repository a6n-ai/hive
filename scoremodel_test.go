package inscribe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// seedSessionAs records a session the way capture does: each event is inserted
// and touches the session row, and the last touch is the session end.
// attempted, when set, is a consolidation attempt recorded through the store.
func seedSessionAs(t *testing.T, h *Inscribe, agent, session string, ended time.Time, attempted time.Time) {
	t.Helper()
	base := ended.Add(-time.Hour).UnixMilli()
	events := []model.Event{
		{TS: base, Agent: agent, Session: session, Project: "/p", Kind: model.Prompt, Digest: session + "-prompt"},
		{TS: base + 1, Agent: agent, Session: session, Project: "/p", Kind: model.PostTool, Digest: session + "-lint",
			Payload: map[string]any{"command": "npm run lint", "exit_code": 0}},
	}
	for i := range 18 {
		events = append(events, model.Event{TS: base + 2 + int64(i), Agent: agent, Session: session, Project: "/p",
			Kind: model.PostTool, Digest: fmt.Sprintf("%s-ls-%d", session, i), Payload: map[string]any{"command": "ls", "exit_code": 0}})
	}
	events = append(events, model.Event{TS: ended.UnixMilli(), Agent: agent, Session: session, Project: "/p", Kind: model.SessionEnd,
		Digest: session + "-end"})
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		for i := range events {
			if _, err := tx.InsertEvent(&events[i]); err != nil {
				return err
			}
			if err := tx.TouchSession(&events[i]); err != nil {
				return err
			}
		}
		if err := tx.InsertScoreOutcome(store.OutcomeInput{MemoryID: "m1", Session: session, Agent: agent, TS: base,
			Features: "[0,0.5,0,0,0,0,0,0]", Score: 0.5, PromptDigest: session + "-prompt", ShownKey: "npm run lint",
			ShownText: "check style before commit", LabelRule: recall.LabelRule}); err != nil {
			return err
		}
		if attempted.IsZero() {
			return nil
		}
		return tx.SetSessionAttempt(session, attempted.UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTrainScoresHoldsOnlyConsolidationCandidates(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	cases := []struct {
		name      string
		agent     string
		ended     time.Time
		attempted time.Time
		auto      bool
		wantHeld  bool
	}{
		{"ended 10m ago and pending consolidation is held", "claude-code", now.Add(-10 * time.Minute), time.Time{}, true, true},
		{"ended 10m ago for codex, which consolidation never reviews, is labeled", "codex", now.Add(-10 * time.Minute), time.Time{}, true, false},
		{"ended 2h ago and pending consolidation is labeled", "claude-code", now.Add(-2 * time.Hour), time.Time{}, true, false},
		{"ended 10m ago with a recent attempt is labeled", "claude-code", now.Add(-10 * time.Minute), now.Add(-10 * time.Minute), true, false},
		{"ended 10m ago with consolidation off is labeled", "claude-code", now.Add(-10 * time.Minute), time.Time{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := Open(Options{Home: t.TempDir(), Caller: "inscribe"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = h.Close() }()
			h.cfg.Consolidate.Auto = tc.auto
			seedSessionAs(t, h, tc.agent, "s", tc.ended, tc.attempted)

			res, err := h.TrainScores(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantHeld {
				if res.Open != 1 || len(res.Reasons) != 0 {
					t.Fatalf("result = %+v, want the session held open", res)
				}
				return
			}
			if res.Open != 0 || res.Reasons["full_command"] != 1 {
				t.Fatalf("result = %+v, want the session labeled", res)
			}
		})
	}
}
