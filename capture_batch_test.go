package hive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

func promptBatch(n int, prefix string) []Event {
	evs := make([]Event, n)
	for i := range evs {
		evs[i] = Event{Agent: "claude", Session: fmt.Sprintf("%s%d", prefix, i), Project: "/p", Kind: model.Prompt, PromptText: "hello"}
	}
	return evs
}

// holdWriteLock takes the database write lock from another connection until release is called.
func holdWriteLock(t *testing.T, dbPath string) (release func()) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	return func() {
		_, _ = conn.ExecContext(ctx, "COMMIT")
		_ = conn.Close()
		_ = db.Close()
	}
}

func spoolFiles(t *testing.T, home string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(home, "spool", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func spooledLines(t *testing.T, home string) int {
	t.Helper()
	n := 0
	for _, f := range spoolFiles(t, home) {
		data, err := os.ReadFile(f) //nolint:gosec // G304: test fixture path under t.TempDir
		if err != nil {
			t.Fatal(err)
		}
		n += strings.Count(string(data), "\n")
	}
	return n
}

func TestCaptureBatchOneTransaction(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h, err := Open(Options{Home: home, Caller: "hive"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()

	release := holdWriteLock(t, filepath.Join(home, "hive.db"))
	if err := h.CaptureBatch(ctx, promptBatch(100, "locked")); err != nil {
		t.Fatal(err)
	}
	release()
	if n := spooledLines(t, home); n != 100 {
		t.Fatalf("locked batch spooled %d lines, want 100", n)
	}
	if files := spoolFiles(t, home); len(files) != 1 {
		t.Fatalf("locked batch used %d spool files, want 1", len(files))
	}

	if err := h.CaptureBatch(ctx, promptBatch(100, "open")); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := h.store.Counts(); events != 200 {
		t.Fatalf("events after open batch = %d, want 200 (100 drained + 100 new)", events)
	}
}

type poisonExtractor struct{}

func (poisonExtractor) Kinds() []model.EventKind { return []model.EventKind{model.Prompt} }

func (poisonExtractor) Extract(_ context.Context, e *model.Event, _ *store.Tx, _ config.Config) error {
	if strings.HasPrefix(e.Session, "poison") {
		return errors.New("extractor failed")
	}
	return nil
}

func TestCaptureBatchPoisonIsolated(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h, err := Open(Options{Home: home, Caller: "hive"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	h.RegisterExtractor(poisonExtractor{})

	evs := []Event{promptBatch(1, "ok")[0], promptBatch(1, "poison")[0], promptBatch(1, "fine")[0]}
	if err := h.CaptureBatch(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := h.store.Counts(); events != 2 {
		t.Fatalf("events = %d, want 2: the poisoned event must roll back alone", events)
	}
	bad, _ := filepath.Glob(filepath.Join(home, "spool", "bad", "poison-*.jsonl"))
	if len(bad) != 1 {
		t.Fatalf("quarantine holds %d files, want 1", len(bad))
	}
}

func TestWriterKeepsIndexCurrent(t *testing.T) {
	ctx := context.Background()
	h, err := Open(Options{Home: t.TempDir(), Caller: "hive", Writer: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()

	var evs []Event
	for _, text := range []string{"deploy via make", "deploy canary with make canary"} {
		evs = append(evs, Event{Agent: "cli", Session: "cli", Project: "/p", Kind: model.Remembered,
			Payload: map[string]any{"text": text, "type": string(model.Note), "scope": string(model.ProjectScope)}})
	}
	if err := h.CaptureBatch(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if n := indexLen(t, h); n != 2 {
		t.Fatalf("index holds %d memories after the batch, want 2", n)
	}
	if ids := promptIDs(t, h, "deploy"); len(ids) != 2 {
		t.Fatalf("recall after the batch = %v, want 2 memories", ids)
	}
}
