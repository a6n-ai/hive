package consolidate_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/a6n-ai/hive/internal/consolidate"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// seedSessionAt stores n prompts in session, the last one age before now.
func seedSessionAt(t *testing.T, e *env, name string, n int, age time.Duration) {
	t.Helper()
	for i := range n {
		ev := model.Event{TS: time.Now().Add(-age).UnixMilli() + int64(i), Agent: "claude-code", Session: name, Project: "/p",
			Kind: model.Prompt, PromptText: "a prompt", Digest: fmt.Sprintf("%s-%d", name, i)}
		err := e.st.Write(context.Background(), func(tx *store.Tx) error {
			if _, err := tx.InsertEvent(&ev); err != nil {
				return err
			}
			return tx.TouchSession(&ev)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCatchUpBounded(t *testing.T) {
	e := newEnv(t)
	e.cfg.Consolidate.MinEvents = 20
	for i := range 10 {
		seedSessionAt(t, e, fmt.Sprintf("c%d", i), 25, time.Duration(2+i)*time.Hour)
	}
	seedSessionAt(t, e, "ancient", 25, 8*24*time.Hour)

	due, err := consolidate.Due(e.st, e.cfg, "claude-code", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 3 {
		t.Errorf("due = %v, want the 3 newest sessions", due)
	}
	if slices.Contains(due, "ancient") {
		t.Error("a session idle for more than 7 days was queued")
	}

	first := due[0]
	rep, err := consolidate.Run(context.Background(), e.st, e.cfg, e.scrub, &fakeRunner{out: "not json"}, first, false)
	if err != nil || rep.Status != "failed" {
		t.Fatalf("run = %+v (%v), want failed", rep, err)
	}
	due, err = consolidate.Due(e.st, e.cfg, "claude-code", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(due, first) {
		t.Errorf("session that failed within 24 h was queued again: %v", due)
	}
}
