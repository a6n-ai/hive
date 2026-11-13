package inscribe

import (
	"context"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// seedDueSession makes a Claude Code session that consolidation is due for, with one
// injection shown for a prompt the agent then ran the command for. The later idle
// touch moves last_event back, which is the only way ended can be recent and Due true.
func seedDueSession(t *testing.T, h *Inscribe, session string, ended, idle time.Time) {
	t.Helper()
	ev := func(kind model.EventKind, ts int64, digest string, payload map[string]any) model.Event {
		return model.Event{TS: ts, Agent: "claude-code", Session: session, Project: "/p", Kind: kind, Digest: digest, Payload: payload}
	}
	events := []model.Event{
		ev(model.Prompt, 100, "p1", nil),
		ev(model.PostTool, 200, "c1", map[string]any{"command": "npm run lint", "exit_code": 0}),
	}
	for i := range 18 {
		events = append(events, ev(model.PostTool, 300+int64(i), "c2", map[string]any{"command": "ls", "exit_code": 0}))
	}
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		for i := range events {
			if _, err := tx.InsertEvent(&events[i]); err != nil {
				return err
			}
		}
		if err := tx.InsertScoreOutcome(store.OutcomeInput{MemoryID: "m1", Session: session, Agent: "claude-code", TS: 100,
			Features: "[0,0.5,0,0,0,0,0,0]", Score: 0.5, PromptDigest: "p1", ShownKey: "npm run lint",
			ShownText: "check style before commit", LabelRule: recall.LabelRule}); err != nil {
			return err
		}
		if err := tx.TouchSession(&model.Event{TS: ended.UnixMilli(), Agent: "claude-code", Session: session, Project: "/p", Kind: model.SessionEnd}); err != nil {
			return err
		}
		return tx.TouchSession(&model.Event{TS: idle.UnixMilli(), Agent: "claude-code", Session: session, Project: "/p", Kind: model.PostTool})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTrainScoresHoldsDueConsolidation(t *testing.T) {
	h, err := Open(Options{Home: t.TempDir(), Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()

	now := time.UnixMilli(1_800_000_000_000)
	seedDueSession(t, h, "held", now.Add(-10*time.Minute), now.Add(-2*time.Hour))
	seedDueSession(t, h, "old", now.Add(-2*time.Hour), now.Add(-2*time.Hour))

	res, err := h.TrainScores(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Open != 1 || res.Reasons["full_command"] != 1 {
		t.Fatalf("result = %+v, want the recently ended session held and the older one labeled", res)
	}
}
