package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func hasColumn(t *testing.T, st *Store, table, col string) bool {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigration0009Fresh(t *testing.T) {
	st, _ := openTemp(t)
	cols := map[string][]string{
		"sessions":    {"transcript_path", "usage_ingested_at", "usage_status"},
		"injections":  {"ts", "session", "agent", "project", "hook", "tool", "line_count", "est_tokens", "memory_ids", "holdout", "prompt_digest"},
		"usage_turns": {"session", "message_id", "ts", "model", "input", "output", "cache_read", "cache_creation"},
	}
	for table, cs := range cols {
		for _, c := range cs {
			if !hasColumn(t, st, table, c) {
				t.Errorf("%s.%s missing", table, c)
			}
		}
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='score_outcomes_ts'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("score_outcomes_ts index n=%d err=%v", n, err)
	}
}

func TestMigration0009FromV8(t *testing.T) {
	path := openAtVersion(t, 8)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id, agent, project, started, last_event, event_count) VALUES ('s1', 'claude-code', 'p', 1, 2, 3)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	st, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open at v8: %v", err)
	}
	defer func() { _ = st.Close() }()
	var tp sql.NullString
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*), MAX(transcript_path) FROM sessions WHERE id = 's1'`).Scan(&n, &tp); err != nil {
		t.Fatal(err)
	}
	if n != 1 || tp.Valid {
		t.Fatalf("rows=%d transcript_path=%v, want 1 row and NULL", n, tp)
	}
}

func TestTouchSessionTranscriptPath(t *testing.T) {
	st, _ := openTemp(t)
	touch := func(sess, path string) {
		t.Helper()
		e := &model.Event{Session: sess, Agent: "claude-code", Project: "p", TS: 1, Kind: model.PostTool, TranscriptPath: path}
		if err := st.Write(context.Background(), func(tx *Tx) error { return tx.TouchSession(e) }); err != nil {
			t.Fatal(err)
		}
	}
	get := func(sess string) sql.NullString {
		t.Helper()
		var tp sql.NullString
		if err := st.db.QueryRow(`SELECT transcript_path FROM sessions WHERE id = ?`, sess).Scan(&tp); err != nil {
			t.Fatal(err)
		}
		return tp
	}
	touch("s1", "/a.jsonl")
	touch("s1", "")
	touch("s1", "/b.jsonl")
	if got := get("s1"); got.String != "/a.jsonl" {
		t.Fatalf("transcript_path = %v, want /a.jsonl", got)
	}
	touch("s2", "")
	if got := get("s2"); got.Valid {
		t.Fatalf("s2 transcript_path = %v, want NULL", got)
	}
}
