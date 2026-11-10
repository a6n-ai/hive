package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncCommand(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	runOK(t, home, "", "remember", "use make deploy for releases", "--type", "note", "--scope", "project", "--project", proj)
	file := filepath.Join(proj, "CLAUDE.md")
	if err := os.WriteFile(file, []byte("# My notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runOK(t, home, "", "sync", "--agent", "claude-code", "--project", proj)
	if !strings.Contains(out, "This file may be committed to your repository; review the block before committing.") {
		t.Fatalf("sync output = %q", out)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"# My notes", "## Hive memory (generated; edits here are replaced)", "use make deploy for releases"} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLAUDE.md lacks %q:\n%s", want, text)
		}
	}
	runOK(t, home, "", "sync", "--agent", "claude-code", "--project", proj)
	again, _ := os.ReadFile(file)
	if string(again) != text {
		t.Fatalf("second sync changed the file:\n%s", again)
	}
}
