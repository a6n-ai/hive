package recall

import (
	"context"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/trajectory"
	"github.com/a6n-ai/inscribe/model"
)

func TestPromptMatchesTrajectoryJSON(t *testing.T) {
	st := newStore(t)
	var linked *model.Memory
	err := st.Write(context.Background(), func(tx *store.Tx) error {
		e := &model.Event{
			TS: now.UnixMilli(), Agent: "codex", Session: "s", Project: "/p",
			Kind: model.PostTool, Tool: "Bash", Digest: "trajdigest0123456789",
			Payload: map[string]any{"command": "npm run lint", "exit_code": 0},
		}
		if _, err := tx.InsertEvent(e); err != nil {
			return err
		}
		if err := tx.InsertTrajectory(e, trajectory.Normalize(e)); err != nil {
			return err
		}
		m := &model.Memory{
			Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p", Key: "style",
			Text: "check style before commit", Confidence: 0.9, Status: model.Active,
			FirstSeen: now.UnixMilli(), LastSeen: now.UnixMilli(), Body: map[string]any{},
		}
		var err error
		linked, err = tx.UpsertMemory(m, nil, e.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	put(t, st, model.Memory{Type: model.Note, Key: "deploy", Text: "deploy with make"})

	lines, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "npm run lint", Mode: Prompt}, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%-6s  %s", "score", "memory")
	for _, l := range lines {
		t.Logf("%6.3f  %s", l.Score, l.Text)
	}
	if len(lines) != 1 || lines[0].MemoryID != linked.ID {
		t.Fatalf("lines = %+v, want only the memory cited by the trajectory JSON", lines)
	}
	if strings.Contains(lines[0].Text, "deploy") {
		t.Fatalf("deploy was injected from a trajectory miss")
	}
}
