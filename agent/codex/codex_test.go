package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/model"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCodexParse(t *testing.T) {
	cases := []struct {
		name, event, file string
		kind              model.EventKind
		tool              string
		payload           map[string]any
		prompt            string
	}{
		{name: "prompt", event: "UserPromptSubmit", file: "UserPromptSubmit.json", kind: model.Prompt, prompt: "fix the build"},
		{name: "shell string", event: "PostToolUse", file: "PostToolUse_shell.json", kind: model.PostTool,
			tool: "shell", payload: map[string]any{"command": "go test ./...", "exit_code": 0}},
		{name: "shell list", event: "PostToolUse", file: "PostToolUse_shell_list.json", kind: model.PostTool,
			tool: "shell", payload: map[string]any{"command": "bash -lc 'go test ./...'", "exit_code": 0}},
		{name: "shell failure", event: "PostToolUse", file: "PostToolUse_shell_fail.json", kind: model.PostTool,
			tool: "shell", payload: map[string]any{"command": "go test", "exit_code": 2, "error": "boom"}},
		{name: "apply_patch", event: "PostToolUse", file: "PostToolUse_apply_patch.json", kind: model.PostTool,
			tool: "edit", payload: map[string]any{"path": "a.go", "paths": []string{"a.go", "b.go"}, "exit_code": 0}},
		{name: "unknown tool", event: "PostToolUse", file: "PostToolUse_web_search.json", kind: model.PostTool,
			tool: "web_search", payload: map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := New().Parse(c.event, fixture(t, c.file))
			if err != nil {
				t.Fatal(err)
			}
			if e.Agent != "codex" || e.Session != "s1" || e.Kind != c.kind {
				t.Fatalf("event = %+v", e)
			}
			if e.PromptText != c.prompt || e.Tool != c.tool {
				t.Fatalf("prompt %q tool %q, want %q %q", e.PromptText, e.Tool, c.prompt, c.tool)
			}
			if c.kind == model.PostTool && !reflect.DeepEqual(e.Payload, c.payload) {
				t.Fatalf("payload = %#v, want %#v", e.Payload, c.payload)
			}
		})
	}
}

func TestCodexParseRejects(t *testing.T) {
	if _, err := New().Parse("SessionStart", fixture(t, "SessionStart_nosession.json")); err == nil {
		t.Fatal("missing session_id accepted")
	}
	if _, err := New().Parse("PreToolUse", fixture(t, "PostToolUse_shell.json")); err == nil {
		t.Fatal("unknown event accepted")
	}
	if _, err := New().Parse("Stop", []byte("{not json")); err == nil {
		t.Fatal("malformed payload accepted")
	}
}

func TestCodexReply(t *testing.T) {
	lines := []recall.Line{{Type: model.Preference, Text: "prefer rg over grep"}}
	out := New().Reply("SessionStart", lines)
	if !strings.Contains(string(out), `"hookEventName":"SessionStart"`) || !strings.Contains(string(out), "prefer rg over grep") {
		t.Fatalf("reply = %s", out)
	}
	if New().Reply("Stop", lines) != nil {
		t.Fatal("Stop should reply with nothing")
	}
	if New().Reply("SessionStart", nil) != nil {
		t.Fatal("no lines should reply with nothing")
	}
}

func TestCodexInstallIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(`{"model":"o3"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New()
	if err := a.Install(path, "/usr/local/bin/inscribe"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := a.Install(path, "/usr/local/bin/inscribe"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("reinstall changed the file:\n%s\n%s", first, second)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd"} {
		if !strings.Contains(string(first), `"`+ev+`"`) {
			t.Fatalf("event %s not installed", ev)
		}
	}
	if strings.Contains(string(first), "PreToolUse") {
		t.Fatal("Inscribe must not install a PreToolUse hook")
	}
	if !strings.Contains(string(first), `"model": "o3"`) {
		t.Fatalf("unrelated key lost: %s", first)
	}
	if err := a.Uninstall(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "inscribe") || strings.Contains(string(after), `"hooks"`) {
		t.Fatalf("uninstall left inscribe entries: %s", after)
	}
}

func TestCodexAdapterFacts(t *testing.T) {
	a := New()
	if !strings.HasSuffix(a.SettingsPath(), filepath.Join(".codex", "hooks.json")) {
		t.Fatalf("settings path = %s", a.SettingsPath())
	}
	if a.InstructionsFile() != "AGENTS.md" {
		t.Fatalf("instructions file = %s", a.InstructionsFile())
	}
	if spec := a.Headless("x"); spec.Command != nil {
		t.Fatalf("headless command = %v, want none", spec.Command)
	}
	if note := a.InstallNote(); !strings.Contains(note, "/hooks") {
		t.Fatalf("install note = %q", note)
	}
}

func TestCodexHomeRespected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	a := New()
	if got, want := a.SettingsPath(), filepath.Join(home, "hooks.json"); got != want {
		t.Fatalf("settings path = %s, want %s", got, want)
	}
	if got, want := a.SkillDirs(), []string{filepath.Join(home, "skills")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skill dirs = %v, want %v", got, want)
	}
}
