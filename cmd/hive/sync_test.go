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

func TestSyncWritesOnlyProjectMemories(t *testing.T) {
	home, proj, other := t.TempDir(), t.TempDir(), t.TempDir()
	runOK(t, home, "", "remember", "prefer tabs everywhere", "--type", "preference", "--scope", "global")
	runOK(t, home, "", "remember", "other repo uses bazel", "--type", "note", "--scope", "project", "--project", other)
	runOK(t, home, "", "remember", "this repo uses make", "--type", "note", "--scope", "project", "--project", proj)
	out := runOK(t, home, "", "sync", "--agent", "claude-code", "--project", proj)
	text := readText(t, filepath.Join(proj, "CLAUDE.md"))
	if !strings.Contains(text, "this repo uses make") {
		t.Fatalf("project memory missing:\n%s", text)
	}
	if strings.Contains(text, "prefer tabs everywhere") || strings.Contains(text, "other repo uses bazel") {
		t.Fatalf("memory from outside the project written to a committable file:\n%s", text)
	}
	if strings.Contains(out, "--include-global") {
		t.Fatalf("default output mentions --include-global: %q", out)
	}
	out = runOK(t, home, "", "sync", "--agent", "claude-code", "--project", proj, "--include-global")
	if !strings.Contains(out, "--include-global is on") {
		t.Fatalf("--include-global output = %q", out)
	}
	if text := readText(t, filepath.Join(proj, "CLAUDE.md")); !strings.Contains(text, "prefer tabs everywhere") {
		t.Fatalf("--include-global did not include the global memory:\n%s", text)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
