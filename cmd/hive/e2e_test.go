package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/model"
	_ "modernc.org/sqlite"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hive-e2e-")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "hive")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// runCLI runs the built binary with HIVE_HOME set to home. extraEnv entries
// override the inherited environment.
func runCLI(t *testing.T, home, stdin string, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(append(os.Environ(), "HIVE_HOME="+home), extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

// runOK runs the CLI and fails the test unless it exits 0.
func runOK(t *testing.T, home, stdin string, args ...string) string {
	t.Helper()
	out, errOut, code := runCLI(t, home, stdin, nil, args...)
	if code != 0 {
		t.Fatalf("hive %v: exit %d: %s", args, code, errOut)
	}
	return out
}

// hookOK sends a hook payload and returns its stdout.
func hookOK(t *testing.T, home string, payload map[string]any, event string) string {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return runOK(t, home, string(b), "hook", "--agent", "claude-code", event)
}

func bashPayload(event, session, cwd, command, errText string) map[string]any {
	p := map[string]any{"session_id": session, "cwd": cwd, "hook_event_name": event, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command}}
	if errText != "" {
		p["error"] = errText
	}
	return p
}

func additionalContext(t *testing.T, out string) string {
	t.Helper()
	var reply struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatalf("reply %q: %v", out, err)
	}
	return reply.HookSpecificOutput.AdditionalContext
}

func TestHookMalformedInputExitsZero(t *testing.T) {
	home := t.TempDir()
	inputs := []string{"", "{", strings.Repeat("a", 9<<20), "\xff\xfe\xfd{"}
	for _, in := range inputs {
		out, _, code := runCLI(t, home, in, nil, "hook", "--agent", "claude-code", "UserPromptSubmit")
		if code != 0 || out != "" {
			t.Errorf("input %.20q: exit %d, stdout %q", in, code, out)
		}
	}
	logged, err := os.ReadFile(filepath.Join(home, "hive.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(logged), "\n"); n != len(inputs) {
		t.Fatalf("hive.log has %d lines, want %d", n, len(inputs))
	}
}

func TestHookDisabled(t *testing.T) {
	home := t.TempDir()
	out, _, code := runCLI(t, home, `{"session_id":"s"}`, []string{"HIVE_DISABLED=1"}, "hook", "--agent", "claude-code", "SessionStart")
	if code != 0 || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("HIVE_DISABLED created %d entries", len(entries))
	}
}

func TestCUJ2SessionStartRecall(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	for range 3 {
		hookOK(t, home, bashPayload("PostToolUse", "s1", proj, "go test ./...", ""), "PostToolUse")
	}
	ctx := additionalContext(t, hookOK(t, home, map[string]any{"session_id": "s1", "cwd": proj, "hook_event_name": "SessionStart"}, "SessionStart"))
	if !strings.Contains(ctx, "[command] tests: go test ./...") {
		t.Fatalf("additionalContext = %q", ctx)
	}
}

func TestCUJ3KnownError(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	file := filepath.Join(proj, "auth.ts")
	errAt := func(line int) string {
		return fmt.Sprintf("Exit code 1\nTypeError: cannot read properties of undefined (reading 'map') at %s:%d:5\n    at next", file, line)
	}
	hookOK(t, home, bashPayload("PostToolUseFailure", "s2", proj, "go test ./...", errAt(12)), "PostToolUseFailure")
	hookOK(t, home, map[string]any{"session_id": "s2", "cwd": proj, "hook_event_name": "PostToolUse", "tool_name": "Edit",
		"tool_input": map[string]any{"file_path": file, "old_string": "a", "new_string": "b"}}, "PostToolUse")
	hookOK(t, home, bashPayload("PostToolUse", "s2", proj, "go test ./...", ""), "PostToolUse")
	out := hookOK(t, home, bashPayload("PostToolUseFailure", "s3", proj, "go test ./...", errAt(40)), "PostToolUseFailure")
	if ctx := additionalContext(t, out); !strings.Contains(ctx, "auth.ts") {
		t.Fatalf("additionalContext = %q", ctx)
	}
}

func TestCUJ6SecretNeverStored(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	secret := "sk-ant-" + "api03-" + strings.Repeat("Q", 30)
	hookOK(t, home, map[string]any{"session_id": "s4", "cwd": proj, "hook_event_name": "UserPromptSubmit",
		"prompt": "export ANTHROPIC_API_KEY=" + secret}, "UserPromptSubmit")
	for _, name := range []string{"hive.db", "hive.db-wal"} {
		b, err := os.ReadFile(filepath.Join(home, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("%s holds the secret", name)
		}
	}
}

func TestRememberListWhyForget(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	id := strings.TrimSpace(runOK(t, home, "", "remember", "deploys go through make deploy", "--type", "note", "--scope", "project", "--project", proj))
	if id == "" {
		t.Fatal("remember printed no id")
	}
	if list := runOK(t, home, "", "list", "--project", proj); !strings.Contains(list, id) {
		t.Fatalf("list lacks %s: %q", id, list)
	}
	if why := runOK(t, home, "", "why", id); !strings.Contains(why, "[remembered]") {
		t.Fatalf("why = %q", why)
	}
	runOK(t, home, "", "forget", id, "--purge")
	if list := runOK(t, home, "", "list", "--project", proj); strings.Contains(list, id) {
		t.Fatalf("purged memory still listed: %q", list)
	}
}

func TestCUJ3ErrorWithSecretStillRecalls(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	file := filepath.Join(proj, "auth.ts")
	errAt := func(line int) string {
		return fmt.Sprintf("Exit code 1\nTypeError: bad config password=hunter2abc at %s:%d:5\n    at next", file, line)
	}
	hookOK(t, home, bashPayload("PostToolUseFailure", "s5", proj, "go test ./...", errAt(12)), "PostToolUseFailure")
	hookOK(t, home, map[string]any{"session_id": "s5", "cwd": proj, "hook_event_name": "PostToolUse", "tool_name": "Edit",
		"tool_input": map[string]any{"file_path": file, "old_string": "a", "new_string": "b"}}, "PostToolUse")
	hookOK(t, home, bashPayload("PostToolUse", "s5", proj, "go test ./...", ""), "PostToolUse")
	out := hookOK(t, home, bashPayload("PostToolUseFailure", "s6", proj, "go test ./...", errAt(40)), "PostToolUseFailure")
	if ctx := additionalContext(t, out); !strings.Contains(ctx, "auth.ts") {
		t.Fatalf("additionalContext = %q", ctx)
	}
}

func TestImportCommand(t *testing.T) {
	home := t.TempDir()
	root := "../../internal/importer/testdata/projects"
	out := runOK(t, home, "", "import", "claude-code", "--root", root)
	for _, want := range []string{"files=1", "skipped=3", "new_memories="} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	again := runOK(t, home, "", "import", "claude-code", "--root", root)
	if !strings.Contains(again, "new_events=0") {
		t.Errorf("second import output %q, want new_events=0", again)
	}
}

func TestRetentionApplyYes(t *testing.T) {
	home := t.TempDir()
	h, err := hive.Open(hive.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -400).UnixMilli()
	if err := h.Capture(context.Background(), hive.Event{TS: old, Agent: "claude-code", Session: "old", Project: "/p",
		Kind: model.Prompt, PromptText: "a prompt from long ago"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	out := runOK(t, home, "", "retention", "apply", "--yes")
	for _, want := range []string{"prompt_texts_cleared=1", "events_deleted=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

// fakeAgent is the headless test double; no test calls a real model.
func fakeAgent(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../internal/consolidate/testdata/fake-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// seedSession stores n prompts through the library, so tests start no hook processes.
func seedSession(t *testing.T, home, session string, n int) {
	t.Helper()
	h, err := hive.Open(hive.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		e := hive.Event{TS: time.Now().UnixMilli() + int64(i), Agent: "claude-code", Session: session, Project: "/p",
			Kind: model.Prompt, PromptText: fmt.Sprintf("prompt %d about deploys", i)}
		if err := h.Capture(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func runRows(t *testing.T, home string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM consolidation_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sessionEnd sends a SessionEnd hook for session, with the test agent in the environment.
func sessionEnd(t *testing.T, home, session, agentPath string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"session_id": session, "cwd": "/p", "hook_event_name": "SessionEnd"})
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := runCLI(t, home, string(payload), []string{"HIVE_TEST_HEADLESS=" + agentPath}, "hook", "--agent", "claude-code", "SessionEnd"); code != 0 {
		t.Fatalf("hook exit %d: %s", code, errOut)
	}
}

func fieldValue(out, key string) string {
	for _, f := range strings.Fields(out) {
		if v, ok := strings.CutPrefix(f, key); ok {
			return v
		}
	}
	return ""
}

func TestCUJ5ConsolidateAndUndo(t *testing.T) {
	home := t.TempDir()
	seedSession(t, home, "cuj5", 60)
	out, errOut, code := runCLI(t, home, "", []string{"HIVE_TEST_HEADLESS=" + fakeAgent(t), "FAKE_ADD=1"},
		"consolidate", "--session", "cuj5", "--agent", "claude-code")
	if code != 0 || !strings.Contains(out, "status=ok applied=1") {
		t.Fatalf("consolidate: exit %d, out %q, err %q", code, out, errOut)
	}
	runID := fieldValue(out, "run=")
	if !strings.Contains(runOK(t, home, "", "list"), "prefer small commits") {
		t.Fatal("consolidation did not add the decision")
	}
	if undo := runOK(t, home, "", "undo", runID); !strings.Contains(undo, "restored=1 skipped=0") {
		t.Fatalf("undo output %q", undo)
	}
	if strings.Contains(runOK(t, home, "", "list"), "prefer small commits") {
		t.Error("undo left the decision")
	}
}

func TestTwoConsolidationsOneRuns(t *testing.T) {
	home := t.TempDir()
	seedSession(t, home, "race", 25)
	env := append(os.Environ(), "HIVE_HOME="+home, "HIVE_TEST_HEADLESS="+fakeAgent(t), "FAKE_SLEEP=1")
	var outs [2]bytes.Buffer
	var cmds []*exec.Cmd
	for i := range outs {
		cmd := exec.Command(binPath, "consolidate", "--session", "race", "--agent", "claude-code")
		cmd.Env, cmd.Stdout = env, &outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	both := outs[0].String() + outs[1].String()
	if ok, locked := strings.Count(both, "status=ok"), strings.Count(both, "status=locked"); ok != 1 || locked != 1 {
		t.Errorf("status=ok %d, status=locked %d, want 1 and 1; output %q", ok, locked, both)
	}
}

func TestSessionEndSpawnsWhenAuto(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[consolidate]\nauto = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedSession(t, home, "end1", 25)
	sessionEnd(t, home, "end1", fakeAgent(t))
	deadline := time.Now().Add(5 * time.Second)
	for runRows(t, home) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no consolidation run within 5 s of the session end")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestSessionEndNoSpawnWhenAutoOff(t *testing.T) {
	home := t.TempDir()
	seedSession(t, home, "end2", 25)
	sessionEnd(t, home, "end2", fakeAgent(t))
	time.Sleep(1500 * time.Millisecond)
	if n := runRows(t, home); n != 0 {
		t.Errorf("runs = %d with consolidate.auto off, want 0", n)
	}
}

func TestInstallCodexAndGemini(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	codexFile := filepath.Join(dir, "codex-hooks.json")
	geminiFile := filepath.Join(dir, "gemini-settings.json")
	if err := os.WriteFile(geminiFile, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runOK(t, home, "", "install", "--agent", "codex", "--settings", codexFile); !strings.Contains(out, "approve the hive hooks") {
		t.Fatalf("codex install did not say to approve hooks: %q", out)
	}
	runOK(t, home, "", "install", "--agent", "gemini", "--settings", geminiFile)
	codexText := readText(t, codexFile)
	geminiText := readText(t, geminiFile)
	for _, want := range []string{"hook --agent codex SessionStart", "hook --agent codex Stop"} {
		if !strings.Contains(codexText, want) {
			t.Fatalf("codex hooks lack %q:\n%s", want, codexText)
		}
	}
	for _, want := range []string{`"theme": "dark"`, "hook --agent gemini AfterTool", `"matcher": "*"`} {
		if !strings.Contains(geminiText, want) {
			t.Fatalf("gemini settings lack %q:\n%s", want, geminiText)
		}
	}
	runOK(t, home, "", "uninstall", "--agent", "codex", "--settings", codexFile)
	if strings.Contains(readText(t, codexFile), "hook --agent codex") {
		t.Fatal("codex uninstall left hive hooks")
	}
}

func TestDoctorWarnsGeminiHooksDisabled(t *testing.T) {
	home, fakeHome := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(fakeHome, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooksConfig":{"enabled":false},"hooks":{"BeforeAgent":[{"matcher":"*","hooks":[{"name":"hive","type":"command","command":"/bin/hive hook --agent gemini BeforeAgent"}]}]}}`
	if err := os.WriteFile(filepath.Join(fakeHome, ".gemini", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runCLI(t, home, "", []string{"HOME=" + fakeHome}, "doctor")
	for _, want := range []string{"gemini: hooksConfig.enabled is false — hooks will not run", "gemini adapter: written from documentation, not verified against a live Gemini CLI"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestVersionDefault(t *testing.T) {
	out := runOK(t, t.TempDir(), "", "version")
	if strings.TrimSpace(out) != "hive dev (none, unknown)" {
		t.Fatalf("version = %q", out)
	}
}

func TestGeminiPromptAndToolRecall(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	runOK(t, home, "", "remember", "always run make lint before pushing", "--type", "note", "--scope", "global")
	gemini := func(event string, payload map[string]any) string {
		t.Helper()
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return runOK(t, home, string(b), "hook", "--agent", "gemini", event)
	}
	prompt := gemini("BeforeAgent", map[string]any{"session_id": "g1", "cwd": proj, "prompt": "always run make lint before pushing"})
	if ctx := additionalContext(t, prompt); !strings.Contains(ctx, "make lint") {
		t.Fatalf("BeforeAgent additionalContext = %q", ctx)
	}
	file := filepath.Join(proj, "auth.ts")
	shell := func(session, exit, text string) map[string]any {
		return map[string]any{"session_id": session, "cwd": proj, "tool_name": "run_shell_command",
			"tool_input": map[string]any{"command": "go test ./..."}, "tool_response": map[string]any{"output": text + "\nExit Code: " + exit}}
	}
	errAt := func(line int) string {
		return fmt.Sprintf("TypeError: cannot read properties of undefined (reading 'map') at %s:%d:5\n    at next", file, line)
	}
	gemini("AfterTool", shell("g2", "1", errAt(12)))
	gemini("AfterTool", map[string]any{"session_id": "g2", "cwd": proj, "tool_name": "replace",
		"tool_input": map[string]any{"file_path": file, "old_string": "a", "new_string": "b"}})
	gemini("AfterTool", shell("g2", "0", "ok"))
	out := gemini("AfterTool", shell("g3", "1", errAt(40)))
	if ctx := additionalContext(t, out); !strings.Contains(ctx, "auth.ts") {
		t.Fatalf("AfterTool additionalContext = %q", ctx)
	}
}

func TestDoctorToleratesJSONC(t *testing.T) {
	home, fakeHome := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(fakeHome, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := "// my theme\n{\"theme\": \"dark\" /* keep */}\n"
	if err := os.WriteFile(filepath.Join(fakeHome, ".gemini", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runCLI(t, home, "", []string{"HOME=" + fakeHome}, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s", code, errOut)
	}
	for _, want := range []string{"gemini: settings.json has comments; hive install cannot edit it safely", "database:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestInstallRefusesJSONCWithInstructions(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	file := filepath.Join(dir, "settings.json")
	orig := "// my theme\n{\"theme\": \"dark\"}\n"
	if err := os.WriteFile(file, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runCLI(t, home, "", nil, "install", "--agent", "gemini", "--settings", file)
	if code == 0 {
		t.Fatal("install accepted settings with comments")
	}
	if got := readText(t, file); got != orig {
		t.Fatalf("settings changed to %q", got)
	}
	for _, want := range []string{`hook --agent gemini SessionStart"`, `"matcher": "*"`} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("install output lacks %s:\n%s", want, errOut)
		}
	}
}
