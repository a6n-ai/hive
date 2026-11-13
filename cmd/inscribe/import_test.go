package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportClaudeMemDryRunLeavesNoDB(t *testing.T) {
	src := filepath.Join(t.TempDir(), "cm.db")
	db, err := sql.Open("sqlite", "file:"+src)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE observations(id INTEGER PRIMARY KEY, memory_session_id TEXT, type TEXT, title TEXT, facts TEXT, files_read TEXT, files_modified TEXT, created_at_epoch INTEGER)`,
		`CREATE TABLE sdk_sessions(memory_session_id TEXT, cwd TEXT)`,
		`INSERT INTO sdk_sessions VALUES('s1', '/work/a')`,
		`INSERT INTO observations(memory_session_id, type, title, facts, files_read, files_modified, created_at_epoch) VALUES('s1', 'decision', 'Keep', '[]', '', '', 1000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("INSCRIBE_HOME", home)
	var out bytes.Buffer
	if err := importClaudeMemCmd([]string{"--db", src, "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "read=1 imported=1 skipped=0") {
		t.Fatalf("report: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "inscribe.db")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created inscribe.db: %v", err)
	}
}
