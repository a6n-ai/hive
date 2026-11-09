package hive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
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

func TestRecallSeesOtherProcessForget(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h := openIndexed(t, home)
	defer func() { _ = h.Close() }()
	id, _ := h.Remember(ctx, "deploy via make", model.Note, model.ProjectScope, "/p")
	if ids := promptIDs(t, h, "deploy"); !slices.Equal(ids, []string{id}) {
		t.Fatalf("recall before forget = %v, want [%s]", ids, id)
	}

	other, err := Open(Options{Home: home, Caller: "hive"})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Forget(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	if ids := promptIDs(t, h, "deploy"); len(ids) != 0 {
		t.Fatalf("recall served a memory another process forgot: %v", ids)
	}
}

func TestSnapshotFromOtherDatabaseRebuilt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h := openIndexed(t, home)
	for i := range 60 {
		if _, err := h.Remember(ctx, fmt.Sprintf("note %d about deploys", i), model.Note, model.ProjectScope, "/p"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	oldSnap, err := os.ReadFile(filepath.Join(home, indexFile))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"hive.db", "hive.db-wal", "hive.db-shm"} {
		if err := os.Remove(filepath.Join(home, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	fresh, err := Open(Options{Home: home, Caller: "hive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Remember(ctx, "one fresh note", model.Note, model.ProjectScope, "/p"); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, indexFile), oldSnap, 0o600); err != nil {
		t.Fatal(err)
	}

	h = openIndexed(t, home)
	defer func() { _ = h.Close() }()
	if n := indexLen(t, h); n != 1 {
		t.Fatalf("index holds %d memories after the database was replaced, want 1", n)
	}
}

func TestSnapshotNewerThanStoreRebuilt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h := openIndexed(t, home)
	if _, err := h.Remember(ctx, "deploy via make", model.Note, model.ProjectScope, "/p"); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	// Restoring an older copy of the database leaves the snapshot's cursor ahead of every change.
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(home, "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM memories; UPDATE memory_deletions SET ts = 1`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	h = openIndexed(t, home)
	defer func() { _ = h.Close() }()
	if n := indexLen(t, h); n != 0 {
		t.Fatalf("index holds %d memories from a snapshot newer than the store, want 0", n)
	}
}

func TestConcurrentSyncAndRecallRace(t *testing.T) {
	ctx := context.Background()
	h := openIndexed(t, t.TempDir())
	defer func() { _ = h.Close() }()

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				// Lock contention between writers is not under test; only the index's state is.
				id, _ := h.Remember(ctx, fmt.Sprintf("deploy step %d-%d via make", g, i), model.Note, model.ProjectScope, "/p")
				if i%3 == 0 && id != "" {
					_ = h.Forget(ctx, id, false)
				}
				if _, err := h.Recall(ctx, recall.Query{Project: "/p", Text: "deploy", Mode: recall.Prompt}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
