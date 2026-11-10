package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "home", "inscribe.db")
	st, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

func TestOpenCreatesFileWithMode0600(t *testing.T) {
	_, path := openTemp(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("db mode = %o, want 600", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %o, want 700", got)
	}
}

func TestPragmas(t *testing.T) {
	st, _ := openTemp(t)
	var mode string
	var busy, fk int
	if err := st.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || busy != 20 || fk != 1 {
		t.Fatalf("journal_mode=%s busy_timeout=%d foreign_keys=%d", mode, busy, fk)
	}
}

func event(digest string) *model.Event {
	return &model.Event{TS: 1000, Agent: "claude-code", Session: "s1", Project: "/p", Kind: model.Prompt, PromptText: "hi", Digest: digest}
}

func TestInsertEventDigestIdempotent(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	var first, second bool
	if err := st.Write(ctx, func(tx *Tx) error {
		var err error
		first, err = tx.InsertEvent(event("d1"))
		if err != nil {
			return err
		}
		second, err = tx.InsertEvent(event("d1"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !first || second {
		t.Fatalf("inserted first=%v second=%v, want true false", first, second)
	}
	var n int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
}

func upsertTwice(t *testing.T, st *Store) *model.Memory {
	t.Helper()
	var got *model.Memory
	for i, digest := range []string{"e1", "e2"} {
		ts := int64(1000 * (i + 1))
		err := st.Write(context.Background(), func(tx *Tx) error {
			e := event(digest)
			e.TS = ts
			if _, err := tx.InsertEvent(e); err != nil {
				return err
			}
			m := &model.Memory{Type: model.Command, ScopeKind: model.ProjectScope, Scope: "/p", Key: "go test ./...",
				Body: map[string]any{"class": "test"}, Text: "tests: go test ./...", Confidence: 0.5, Status: model.Active,
				FirstSeen: ts, LastSeen: ts}
			var err error
			got, err = tx.UpsertMemory(m, nil, e.ID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func TestUpsertMemoryIncrementsCount(t *testing.T) {
	st, _ := openTemp(t)
	m := upsertTwice(t, st)
	if m.Count != 2 || m.LastSeen != 2000 {
		t.Fatalf("count=%d last_seen=%d, want 2 2000", m.Count, m.LastSeen)
	}
	var rows, ev int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&rows)
	_ = st.db.QueryRow("SELECT COUNT(*) FROM memory_evidence").Scan(&ev)
	if rows != 1 || ev != 2 {
		t.Fatalf("memories=%d evidence=%d, want 1 2", rows, ev)
	}
}

func TestFTSFindsMemory(t *testing.T) {
	st, _ := openTemp(t)
	upsertTwice(t, st)
	ms, _, err := st.SearchFTS([]string{"test"}, ListFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Key != "go test ./..." {
		t.Fatalf("SearchFTS = %+v", ms)
	}
}

func TestFTSPrefixFindsCJK(t *testing.T) {
	st, _ := openTemp(t)
	err := st.Write(context.Background(), func(tx *Tx) error {
		e := event("c1")
		if _, err := tx.InsertEvent(e); err != nil {
			return err
		}
		_, err := tx.UpsertMemory(&model.Memory{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p", Key: "k",
			Body: map[string]any{}, Text: "部署命令: make deploy", Confidence: 0.9, Status: model.Active, FirstSeen: 1, LastSeen: 1}, nil, e.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ms, _, err := st.SearchFTS([]string{"部署"}, ListFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("SearchFTS CJK = %d results, want 1", len(ms))
	}
}

func TestSchemaNewerOpensReadOnly(t *testing.T) {
	st, path := openTemp(t)
	if _, err := st.db.Exec("UPDATE meta SET v = '999' WHERE k = 'schema_version'"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st2, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open newer schema: %v", err)
	}
	defer func() { _ = st2.Close() }()
	err = st2.Write(context.Background(), func(tx *Tx) error { return nil })
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("Write err = %v, want ErrSchemaNewer", err)
	}
	if _, err := st2.ListMemories(ListFilter{}); err != nil {
		t.Fatalf("reader on newer schema: %v", err)
	}
}
