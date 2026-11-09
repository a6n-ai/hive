package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
