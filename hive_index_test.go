package hive

import (
	"context"
	"slices"
	"testing"

	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

func openIndexed(t *testing.T, home string) *Hive {
	t.Helper()
	h, err := Open(Options{Home: home, Caller: "hive", Index: true})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func indexLen(t *testing.T, h *Hive) int {
	t.Helper()
	if h.index == nil {
		t.Fatal("Options.Index did not build an index")
	}
	return h.index.Len()
}

func promptIDs(t *testing.T, h *Hive, text string) []string {
	t.Helper()
	lines, err := h.Recall(context.Background(), recall.Query{Project: "/p", Text: text, Mode: recall.Prompt})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range lines {
		ids = append(ids, l.MemoryID)
	}
	return ids
}

func TestForgottenLeavesIndex(t *testing.T) {
	ctx := context.Background()
	h := openIndexed(t, t.TempDir())
	defer func() { _ = h.Close() }()

	keep, _ := h.Remember(ctx, "deploys go through make deploy", model.Note, model.ProjectScope, "/p")
	gone, _ := h.Remember(ctx, "deploy blue pipeline first", model.Note, model.ProjectScope, "/p")
	purged, _ := h.Remember(ctx, "deploy rollback runs make rollback", model.Note, model.ProjectScope, "/p")
	if n := indexLen(t, h); n != 3 {
		t.Fatalf("index holds %d memories after Remember, want 3", n)
	}
	if ids := promptIDs(t, h, "how do we deploy"); len(ids) != 3 {
		t.Fatalf("recall before forget = %v, want 3", ids)
	}

	if err := h.Forget(ctx, gone, false); err != nil {
		t.Fatal(err)
	}
	if err := h.Forget(ctx, purged, true); err != nil {
		t.Fatal(err)
	}
	if ids := promptIDs(t, h, "how do we deploy"); !slices.Equal(ids, []string{keep}) {
		t.Fatalf("recall after forget = %v, want [%s]", ids, keep)
	}
	if n := indexLen(t, h); n != 1 {
		t.Fatalf("index holds %d memories after forget and purge, want 1", n)
	}

	if err := h.store.Write(ctx, func(tx *store.Tx) error { return tx.SetStatus(keep, model.Superseded) }); err != nil {
		t.Fatal(err)
	}
	if err := h.syncIndex(); err != nil {
		t.Fatal(err)
	}
	if ids := promptIDs(t, h, "how do we deploy"); len(ids) != 0 {
		t.Fatalf("superseded memory still recalled: %v", ids)
	}
}

func TestIndexSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	h := openIndexed(t, home)
	first, _ := h.Remember(ctx, "deploy via make", model.Note, model.ProjectScope, "/p")
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	// Another process, without the index, writes while this one is closed.
	other, err := Open(Options{Home: home, Caller: "hive"})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := other.Remember(ctx, "deploy canary with make canary", model.Note, model.ProjectScope, "/p")
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	h = openIndexed(t, home)
	defer func() { _ = h.Close() }()
	if n := indexLen(t, h); n != 2 {
		t.Fatalf("reopened index holds %d memories, want 2", n)
	}
	if ids := promptIDs(t, h, "deploy"); !slices.Contains(ids, first) || !slices.Contains(ids, second) {
		t.Fatalf("recall after reopen = %v, want %s and %s", ids, first, second)
	}
}
