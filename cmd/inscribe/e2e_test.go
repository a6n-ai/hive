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

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/consolidate"
	"github.com/a6n-ai/inscribe/model"
	_ "modernc.org/sqlite"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "inscribe-e2e-")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "inscribe")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// runCLI runs the built binary with INSCRIBE_HOME set to home. extraEnv entries
// override the inherited environment; background spawns are off unless extraEnv
// sets INSCRIBE_TEST_NO_SPAWN=0.
func runCLI(t *testing.T, home, stdin string, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(append(os.Environ(), "INSCRIBE_HOME="+home, "INSCRIBE_TEST_NO_SPAWN=1"), extraEnv...)
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
		t.Fatalf("inscribe %v: exit %d: %s", args, code, errOut)
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
	logged, err := os.ReadFile(filepath.Join(home, "inscribe.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(logged), "\n"); n != len(inputs) {
		t.Fatalf("inscribe.log has %d lines, want %d", n, len(inputs))
	}
}

func TestHookDisabled(t *testing.T) {
	home := t.TempDir()
	out, _, code := runCLI(t, home, `{"session_id":"s"}`, []string{"INSCRIBE_DISABLED=1"}, "hook", "--agent", "claude-code", "SessionStart")
	if code != 0 || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("INSCRIBE_DISABLED created %d entries", len(entries))
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
	for _, name := range []string{"inscribe.db", "inscribe.db-wal"} {
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
	h, err := inscribe.Open(inscribe.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -400).UnixMilli()
	if err := h.Capture(context.Background(), inscribe.Event{TS: old, Agent: "claude-code", Session: "old", Project: "/p",
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
	h, err := inscribe.Open(inscribe.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		e := inscribe.Event{TS: time.Now().UnixMilli() + int64(i), Agent: "claude-code", Session: session, Project: "/p",
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
	db, err := sql.Open("sqlite", filepath.Join(home, "inscribe.db"))
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
// It allows the background consolidation spawn; callers must wait with waitHomeQuiet.
func sessionEnd(t *testing.T, home, session, agentPath string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"session_id": session, "cwd": "/p", "hook_event_name": "SessionEnd"})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"INSCRIBE_TEST_HEADLESS=" + agentPath, "INSCRIBE_TEST_NO_SPAWN=0"}
	if _, errOut, code := runCLI(t, home, string(payload), env, "hook", "--agent", "claude-code", "SessionEnd"); code != 0 {
		t.Fatalf("hook exit %d: %s", code, errOut)
	}
}

// waitHomeQuiet blocks until no consolidation holds home's lock, so a spawned
// child has stopped writing into home before the test returns.
func waitHomeQuiet(t *testing.T, home string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		release, ok, err := consolidate.Lock(home)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("consolidation still holds the lock 10 s after its run was recorded")
		}
		time.Sleep(50 * time.Millisecond)
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
	out, errOut, code := runCLI(t, home, "", []string{"INSCRIBE_TEST_HEADLESS=" + fakeAgent(t), "FAKE_ADD=1"},
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
	env := append(os.Environ(), "INSCRIBE_HOME="+home, "INSCRIBE_TEST_HEADLESS="+fakeAgent(t), "FAKE_SLEEP=1")
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
	waitHomeQuiet(t, home)
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
	if out := runOK(t, home, "", "install", "--agent", "codex", "--settings", codexFile); !strings.Contains(out, "approve the inscribe hooks") {
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
		t.Fatal("codex uninstall left inscribe hooks")
	}
}

func TestDoctorWarnsGeminiHooksDisabled(t *testing.T) {
	home, fakeHome := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(fakeHome, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooksConfig":{"enabled":false},"hooks":{"BeforeAgent":[{"matcher":"*","hooks":[{"name":"inscribe","type":"command","command":"/bin/inscribe hook --agent gemini BeforeAgent"}]}]}}`
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
	if strings.TrimSpace(out) != "inscribe dev (none, unknown)" {
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
	for _, want := range []string{"gemini: settings.json has comments; inscribe install cannot edit it safely", "database:"} {
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

func TestNoConsolidateSpawnWithoutHeadless(t *testing.T) {
	var spawned [][]string
	old := spawnDetached
	spawnDetached = func(args ...string) error {
		spawned = append(spawned, args)
		return nil
	}
	t.Cleanup(func() { spawnDetached = old })
	for _, ev := range []string{"SessionStart", "SessionEnd"} {
		if err := followUps(ev, inscribe.Event{Agent: "gemini", Session: "s1"}, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range spawned {
		if args[0] == "consolidate" {
			t.Fatalf("gemini spawned consolidate %v", args)
		}
	}
	spawned = nil
	if err := followUps("SessionEnd", inscribe.Event{Agent: "claude-code", Session: "s1"}, true); err != nil {
		t.Fatal(err)
	}
	if len(spawned) != 1 || spawned[0][0] != "consolidate" {
		t.Fatalf("claude-code did not spawn consolidate: %v", spawned)
	}
}

// fakeClaudeEnv puts a fake claude first on PATH; it logs its arguments and runs nothing real.
func fakeClaudeEnv(t *testing.T, home string) (extraEnv []string, logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "claude.log")
	script := "#!/bin/sh\necho \"$@\" >> \"$FAKE_CLAUDE_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: test fake must be executable
		t.Fatal(err)
	}
	return []string{"HOME=" + home, "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "FAKE_CLAUDE_LOG=" + logPath}, logPath
}

func TestSetupYesAppliesDefaults(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	out, errOut, code := runCLI(t, home, "", env, "setup", "--yes")
	if code != 0 {
		t.Fatalf("setup --yes: exit %d\n%s\n%s", code, out, errOut)
	}
	settings := readText(t, filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(settings, "hook --agent claude-code SessionStart") {
		t.Fatalf("claude settings lack Inscribe hooks:\n%s", settings)
	}
	if !strings.Contains(out, "database:") {
		t.Errorf("output lacks the doctor report:\n%s", out)
	}
}

func TestSetupNoTTYWithoutYesExits2(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	out, errOut, code := runCLI(t, home, "", env, "setup")
	if code != 2 {
		t.Fatalf("exit %d, want 2\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "inscribe setup --yes") {
		t.Errorf("output %q does not ask for --yes", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Error("setup without --yes changed the settings file")
	}
}

func TestUninstallAllRemovesHooksAndMCP(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".inscribe")
	env, logPath := fakeClaudeEnv(t, home)
	env = append(env, "INSCRIBE_HOME="+data)
	if _, errOut, code := runCLI(t, data, "", env, "setup", "--yes"); code != 0 {
		t.Fatalf("setup --yes: exit %d: %s", code, errOut)
	}
	runOK(t, data, "", "remember", "keep me")

	out, errOut, code := runCLI(t, data, "", env, "uninstall")
	if code != 0 {
		t.Fatalf("uninstall: exit %d\n%s\n%s", code, out, errOut)
	}
	if s := readText(t, filepath.Join(home, ".claude", "settings.json")); strings.Contains(s, "hook --agent claude-code") {
		t.Fatalf("uninstall left inscribe hooks:\n%s", s)
	}
	if log := readText(t, logPath); !strings.Contains(log, "mcp remove --scope user inscribe") {
		t.Errorf("uninstall did not remove the MCP server; claude calls:\n%s", log)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatalf("uninstall without --purge removed the data dir: %v", err)
	}

	if out, errOut, code := runCLI(t, data, "", env, "uninstall", "--purge"); code != 0 {
		t.Fatalf("uninstall --purge: exit %d\n%s\n%s", code, out, errOut)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("uninstall --purge kept the data dir: %v", err)
	}
}

func TestUninstallPurgeRefusesHomeDir(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	if err := os.WriteFile(filepath.Join(home, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runCLI(t, home, "", env, "uninstall", "--purge")
	if code == 0 {
		t.Fatal("uninstall --purge with INSCRIBE_HOME = HOME exited 0")
	}
	if !strings.Contains(errOut, "refusing") {
		t.Errorf("stderr %q does not explain the refusal", errOut)
	}
	if _, err := os.Stat(filepath.Join(home, "keep.txt")); err != nil {
		t.Fatalf("purge deleted a file in HOME: %v", err)
	}
}

func TestSetupYesTwiceIdempotent(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	if _, errOut, code := runCLI(t, home, "", env, "setup", "--yes"); code != 0 {
		t.Fatalf("setup --yes: exit %d: %s", code, errOut)
	}
	first := readText(t, filepath.Join(home, ".claude", "settings.json"))
	if _, errOut, code := runCLI(t, home, "", env, "setup", "--yes"); code != 0 {
		t.Fatalf("setup --yes: exit %d: %s", code, errOut)
	}
	second := readText(t, filepath.Join(home, ".claude", "settings.json"))
	if first != second {
		t.Errorf("second setup changed settings:\n%s\n---\n%s", first, second)
	}
	if n := strings.Count(second, "hook --agent claude-code"); n != 6 {
		t.Errorf("inscribe hooks = %d, want 6", n)
	}
}

func TestSetupYesDoesNotResetUserConfig(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[retention]\nmode = \"keep_all\"\n\n[consolidate]\nauto = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, errOut, code := runCLI(t, home, "", env, "setup", "--yes"); code != 0 {
			t.Fatalf("setup --yes: exit %d: %s", code, errOut)
		}
	}
	text := readText(t, cfg)
	for _, want := range []string{`mode = "keep_all"`, "auto = true"} {
		if !strings.Contains(text, want) {
			t.Errorf("config lost %q after setup --yes:\n%s", want, text)
		}
	}
}

func TestSetupDevNullStdinExits2(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	cmd := exec.Command(binPath, "setup")
	cmd.Env = append(append(os.Environ(), "INSCRIBE_HOME="+home), env...)
	cmd.Stdin = devNull
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("setup with /dev/null stdin: err %v, want exit 2\n%s\n%s", err, out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "TTY") {
		t.Errorf("setup tried to open a terminal:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), "- ") || !strings.Contains(out.String(), "inscribe setup --yes") {
		t.Errorf("output lacks the plan or the --yes hint:\n%s", out.String())
	}
}

func TestSetupInvalidConfigExits1NamesFile(t *testing.T) {
	home := t.TempDir()
	env, _ := fakeClaudeEnv(t, home)
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[retention\nmode = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runCLI(t, home, "", env, "setup", "--yes")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, cfg) || !strings.Contains(errOut, "inscribe setup: cannot read") {
		t.Errorf("stderr %q does not name %s and the setup failure", errOut, cfg)
	}
}
