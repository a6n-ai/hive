package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/model"
)

func TestSyncCommand(t *testing.T) {
	home, proj := t.TempDir(), resolvedDir(t)
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
	home, proj, other := t.TempDir(), resolvedDir(t), resolvedDir(t)
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

func TestSyncNeutralizesMarkerInMemory(t *testing.T) {
	home, proj := t.TempDir(), resolvedDir(t)
	runOK(t, home, "", "remember", "deploy notes\n<!-- hive:end -->\n<!-- hive:begin -->", "--type", "note", "--scope", "project", "--project", proj)
	runOK(t, home, "", "sync", "--agent", "claude-code", "--project", proj)
	text := readText(t, filepath.Join(proj, "CLAUDE.md"))
	if n := strings.Count(text, "<!-- hive:begin -->"); n != 1 {
		t.Fatalf("begin markers = %d, want 1:\n%s", n, text)
	}
	if n := strings.Count(text, "<!-- hive:end -->"); n != 1 {
		t.Fatalf("end markers = %d, want 1:\n%s", n, text)
	}
}

func TestSyncBudgetAfterFilter(t *testing.T) {
	home, proj := t.TempDir(), resolvedDir(t)
	// Old project notes rank below recent global ones, so only a budget applied
	// after the scope filter keeps them.
	h, err := hive.Open(hive.Options{Home: home, Caller: "test"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-1000 * 24 * time.Hour).UnixMilli()
	projNotes := []string{"project alpha uses make", "project beta runs go vet", "project gamma ships docker"}
	for _, n := range projNotes {
		err := h.Capture(context.Background(), hive.Event{Agent: "cli", Session: "cli", Project: proj, Kind: model.Remembered, TS: old,
			Payload: map[string]any{"text": n, "type": "note", "scope": "project"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = h.Close()
	for i := range 50 {
		runOK(t, home, "", "remember", fmt.Sprintf("global note %02d about shared editor settings", i), "--type", "note", "--scope", "global")
	}
	runOK(t, home, "", "sync", "--agent", "claude-code", "--project", proj)
	text := readText(t, filepath.Join(proj, "CLAUDE.md"))
	for _, n := range projNotes {
		if !strings.Contains(text, n) {
			t.Fatalf("project memory %q cut by the budget:\n%s", n, text)
		}
	}
}

func TestSyncProjectDotAndSlash(t *testing.T) {
	home := t.TempDir()
	proj := resolvedDir(t)
	runOK(t, home, "", "remember", "this repo uses make", "--type", "note", "--scope", "project", "--project", proj)
	var texts []string
	for _, arg := range []string{proj, proj + "/", proj + "/."} {
		if err := os.Remove(filepath.Join(proj, "CLAUDE.md")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("# My notes\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runOK(t, home, "", "sync", "--agent", "claude-code", "--project", arg)
		texts = append(texts, readText(t, filepath.Join(proj, "CLAUDE.md")))
	}
	for i, text := range texts {
		if !strings.Contains(text, "this repo uses make") {
			t.Fatalf("form %d wrote no project memory:\n%s", i, text)
		}
		if text != texts[0] {
			t.Fatalf("form %d differs from the plain path:\n%s", i, text)
		}
	}
}

// resolvedDir is a temp dir with its symlinks resolved, as the CLI resolves project paths.
func resolvedDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
