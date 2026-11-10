package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/model"
)

func changeTestMemory(id string) *model.Memory {
	return &model.Memory{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p", Key: "k-" + id,
		Body: map[string]any{}, Text: "alpha " + id, Confidence: 0.9, Status: model.Active, FirstSeen: 1, LastSeen: 1}
}

func upsertOne(t *testing.T, st *Store, m *model.Memory) string {
	t.Helper()
	var id string
	err := st.Write(context.Background(), func(tx *Tx) error {
		e := event("c-" + m.Key)
		if _, err := tx.InsertEvent(e); err != nil {
			return err
		}
		got, err := tx.UpsertMemory(m, nil, e.ID)
		if err != nil {
			return err
		}
		id = got.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigration0002AppliesOnV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inscribe.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		string(v1),
		`INSERT INTO meta(k, v) VALUES ('schema_version', '1')`,
		`INSERT INTO memories(id, type, scope_kind, scope, key, body, text, count, confidence, status, first_seen, last_seen)
		 VALUES ('m1', 'note', 'project', '/p', 'k', '{}', 'text', 1, 0.9, 'active', 5, 777)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if v, err := st.SchemaVersion(); err != nil || v != latestVersion() {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, latestVersion())
	}
	var updated int64
	if err := st.db.QueryRow(`SELECT updated_at FROM memories WHERE id = 'm1'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 777 {
		t.Fatalf("backfilled updated_at = %d, want last_seen 777", updated)
	}
}

func TestChangedSinceSeesInsertUpdateStatus(t *testing.T) {
	st, _ := openTemp(t)
	id := upsertOne(t, st, changeTestMemory("a"))

	ms, _, cur, err := st.ChangesSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != id || cur <= 0 {
		t.Fatalf("after insert: got %d memories, cursor %d; want the new memory", len(ms), cur)
	}

	time.Sleep(3 * time.Millisecond)
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.SetStatus(id, model.Candidate) }); err != nil {
		t.Fatal(err)
	}
	ms, _, next, err := st.ChangesSince(cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Status != model.Candidate || next <= cur {
		t.Fatalf("after SetStatus: got %d memories (status %v), cursor %d -> %d", len(ms), ms, cur, next)
	}
	cur = next

	time.Sleep(3 * time.Millisecond)
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.TouchUsed([]string{id}, 99) }); err != nil {
		t.Fatal(err)
	}
	ms, _, next, err = st.ChangesSince(cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 || next != cur {
		t.Fatalf("after TouchUsed: got %d memories, cursor %d -> %d; want no change", len(ms), cur, next)
	}
}

func TestTouchUsedDoesNotAdvanceUpdatedAt(t *testing.T) {
	st, _ := openTemp(t)
	id := upsertOne(t, st, changeTestMemory("a"))
	var before int64
	if err := st.db.QueryRow(`SELECT updated_at FROM memories WHERE id = ?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}

	time.Sleep(3 * time.Millisecond)
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.TouchUsed([]string{id}, 99) }); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := st.db.QueryRow(`SELECT updated_at FROM memories WHERE id = ?`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("TouchUsed moved updated_at from %d to %d", before, after)
	}
}

func TestDeletedSince(t *testing.T) {
	st, _ := openTemp(t)
	keep := upsertOne(t, st, changeTestMemory("keep"))
	gone := upsertOne(t, st, changeTestMemory("gone"))
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.DeleteMemory(gone) }); err != nil {
		t.Fatal(err)
	}
	_, ids, cur, err := st.ChangesSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{gone}) || cur <= 0 {
		t.Fatalf("DeletedMemoryIDsSince = %v, cursor %d; want [%s]", ids, cur, gone)
	}
	if slices.Contains(ids, keep) {
		t.Fatal("live memory reported as deleted")
	}
}

func TestSyncInsertAndDeleteBetweenReads(t *testing.T) {
	ctx := context.Background()
	st, path := openTemp(t)
	gone := upsertOne(t, st, changeTestMemory("gone"))
	_, _, from, err := st.ChangesSince(0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)

	other, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	var added string
	st.betweenReads = func() {
		st.betweenReads = nil
		added = upsertOne(t, other, changeTestMemory("added"))
		time.Sleep(3 * time.Millisecond)
		if err := other.Write(ctx, func(tx *Tx) error { return tx.DeleteMemory(gone) }); err != nil {
			t.Error(err)
		}
	}

	first, deleted, next, err := st.ChangesSince(from)
	if err != nil {
		t.Fatal(err)
	}
	second, deletedLater, _, err := st.ChangesSince(next)
	if err != nil {
		t.Fatal(err)
	}
	var changedIDs []string
	for _, m := range append(first, second...) {
		changedIDs = append(changedIDs, m.ID)
	}
	if !slices.Contains(changedIDs, added) {
		t.Fatalf("memory inserted between the reads was lost: changed %v, cursor %d", changedIDs, next)
	}
	if !slices.Contains(append(deleted, deletedLater...), gone) {
		t.Fatalf("deletion between the reads was lost: deleted %v", append(deleted, deletedLater...))
	}
}
