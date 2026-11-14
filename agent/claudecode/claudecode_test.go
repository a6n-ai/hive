package claudecode

import (
	"encoding/json"
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

func TestParseGolden(t *testing.T) {
	a := New()
	cases := []struct {
		file, event string
		kind        model.EventKind
		tool        string
		payload     map[string]any
		prompt      string
	}{
		{"SessionStart.json", "SessionStart", model.SessionStart, "", map[string]any{}, ""},
		{"UserPromptSubmit.json", "UserPromptSubmit", model.Prompt, "", map[string]any{}, "fix the failing login test"},
		{"PostToolUse_Bash.json", "PostToolUse", model.PostTool, "shell", map[string]any{"command": "go test ./...", "exit_code": 0}, ""},
		{"PostToolUseFailure_Bash.json", "PostToolUseFailure", model.PostTool, "shell", map[string]any{"command": "go test ./...", "exit_code": 1,
			"error": "TypeError: cannot read properties of undefined (reading 'map') at /tmp/inscribe-fixture-proj/auth.ts:12:5\n    at next"}, ""},
		{"PostToolUse_Edit.json", "PostToolUse", model.PostTool, "edit", map[string]any{"path": "/tmp/inscribe-fixture-proj/auth.ts"}, ""},
		{"PostToolUse_Skill.json", "PostToolUse", model.PostTool, "skill", map[string]any{"skill": "superpowers:brainstorming"}, ""},
		{"Stop.json", "Stop", model.TurnEnd, "", map[string]any{}, ""},
		{"SessionEnd.json", "SessionEnd", model.SessionEnd, "", map[string]any{}, ""},
	}
	for _, c := range cases {
		e, err := a.Parse(c.event, fixture(t, c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if e.Kind != c.kind || e.Tool != c.tool || e.Session != "sess-1" || e.Agent != "claude-code" || e.PromptText != c.prompt {
			t.Errorf("%s: got kind=%s tool=%s session=%s agent=%s prompt=%q", c.file, e.Kind, e.Tool, e.Session, e.Agent, e.PromptText)
		}
		if e.Project == "" {
			t.Errorf("%s: empty project", c.file)
		}
		if want := map[string]string{"SessionStart.json": "/t.jsonl", "UserPromptSubmit.json": "/t.jsonl"}[c.file]; want != "" && e.TranscriptPath != want {
			t.Errorf("%s: transcript path = %q, want %q", c.file, e.TranscriptPath, want)
		}
		if !reflect.DeepEqual(e.Payload, c.payload) {
			t.Errorf("%s: payload = %v, want %v", c.file, e.Payload, c.payload)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	a := New()
	for _, in := range []string{"", "{", "[]", `{"session_id": 3}`} {
		if _, err := a.Parse("UserPromptSubmit", []byte(in)); err == nil {
			t.Errorf("Parse(%q) accepted", in)
		}
	}
	if _, err := a.Parse("NoSuchEvent", fixture(t, "Stop.json")); err == nil {
		t.Error("unknown event accepted")
	}
}

func TestReplyGolden(t *testing.T) {
	a := New()
	lines := []recall.Line{{Type: model.Command, Text: "tests: go test ./..."}, {Type: model.Preference, Text: "use rg not grep"}}
	if got, want := string(a.Reply("UserPromptSubmit", lines)), string(fixture(t, "reply_prompt.golden")); got != want {
		t.Fatalf("reply:\n got  %s\n want %s", got, want)
	}
	if got, want := string(a.Reply("UserPromptSubmit", nil)), `{"systemMessage":"inscribe: recalled 0 memories for this prompt"}`; got != want {
		t.Fatalf("empty prompt reply = %s, want %s", got, want)
	}
	if got := a.Reply("PostToolUseFailure", nil); len(got) != 0 {
		t.Fatalf("empty error reply = %q, want nothing", got)
	}
	if got := a.Reply("Stop", lines); len(got) != 0 {
		t.Fatalf("Stop reply = %q, want nothing", got)
	}
	want := `{"systemMessage":"inscribe: loaded 0 memories, 0 recent sessions"}`
	if got := string(a.Reply("SessionStart", nil)); got != want {
		t.Fatalf("empty SessionStart reply = %s, want %s", got, want)
	}
}

const bin = "/usr/local/bin/inscribe"

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func inscribeEntries(m map[string]any, event string) int {
	hooks, _ := m["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	n := 0
	for _, g := range groups {
		for _, h := range g.(map[string]any)["hooks"].([]any) {
			if strings.HasPrefix(h.(map[string]any)["command"].(string), bin+" hook --agent claude-code") {
				n++
			}
		}
	}
	return n
}

func TestInstallIdempotentKeepsOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"model":"opus","hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"other-tool check"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New()
	for i := 0; i < 2; i++ {
		if err := a.Install(path, bin); err != nil {
			t.Fatal(err)
		}
	}
	m := readJSON(t, path)
	for _, ev := range installEvents {
		if n := inscribeEntries(m, ev); n != 1 {
			t.Errorf("%s: %d inscribe entries, want 1", ev, n)
		}
	}
	if !strings.Contains(string(mustRead(t, path)), "other-tool check") || m["model"] != "opus" {
		t.Fatal("unrelated settings lost")
	}
	if err := a.Uninstall(path); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(original), &want)
	if got := readJSON(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("after uninstall:\n got  %v\n want %v", got, want)
	}
	if _, err := os.Stat(path + ".inscribe-backup"); err != nil {
		t.Fatalf("no backup written: %v", err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallMissingFileCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	if err := New().Install(path, bin); err != nil {
		t.Fatal(err)
	}
	if n := inscribeEntries(readJSON(t, path), "SessionStart"); n != 1 {
		t.Fatalf("SessionStart entries = %d", n)
	}
}

func TestInstallInvalidJSONRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	bad := []byte(`{"hooks": [oops`)
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	err := New().Install(path, bin)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want one naming %s", err, path)
	}
	if got := mustRead(t, path); string(got) != string(bad) {
		t.Fatal("invalid settings file was modified")
	}
}

func TestInstallFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := New().Install(link, bin); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings.json is no longer a symlink: %v", err)
	}
	if n := inscribeEntries(readJSON(t, target), "SessionStart"); n != 1 {
		t.Fatalf("target has %d inscribe entries, want 1", n)
	}
}

func TestInstallKeepsOtherCommandsVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"count":12345678901234567890,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo a 2>&1 > /tmp/x && true"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"install", "uninstall"} {
		var err error
		if step == "install" {
			err = New().Install(path, bin)
		} else {
			err = New().Uninstall(path)
		}
		if err != nil {
			t.Fatal(err)
		}
		raw := string(mustRead(t, path))
		for _, want := range []string{"12345678901234567890", "echo a 2>&1 > /tmp/x && true"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("after %s, %q is not byte-for-byte in:\n%s", step, want, raw)
			}
		}
	}
}

func TestInstallQuotesBinaryWithSpace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := New().Install(path, "/Applications/My Tools/inscribe"); err != nil {
		t.Fatal(err)
	}
	cmd := readJSON(t, path)["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if want := "'/Applications/My Tools/inscribe' hook --agent claude-code"; !strings.HasPrefix(cmd, want) {
		t.Fatalf("command = %q, want prefix %q", cmd, want)
	}
}

func TestReinstallFromNewPathReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := New().Install(path, "/a/inscribe"); err != nil {
		t.Fatal(err)
	}
	if err := New().Install(path, "/b/inscribe"); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	for _, ev := range installEvents {
		groups := m["hooks"].(map[string]any)[ev].([]any)
		var cmds []string
		for _, g := range groups {
			for _, h := range g.(map[string]any)["hooks"].([]any) {
				if c := h.(map[string]any)["command"].(string); strings.Contains(c, " hook --agent claude-code ") {
					cmds = append(cmds, c)
				}
			}
		}
		if len(cmds) != 1 || cmds[0] != "/b/inscribe hook --agent claude-code "+ev {
			t.Errorf("%s: inscribe commands = %v, want exactly /b/inscribe", ev, cmds)
		}
	}
}

func TestStatusSessionStartWithSummary(t *testing.T) {
	sum := "last 7d: 4/9 prompt lines hit, 1.2k tok injected, 3 next-try fixes"
	lines := []recall.Line{{Type: "command", MemoryID: "m1"}, {Type: "recent"}, {Type: "recent"}, {Type: "inscribe", Text: sum}}
	want := "inscribe: loaded 1 memories, 2 recent sessions · " + sum
	if got := status("SessionStart", lines); got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestStatusHoldout(t *testing.T) {
	lines := []recall.Line{{Type: recall.HoldoutType}}
	want := "inscribe: holdout prompt, 0 memories injected"
	for _, ev := range []string{"UserPromptSubmit", "SessionStart"} {
		if got := status(ev, lines); got != want {
			t.Fatalf("%s: status = %q, want %q", ev, got, want)
		}
	}
}
