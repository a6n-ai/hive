package gemini

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

func TestGeminiParse(t *testing.T) {
	cases := []struct {
		name, event, file string
		kind              model.EventKind
		tool              string
		payload           map[string]any
		prompt            string
	}{
		{name: "session start", event: "SessionStart", file: "SessionStart.json", kind: model.SessionStart},
		{name: "prompt", event: "BeforeAgent", file: "BeforeAgent.json", kind: model.Prompt, prompt: "fix the build"},
		{name: "prompt from llm_request", event: "BeforeAgent", file: "BeforeAgent_llm_request.json", kind: model.Prompt, prompt: "summarize the repo"},
		{name: "turn end", event: "AfterAgent", file: "AfterAgent.json", kind: model.TurnEnd},
		{name: "session end", event: "SessionEnd", file: "SessionEnd.json", kind: model.SessionEnd},
		{name: "read", event: "AfterTool", file: "AfterTool_read.json", kind: model.PostTool,
			tool: "read", payload: map[string]any{"path": "/tmp/a.go"}},
		{name: "shell exit code in text", event: "AfterTool", file: "AfterTool_shell_fail.json", kind: model.PostTool,
			tool: "shell", payload: map[string]any{"command": "go test", "exit_code": 1, "error": "FAIL"}},
		{name: "shell exit code in response", event: "AfterTool", file: "AfterTool_shell_key.json", kind: model.PostTool,
			tool: "shell", payload: map[string]any{"command": "go vet", "exit_code": 3, "error": "vet: bad"}},
		{name: "write", event: "AfterTool", file: "AfterTool_write.json", kind: model.PostTool,
			tool: "write", payload: map[string]any{"path": "/tmp/b.go"}},
		{name: "skill", event: "AfterTool", file: "AfterTool_skill.json", kind: model.PostTool,
			tool: "skill", payload: map[string]any{"skill": "tdd"}},
		{name: "unknown tool", event: "AfterTool", file: "AfterTool_unknown.json", kind: model.PostTool,
			tool: "google_web_search", payload: map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := New().Parse(c.event, fixture(t, c.file))
			if err != nil {
				t.Fatal(err)
			}
			if e.Agent != "gemini" || e.Session != "g1" || e.Kind != c.kind {
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

func TestGeminiParseRejects(t *testing.T) {
	if _, err := New().Parse("SessionStart", fixture(t, "SessionStart_nosession.json")); err == nil {
		t.Fatal("missing session_id accepted")
	}
	if _, err := New().Parse("PreToolUse", fixture(t, "AfterTool_read.json")); err == nil {
		t.Fatal("unknown event accepted")
	}
	if _, err := New().Parse("AfterTool", []byte("{not json")); err == nil {
		t.Fatal("malformed payload accepted")
	}
}

func TestGeminiReply(t *testing.T) {
	lines := []recall.Line{{Type: model.Preference, Text: "prefer rg over grep"}}
	for _, ev := range []string{"SessionStart", "BeforeAgent", "AfterTool"} {
		out := New().Reply(ev, lines)
		if !strings.Contains(string(out), `"hookEventName":"`+ev+`"`) || !strings.Contains(string(out), "prefer rg over grep") {
			t.Fatalf("%s reply = %s", ev, out)
		}
	}
	if New().Reply("AfterAgent", lines) != nil {
		t.Fatal("AfterAgent should reply with nothing")
	}
	if New().Reply("BeforeAgent", nil) != nil {
		t.Fatal("no lines should reply with nothing")
	}
}

func TestGeminiInstallKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"theme":"dark","mcpServers":{"x":{"command":"y"}},"hooks":{"AfterTool":[{"matcher":"write_file","hooks":[{"name":"other","type":"command","command":"other-tool"}]}]}}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New()
	if err := a.Install(path, "/usr/local/bin/inscribe"); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(path)
	for _, want := range []string{`"theme": "dark"`, `"mcpServers"`, `"other-tool"`, `"matcher": "*"`, `"name": "inscribe"`} {
		if !strings.Contains(string(installed), want) {
			t.Fatalf("missing %s in:\n%s", want, installed)
		}
	}
	for _, ev := range []string{"SessionStart", "BeforeAgent", "AfterTool", "AfterAgent", "SessionEnd"} {
		if !strings.Contains(string(installed), `"`+ev+`"`) {
			t.Fatalf("event %s not installed", ev)
		}
	}
	if err := a.Uninstall(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "inscribe") || !strings.Contains(string(after), "other-tool") || !strings.Contains(string(after), `"theme"`) {
		t.Fatalf("uninstall did not restore other settings: %s", after)
	}
}

func TestGeminiAdapterFacts(t *testing.T) {
	a := New()
	if !strings.HasSuffix(a.SettingsPath(), filepath.Join(".gemini", "settings.json")) {
		t.Fatalf("settings path = %s", a.SettingsPath())
	}
	if a.InstructionsFile() != "GEMINI.md" {
		t.Fatalf("instructions file = %s", a.InstructionsFile())
	}
	caps := a.Capabilities()
	if !caps.CanInject || caps.CanBlock || caps.HasPreTool || !caps.HasSessionEnd {
		t.Fatalf("caps = %+v", caps)
	}
	if spec := a.Headless("x"); spec.Command != nil {
		t.Fatalf("headless command = %v, want none", spec.Command)
	}
}

func TestGeminiExitCodeAnywhere(t *testing.T) {
	const text = "Command: false\nDirectory: (root)\nExit Code: 1\nboom"
	payloads := map[string]map[string]any{
		"string response":          {"tool_response": text},
		"tool_response llmContent": {"tool_response": map[string]any{"llmContent": text}},
		"tool_result llmContent":   {"tool_result": map[string]any{"llmContent": text}},
	}
	for name, p := range payloads {
		t.Run(name, func(t *testing.T) {
			p["session_id"], p["tool_name"], p["tool_input"] = "g1", "run_shell_command", map[string]any{"command": "false"}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			e, err := New().Parse("AfterTool", b)
			if err != nil {
				t.Fatal(err)
			}
			if e.Payload["exit_code"] != 1 || e.Payload["error"] != "Command: false\nDirectory: (root)" {
				t.Fatalf("payload = %#v", e.Payload)
			}
		})
	}
}

func TestGeminiExitCodeNotSpoofable(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		code    int
		errText string
	}{
		{"last text marker wins", map[string]any{"tool_response": "Command: x\nExit Code: 0\nstuff\nExit Code: 1\nboom"}, 1, "Command: x\nExit Code: 0\nstuff"},
		{"structured beats text", map[string]any{"tool_response": map[string]any{"exit_code": 0, "output": "Exit Code: 1\nboom"}}, 0, ""},
		{"structured in tool_result beats text", map[string]any{"tool_response": "Exit Code: 1\nboom", "tool_result": map[string]any{"exitCode": 0}}, 0, ""},
		{"structured error text", map[string]any{"tool_response": map[string]any{"returncode": 3, "output": "vet: bad"}}, 3, "vet: bad"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.payload["session_id"], c.payload["tool_name"], c.payload["tool_input"] = "g1", "run_shell_command", map[string]any{"command": "x"}
			b, err := json.Marshal(c.payload)
			if err != nil {
				t.Fatal(err)
			}
			e, err := New().Parse("AfterTool", b)
			if err != nil {
				t.Fatal(err)
			}
			if e.Payload["exit_code"] != c.code {
				t.Fatalf("exit_code = %v, want %d (payload %#v)", e.Payload["exit_code"], c.code, e.Payload)
			}
			if got, has := e.Payload["error"]; (c.code != 0) != has || (has && got != c.errText) {
				t.Fatalf("error = %#v, want %q", got, c.errText)
			}
		})
	}
}
