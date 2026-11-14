package stats

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
	_ "modernc.org/sqlite"
)

type env struct {
	st  *store.Store
	db  *sql.DB
	dir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "inscribe.db")
	st, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(); _ = db.Close() })
	return &env{st, db, dir}
}

func (e *env) touch(t *testing.T, session, transcript string, kind model.EventKind, ts int64) {
	t.Helper()
	ev := &model.Event{Session: session, Agent: "claude-code", Project: "p", TS: ts, Kind: kind, TranscriptPath: transcript}
	if err := e.st.Write(context.Background(), func(tx *store.Tx) error { return tx.TouchSession(ev) }); err != nil {
		t.Fatal(err)
	}
}

func (e *env) status(t *testing.T, session string) (status sql.NullString, ingestedAt sql.NullInt64) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT usage_status, usage_ingested_at FROM sessions WHERE id = ?`, session).Scan(&status, &ingestedAt); err != nil {
		t.Fatal(err)
	}
	return
}

func (e *env) outputs(t *testing.T, session string) []int {
	t.Helper()
	rows, err := e.db.Query(`SELECT output FROM usage_turns WHERE session = ? ORDER BY ts, message_id`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func (e *env) write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(e.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/transcript_split.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var now = time.UnixMilli(5000)

func TestIngestSplitMessageAndSidechain(t *testing.T) {
	e := newEnv(t)
	p := e.write(t, "t.jsonl", fixture(t))
	e.touch(t, "s1", p, model.SessionEnd, 100)
	n, err := IngestUsage(e.st, 20, now)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := e.outputs(t, "s1"); len(got) != 2 || got[0] != 50 || got[1] != 4 {
		t.Fatalf("outputs = %v, want [50 4] (max of split message, sidechain counted)", got)
	}
	st, at := e.status(t, "s1")
	if st.String != "ok" || at.Int64 != 5000 {
		t.Fatalf("status=%v ingested_at=%v", st, at)
	}
	var in, cr, cc int
	var model string
	if err := e.db.QueryRow(`SELECT input, cache_read, cache_creation, model FROM usage_turns WHERE message_id = 'msg_A'`).Scan(&in, &cr, &cc, &model); err != nil {
		t.Fatal(err)
	}
	if in != 10 || cr != 100 || cc != 7 || model != "claude-opus" {
		t.Fatalf("msg_A = %d %d %d %s", in, cr, cc, model)
	}
	if n, _ := IngestUsage(e.st, 20, now); n != 0 {
		t.Fatalf("second run ingested %d, want 0", n)
	}
}

func TestIngestMissing(t *testing.T) {
	e := newEnv(t)
	e.touch(t, "s1", filepath.Join(e.dir, "gone.jsonl"), model.SessionEnd, 100)
	if n, err := IngestUsage(e.st, 20, now); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if st, _ := e.status(t, "s1"); st.String != "missing" {
		t.Fatalf("status = %v", st)
	}
}

func TestIngestUnparsed(t *testing.T) {
	e := newEnv(t)
	p := e.write(t, "t.jsonl", `{"type":"assistant","message":{"id":"a"}}`+"\n"+`{"type":"assistant","message":{"id":"b"}}`+"\n")
	e.touch(t, "s1", p, model.SessionEnd, 100)
	if _, err := IngestUsage(e.st, 20, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.status(t, "s1"); st.String != "unparsed" {
		t.Fatalf("status = %v", st)
	}
	if got := e.outputs(t, "s1"); len(got) != 0 {
		t.Fatalf("rows = %v", got)
	}
}

func TestIngestNoAssistantLines(t *testing.T) {
	e := newEnv(t)
	p := e.write(t, "t.jsonl", `{"type":"user","message":{"content":"hi"}}`+"\n")
	e.touch(t, "s1", p, model.SessionEnd, 100)
	if _, err := IngestUsage(e.st, 20, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.status(t, "s1"); st.String != "ok" {
		t.Fatalf("status = %v", st)
	}
	if got := e.outputs(t, "s1"); len(got) != 0 {
		t.Fatalf("rows = %v", got)
	}
}

func TestIngestReopened(t *testing.T) {
	e := newEnv(t)
	line := func(id string, out int, ts string) string {
		return `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"` + id + `","model":"m","usage":{"input_tokens":1,"output_tokens":` + string(rune('0'+out)) + `}}}` + "\n"
	}
	p := e.write(t, "t.jsonl", line("m1", 3, "2026-11-14T10:00:00Z"))
	e.touch(t, "s1", p, model.SessionEnd, 100)
	if _, err := IngestUsage(e.st, 20, now); err != nil {
		t.Fatal(err)
	}
	e.touch(t, "s1", "", model.PostTool, 6000)
	e.touch(t, "s1", "", model.SessionEnd, 7000)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line("m2", 4, "2026-11-14T11:00:00Z")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if n, err := IngestUsage(e.st, 20, time.UnixMilli(8000)); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := e.outputs(t, "s1"); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("outputs = %v, want [3 4]", got)
	}
}

func TestIngestPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	e := newEnv(t)
	p := e.write(t, "t.jsonl", fixture(t))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	e.touch(t, "s1", p, model.SessionEnd, 100)
	n, err := IngestUsage(e.st, 20, now)
	if err == nil || n != 0 {
		t.Fatalf("n=%d err=%v, want error and 0", n, err)
	}
	if st, at := e.status(t, "s1"); st.Valid || at.Valid {
		t.Fatalf("session marked: %v %v", st, at)
	}
}
