package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/extract"
	"github.com/a6n-ai/hive/internal/scrub"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

func newPipeline(t *testing.T) (*Pipeline, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hive.db")
	st, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sc, err := scrub.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Pipeline{Store: st, Scrub: sc, Extract: &extract.Registry{}, Cfg: config.Defaults()}, path
}

func TestResolveProject(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(root, "a"), link); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{root, sub, link} {
		if got := ResolveProject(cwd); got != root {
			t.Errorf("ResolveProject(%s) = %s, want %s", cwd, got, root)
		}
	}
	plain := t.TempDir()
	plain, _ = filepath.EvalSymlinks(plain)
	if got := ResolveProject(plain); got != plain {
		t.Errorf("non-git ResolveProject = %s, want %s", got, plain)
	}
}

func TestPolicyTruncatesOnRuneBoundary(t *testing.T) {
	e := model.Event{Kind: model.Prompt, PromptText: strings.Repeat("a", 1999) + "😀界"}
	ApplyPolicy(&e)
	if !utf8.ValidString(e.PromptText) || utf8.RuneCountInString(e.PromptText) > 2000 {
		t.Fatalf("prompt runes=%d valid=%v", utf8.RuneCountInString(e.PromptText), utf8.ValidString(e.PromptText))
	}
	big := model.Event{Kind: model.Prompt, PromptText: strings.Repeat("x", 1<<20)}
	ApplyPolicy(&big)
	if utf8.RuneCountInString(big.PromptText) != 2000 {
		t.Fatalf("1MB prompt -> %d runes, want 2000", utf8.RuneCountInString(big.PromptText))
	}
}

func TestPolicyErrorText(t *testing.T) {
	e := model.Event{Kind: model.PostTool, Tool: "shell", Payload: map[string]any{"error": strings.Repeat("e", 300) + "\nline2\nline3\nline4\nline5"}}
	ApplyPolicy(&e)
	got := e.Payload["error"].(string)
	if strings.Count(got, "\n") > 1 || utf8.RuneCountInString(got) > 500 {
		t.Fatalf("error kept %d lines, %d runes", strings.Count(got, "\n")+1, utf8.RuneCountInString(got))
	}
}

func TestPolicySecretFileKeepsPathOnly(t *testing.T) {
	e := model.Event{Kind: model.PostTool, Tool: "read", Payload: map[string]any{"path": "/x/.env.local", "output": "API=1", "error": "boom"}}
	ApplyPolicy(&e)
	if len(e.Payload) != 1 || e.Payload["path"] != "/x/.env.local" {
		t.Fatalf("payload = %v, want only path", e.Payload)
	}
}

func TestPolicyDropsFileContents(t *testing.T) {
	e := model.Event{Kind: model.PostTool, Tool: "edit", Payload: map[string]any{"path": "/x/a.go", "old_string": "x", "new_string": "y", "output": "z"}}
	ApplyPolicy(&e)
	if _, ok := e.Payload["new_string"]; ok || e.Payload["path"] != "/x/a.go" {
		t.Fatalf("payload = %v", e.Payload)
	}
}

func TestDeriveSkillLoaded(t *testing.T) {
	e := model.Event{Kind: model.PostTool, Tool: "skill", Payload: map[string]any{"skill": "superpowers:brainstorming"}}
	d := Derive(&e, nil)
	if len(d) != 1 || d[0].Kind != model.SkillLoaded || d[0].Payload["skill"] != "superpowers:brainstorming" {
		t.Fatalf("derived = %+v", d)
	}
}

func TestDeriveCommandResult(t *testing.T) {
	e := model.Event{Kind: model.PostTool, Tool: "shell", Payload: map[string]any{"command": "go test ./...", "exit_code": 0}}
	d := Derive(&e, nil)
	if len(d) != 1 || d[0].Kind != model.CommandResult || d[0].Payload["command"] != "go test ./..." || d[0].Payload["exit_code"] != 0 {
		t.Fatalf("derived = %+v", d)
	}
}

func TestDeriveCorrection(t *testing.T) {
	prev := &model.Event{Kind: model.TurnEnd}
	e := model.Event{Kind: model.Prompt, PromptText: "no, use rg not grep"}
	d := Derive(&e, prev)
	if len(d) != 1 || d[0].Kind != model.Correction || d[0].Payload["rule"] != "use rg not grep" {
		t.Fatalf("derived = %+v", d)
	}
	if d := Derive(&model.Event{Kind: model.Prompt, PromptText: "now add a test"}, prev); len(d) != 0 {
		t.Fatalf("non-correction derived %+v", d)
	}
	if d := Derive(&e, &model.Event{Kind: model.PostTool}); len(d) != 0 {
		t.Fatalf("correction without preceding turn end derived %+v", d)
	}
}

func TestCaptureScrubsBeforeWrite(t *testing.T) {
	p, path := newPipeline(t)
	secret := "sk-" + "ant-" + "api03-" + strings.Repeat("Zx7q", 8)
	err := p.Capture(context.Background(), model.Event{TS: 1, Agent: "claude-code", Session: "s", Project: "/p",
		Kind: model.Prompt, PromptText: "export ANTHROPIC_API_KEY=" + secret})
	if err != nil {
		t.Fatal(err)
	}
	evs, err := p.Store.SessionEvents("s")
	if err != nil || len(evs) != 1 || !strings.Contains(evs[0].PromptText, "[REDACTED:") {
		t.Fatalf("events = %+v err=%v", evs, err)
	}
	for _, f := range []string{path, path + "-wal"} {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("%s contains the secret", f)
		}
	}
}

func TestExcludedProjectNotCaptured(t *testing.T) {
	p, _ := newPipeline(t)
	p.Cfg.Exclude.Paths = []string{"/tmp/secret-proj"}
	for _, proj := range []string{"/tmp/secret-proj", "/tmp/secret-proj/sub"} {
		if err := p.Capture(context.Background(), model.Event{TS: 1, Agent: "a", Session: "x", Project: proj, Kind: model.Prompt, PromptText: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Capture(context.Background(), model.Event{TS: 2, Agent: "a", Session: "y", Project: "/tmp/secret-project-other", Kind: model.Prompt, PromptText: "hi"}); err != nil {
		t.Fatal(err)
	}
	if evs, _ := p.Store.SessionEvents("x"); len(evs) != 0 {
		t.Fatalf("excluded project captured %d events", len(evs))
	}
	if evs, _ := p.Store.SessionEvents("y"); len(evs) != 1 {
		t.Fatalf("sibling path wrongly excluded")
	}
}

type countingExtractor struct{ n int }

func (c *countingExtractor) Kinds() []model.EventKind { return []model.EventKind{model.CommandResult} }
func (c *countingExtractor) Extract(context.Context, *model.Event, *store.Tx, config.Config) error {
	c.n++
	return nil
}

func TestSpooledEventsAreExtractedOnDrain(t *testing.T) {
	p, path := newPipeline(t)
	ce := &countingExtractor{}
	p.Extract.Register(ce)
	// Simulate an event that was spooled by another process while locked.
	e := model.Event{TS: 5, Agent: "a", Session: "s", Project: "/p", Kind: model.PostTool, Tool: "shell",
		Payload: map[string]any{"command": "go test ./...", "exit_code": 0}}
	e.Digest = Digest(&e)
	if err := p.Store.Spool(&e); err != nil {
		t.Fatal(err)
	}
	_ = path
	if err := p.Capture(context.Background(), model.Event{TS: 6, Agent: "a", Session: "s", Project: "/p", Kind: model.Prompt, PromptText: "next"}); err != nil {
		t.Fatal(err)
	}
	if ce.n != 1 {
		t.Fatalf("extractor ran %d times on drained command_result, want 1", ce.n)
	}
}

type failingExtractor struct{}

func (failingExtractor) Kinds() []model.EventKind { return []model.EventKind{model.Prompt} }
func (failingExtractor) Extract(context.Context, *model.Event, *store.Tx, config.Config) error {
	return errors.New("boom")
}

func TestCaptureSpoolsWhenWriteFails(t *testing.T) {
	p, path := newPipeline(t)
	p.Extract.Register(failingExtractor{})
	e := model.Event{TS: 1000, Agent: "claude-code", Session: "s1", Project: "/p", Kind: model.Prompt, PromptText: "hi"}
	if err := p.Capture(context.Background(), e); err != nil {
		t.Fatalf("Capture returned %v; a failed write must spool the event", err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "spool", fmt.Sprintf("%d.jsonl", os.Getpid())))
	if err != nil || !bytes.Contains(b, []byte(`"prompt"`)) {
		t.Fatalf("spool = %q, err %v", b, err)
	}
}
