package importer_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/importer"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
	_ "modernc.org/sqlite"
)

type cmRow struct {
	session, typ, title, facts, filesRead, filesModified string
	epoch                                                int64
}

// claudeMemDB writes a claude-mem database holding only the columns the importer reads.
func claudeMemDB(t *testing.T, sessions map[string]string, rows []cmRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-mem.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE observations(id INTEGER PRIMARY KEY, memory_session_id TEXT, project TEXT, type TEXT, title TEXT, subtitle TEXT, facts TEXT, narrative TEXT, files_read TEXT, files_modified TEXT, created_at_epoch INTEGER)`)
	exec(`CREATE TABLE sdk_sessions(memory_session_id TEXT, cwd TEXT)`)
	for id, cwd := range sessions {
		exec(`INSERT INTO sdk_sessions(memory_session_id, cwd) VALUES(?, ?)`, id, cwd)
	}
	for _, r := range rows {
		exec(`INSERT INTO observations(memory_session_id, project, type, title, facts, files_read, files_modified, created_at_epoch) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			r.session, "proj", r.typ, r.title, r.facts, r.filesRead, r.filesModified, r.epoch)
	}
	return path
}

func TestReadClaudeMemMapsTypesAndScope(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a", "s2": "/work/b"}, []cmRow{
		{session: "s1", typ: "decision", title: "Use WAL", facts: `["fast reads","x"]`, epoch: 1700000000123},
		{session: "s2", typ: "bugfix", title: "Fix nil map", facts: `[]`, epoch: 1700000000},
	})
	evs, rep, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 2 || rep.Imported != 2 || rep.Skipped != 0 || len(evs) != 2 {
		t.Fatalf("report = %+v events = %d", rep, len(evs))
	}
	a, b := evs[0], evs[1]
	if a.Agent != "claude-mem" || a.Session != "claude-mem-import" || a.Kind != model.Remembered {
		t.Fatalf("event identity = %+v", a)
	}
	if a.Project != "/work/a" || a.TS != 1700000000123 || a.Payload["text"] != "Use WAL. fast reads" || a.Payload["type"] != "decision" {
		t.Fatalf("decision event = %+v", a)
	}
	if a.Payload["scope"] != "project" || a.Payload["confidence"] != 0.6 {
		t.Fatalf("decision payload = %+v", a.Payload)
	}
	if b.Project != "/work/b" || b.TS != 1700000000000 || b.Payload["text"] != "Fix nil map" || b.Payload["type"] != "fix" {
		t.Fatalf("bugfix event = %+v", b)
	}
}

func TestReadClaudeMemSkips(t *testing.T) {
	const at = 1700000000000
	db := claudeMemDB(t, map[string]string{"s1": "/work/a", "pk": "/work/apps/puchkaman"}, []cmRow{
		{session: "ghost", typ: "decision", title: "No session", epoch: at},
		{session: "s1", typ: "sensitive", title: "Secret", epoch: at},
		{session: "s1", typ: "decision", title: "Touches app", filesModified: `["apps/puchkaman/main.ts"]`, epoch: at},
		{session: "pk", typ: "decision", title: "Runs in app", epoch: at},
		{session: "s1", typ: "decision", title: "Undated"},
		{session: "s1", typ: "decision", title: "", epoch: at},
		{session: "s1", typ: "discovery", title: "Found", epoch: at},
		{session: "s1", typ: "decision", title: "Keep", epoch: at},
	})
	evs, rep, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"no_cwd": 1, "sensitive": 1, "puchkaman": 2, "no_time": 1, "empty_title": 1, "type": 1}
	if rep.Read != 8 || rep.Imported != 1 || rep.Skipped != 7 || len(evs) != 1 || evs[0].Payload["text"] != "Keep" {
		t.Fatalf("report = %+v events = %d", rep, len(evs))
	}
	if !reflect.DeepEqual(rep.SkippedByReason, want) {
		t.Fatalf("SkippedByReason = %v, want %v", rep.SkippedByReason, want)
	}
}

func TestReadClaudeMemScopesToGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	db := claudeMemDB(t, map[string]string{"s1": cwd}, []cmRow{{session: "s1", typ: "decision", title: "Keep", epoch: 1000}})
	ctx := context.Background()
	evs, _, err := importer.ReadClaudeMem(ctx, importer.ClaudeMemOptions{DB: db})
	if err != nil || len(evs) != 1 || evs[0].Project != resolved {
		t.Fatalf("event project: events=%+v want %q err=%v", evs, resolved, err)
	}
	evs, _, err = importer.ReadClaudeMem(ctx, importer.ClaudeMemOptions{DB: db, Project: filepath.Join(root, "sub")})
	if err != nil || len(evs) != 1 {
		t.Fatalf("project filter inside repo: events=%d err=%v", len(evs), err)
	}
}

func TestReadClaudeMemProjectAndSince(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a", "s2": "/work/b"}, []cmRow{
		{session: "s1", typ: "decision", title: "Old A", epoch: 1700000000000},
		{session: "s2", typ: "decision", title: "New B", epoch: 1700000100000},
	})
	ctx := context.Background()
	evs, rep, err := importer.ReadClaudeMem(ctx, importer.ClaudeMemOptions{DB: db, Project: "/work/a"})
	if err != nil || len(evs) != 1 || rep.SkippedByReason["project"] != 1 {
		t.Fatalf("project scope: events=%d rep=%+v err=%v", len(evs), rep, err)
	}
	evs, rep, err = importer.ReadClaudeMem(ctx, importer.ClaudeMemOptions{DB: db, Since: time.UnixMilli(1700000050000)})
	if err != nil || len(evs) != 1 || evs[0].Payload["text"] != "New B" || rep.SkippedByReason["since"] != 1 {
		t.Fatalf("since: events=%v rep=%+v err=%v", evs, rep, err)
	}
}

func TestReadClaudeMemReadOnly(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{{session: "s1", typ: "decision", title: "Keep", epoch: 1000}})
	if err := os.Chmod(db, 0o444); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(db) //nolint:gosec // G304: test fixture path under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	evs, _, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: db})
	if err != nil || len(evs) != 1 {
		t.Fatalf("read-only read: events=%d err=%v", len(evs), err)
	}
	after, err := os.ReadFile(db) //nolint:gosec // G304: test fixture path under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("claude-mem database bytes changed")
	}
}

func TestImportClaudeMemScrubsSecrets(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{
		{session: "s1", typ: "decision", title: "Rotate CI", facts: `["token=ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`, epoch: 1000},
	})
	ctx := context.Background()
	h, _ := openHome(t)
	if _, err := h.ImportClaudeMem(ctx, importer.ClaudeMemOptions{DB: db}); err != nil {
		t.Fatal(err)
	}
	ms, err := h.List(ctx, store.ListFilter{})
	if err != nil || len(ms) != 1 {
		t.Fatalf("memories = %+v err=%v", ms, err)
	}
	if strings.Contains(ms[0].Text, "ghp_aaaa") {
		t.Fatalf("secret stored in memory text: %q", ms[0].Text)
	}
}

func TestImportClaudeMemIdempotent(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{{session: "s1", typ: "bugfix", title: "Fix nil map", epoch: 1000}})
	ctx := context.Background()
	h, _ := openHome(t)
	if _, err := h.ImportClaudeMem(ctx, importer.ClaudeMemOptions{DB: db}); err != nil {
		t.Fatal(err)
	}
	first, _ := h.List(ctx, store.ListFilter{})
	if _, err := h.ImportClaudeMem(ctx, importer.ClaudeMemOptions{DB: db}); err != nil {
		t.Fatal(err)
	}
	second, _ := h.List(ctx, store.ListFilter{})
	if len(first) != 1 || len(second) != len(first) {
		t.Fatalf("memories after first = %d, after second = %d", len(first), len(second))
	}
}

func TestImportClaudeMemConfidence(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{{session: "s1", typ: "decision", title: "Use WAL", epoch: 1000}})
	ctx := context.Background()
	h, _ := openHome(t)
	if _, err := h.ImportClaudeMem(ctx, importer.ClaudeMemOptions{DB: db}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Remember(ctx, "deploys go through make deploy", model.Decision, model.ProjectScope, "/work/a"); err != nil {
		t.Fatal(err)
	}
	ms, _ := h.List(ctx, store.ListFilter{})
	conf := map[string]float64{}
	for _, m := range ms {
		conf[m.Text] = m.Confidence
	}
	if conf["Use WAL"] != 0.6 || conf["deploys go through make deploy"] != 0.9 {
		t.Fatalf("confidence = %v", conf)
	}
}

func TestImportClaudeMemDryRunWritesNothing(t *testing.T) {
	db := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{{session: "s1", typ: "decision", title: "Use WAL", epoch: 1000}})
	ctx := context.Background()
	h, _ := openHome(t)
	rep, err := h.ImportClaudeMem(ctx, importer.ClaudeMemOptions{DB: db, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 1 || rep.Imported != 1 {
		t.Fatalf("dry-run report = %+v", rep)
	}
	if ms, _ := h.List(ctx, store.ListFilter{}); len(ms) != 0 {
		t.Fatalf("dry run stored %d memories", len(ms))
	}
}

func TestReadClaudeMemSpecialPathChars(t *testing.T) {
	src := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{{session: "s1", typ: "decision", title: "Keep", epoch: 1000}})
	dir := t.TempDir()
	hash := filepath.Join(dir, "ha#sh.db")
	if err := os.Rename(src, hash); err != nil {
		t.Fatal(err)
	}
	evs, _, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: hash})
	if err != nil || len(evs) != 1 {
		t.Fatalf("# path: events=%d err=%v", len(evs), err)
	}
	if _, _, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: filepath.Join(dir, "we?ird.db")}); err == nil {
		t.Fatal("missing ? path read without error")
	}
	if _, _, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: filepath.Join(dir, "ha#missing.db")}); err == nil {
		t.Fatal("missing # path read without error")
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 {
		t.Fatalf("directory changed: %v err=%v", ents, err)
	}
}

func TestReadClaudeMemRelativePath(t *testing.T) {
	src := claudeMemDB(t, map[string]string{"s1": "/work/a"}, []cmRow{{session: "s1", typ: "decision", title: "Keep", epoch: 1000}})
	t.Chdir(filepath.Dir(src))
	evs, _, err := importer.ReadClaudeMem(context.Background(), importer.ClaudeMemOptions{DB: filepath.Base(src)})
	if err != nil || len(evs) != 1 {
		t.Fatalf("relative path: events=%d err=%v", len(evs), err)
	}
}
