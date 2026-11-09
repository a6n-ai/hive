package hive

import (
	"context"
	"testing"

	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

func TestHiveRememberWhyForget(t *testing.T) {
	ctx := context.Background()
	h, err := Open(Options{Home: t.TempDir(), Caller: "hive"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()

	id, err := h.Remember(ctx, "deploys go through make deploy", model.Note, model.ProjectScope, "/p")
	if err != nil || id == "" {
		t.Fatalf("Remember: id=%q err=%v", id, err)
	}
	ms, _ := h.List(ctx, store.ListFilter{Types: []model.MemoryType{model.Note}})
	if len(ms) != 1 || ms[0].ID != id {
		t.Fatalf("List = %+v", ms)
	}
	ev, err := h.Why(ctx, id)
	if err != nil || len(ev) != 1 || ev[0].Kind != model.Remembered {
		t.Fatalf("Why = %+v err=%v", ev, err)
	}
	q := recall.Query{Project: "/p", Text: "how do we deploy", Mode: recall.Prompt}
	if lines, _ := h.Recall(ctx, q); len(lines) != 1 {
		t.Fatalf("Recall before forget = %+v", lines)
	}
	if err := h.Forget(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if lines, _ := h.Recall(ctx, q); len(lines) != 0 {
		t.Fatalf("Recall after forget = %+v", lines)
	}
	if err := h.Forget(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if ms, _ := h.List(ctx, store.ListFilter{}); len(ms) != 0 {
		t.Fatalf("purged memory still listed: %+v", ms)
	}
}

func TestQueryEvents(t *testing.T) {
	ctx := context.Background()
	h, err := Open(Options{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	for i, k := range []model.EventKind{model.Prompt, model.TurnEnd, model.Prompt} {
		if err := h.Capture(ctx, Event{TS: int64(i + 1), Agent: "a", Session: "s", Project: "/p", Kind: k, PromptText: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := h.QueryEvents(ctx, EventFilter{Session: "s", Kinds: []model.EventKind{model.Prompt}})
	if err != nil || len(evs) != 2 {
		t.Fatalf("QueryEvents = %d (%v), want 2", len(evs), err)
	}
}
