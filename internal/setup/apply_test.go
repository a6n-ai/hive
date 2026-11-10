package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/internal/config"
)

// fakeRunner records every command. It fails commands starting with failOn,
// and reports the hive MCP server as listed once it has been added.
type fakeRunner struct {
	calls  []string
	failOn string
	list   string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if f.failOn != "" && strings.HasPrefix(line, f.failOn) {
		return "", errors.New("exit status 1")
	}
	if strings.HasPrefix(line, "claude mcp add") {
		f.list = "hive"
	}
	if line == "claude mcp list" {
		return f.list, nil
	}
	return "", nil
}

func TestApplyRunsActionsInOrder(t *testing.T) {
	env, home := testEnv(t, nil, nil)
	settings := filepath.Join(home, ".claude", "settings.json")
	cfg := filepath.Join(home, ".hive", "config.toml")
	actions := []Action{
		{Kind: "install-hooks", Agent: "claude-code", Files: []string{settings}},
		{Kind: "disable-plugin", Command: []string{"claude", "plugin", "disable", "claude-mem@x"}},
		{Kind: "write-config", Files: []string{cfg}, Keys: map[string]any{"retention.mode": "keep_all"}},
		{Kind: "mcp-add", Command: []string{"claude", "mcp", "add", "hive", "--", "/opt/hive/bin/hive", "mcp"}},
	}
	f := &fakeRunner{}
	rep := Apply(context.Background(), env, actions, f.run)
	if len(rep.Failed) != 0 || len(rep.Done) != len(actions) {
		t.Fatalf("report done=%d failed=%+v, want all %d done", len(rep.Done), rep.Failed, len(actions))
	}
	for i, a := range rep.Done {
		if a.Kind != actions[i].Kind {
			t.Errorf("done[%d] = %s, want %s", i, a.Kind, actions[i].Kind)
		}
	}
	want := []string{"claude plugin disable claude-mem@x", "claude mcp list", "claude mcp add hive -- /opt/hive/bin/hive mcp"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	if !strings.Contains(readFile(t, settings), "hook --agent claude-code") {
		t.Error("install-hooks wrote no Hive hooks")
	}
	if !strings.Contains(readFile(t, cfg), `mode = "keep_all"`) {
		t.Error("write-config did not set retention.mode")
	}
}

func TestApplyContinuesAfterFailure(t *testing.T) {
	env, home := testEnv(t, nil, nil)
	cfg := filepath.Join(home, ".hive", "config.toml")
	actions := []Action{
		{Kind: "disable-plugin", Command: []string{"claude", "plugin", "disable", "claude-mem@x"}},
		{Kind: "write-config", Files: []string{cfg}, Keys: map[string]any{"consolidate.auto": true}},
		{Kind: "mcp-add", Command: []string{"claude", "mcp", "add", "hive", "--", "/opt/hive/bin/hive", "mcp"}},
	}
	f := &fakeRunner{failOn: "claude plugin disable"}
	rep := Apply(context.Background(), env, actions, f.run)
	if len(rep.Failed) != 1 || rep.Failed[0].Action.Kind != "disable-plugin" || rep.Failed[0].Err == nil {
		t.Fatalf("failed = %+v, want only disable-plugin with an error", rep.Failed)
	}
	if len(rep.Done) != 2 || rep.Done[0].Kind != "write-config" || rep.Done[1].Kind != "mcp-add" {
		t.Errorf("done = %+v, want write-config and mcp-add", rep.Done)
	}
	if !strings.Contains(readFile(t, cfg), "auto = true") {
		t.Error("write-config did not run after the failure")
	}
}

func TestApplyTwiceIsIdempotent(t *testing.T) {
	env, home := testEnv(t, nil, nil)
	settings := filepath.Join(home, ".claude", "settings.json")
	cfg := filepath.Join(home, ".hive", "config.toml")
	writeTestFile(t, settings, `{"enabledPlugins":{"claude-mem@x":true}}`)
	proj := filepath.Join(home, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../importer/testdata/projects/-tmp-proj/sess-a.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(proj, "sess-a.jsonl"), string(fixture))

	d := Detected{
		Agents:      []AgentInfo{{Name: "claude-code", Found: true, Settings: settings}},
		ClaudeMem:   true,
		ClaudeMemID: "claude-mem@x",
		ConfigPath:  cfg,
		HasHistory:  true,
		HiveBin:     env.HiveBin,
		Current:     config.Defaults(),
	}
	c := Choices{Agents: []string{"claude-code"}, DisableClaudeMem: true, ImportSince: "all", Retention: "rolling", MCP: true}
	plan := Plan(d, c)
	f := &fakeRunner{}
	Apply(context.Background(), env, plan, f.run)
	settingsOnce, cfgOnce := readFile(t, settings), readFile(t, cfgPathOrEmpty(t, cfg))
	events := countEvents(t)
	Apply(context.Background(), env, plan, f.run)

	if got := readFile(t, settings); got != settingsOnce {
		t.Errorf("settings changed on the second run:\n%s\n---\n%s", settingsOnce, got)
	}
	if got := readFile(t, cfg); got != cfgOnce {
		t.Errorf("config changed on the second run:\n%s", got)
	}
	if n := countEvents(t); n != events || n == 0 {
		t.Errorf("events after second run = %d, want %d (non-zero)", n, events)
	}
	if n := strings.Count(settingsOnce, "hook --agent claude-code"); n != 6 {
		t.Errorf("hive hooks = %d, want 6", n)
	}
	adds := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "claude mcp add") {
			adds++
		}
	}
	if adds != 1 {
		t.Errorf("mcp add ran %d times, want 1", adds)
	}
}

func cfgPathOrEmpty(t *testing.T, path string) string {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		writeTestFile(t, path, "")
	}
	return path
}

func countEvents(t *testing.T) int {
	t.Helper()
	h, err := hive.Open(hive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	evs, err := h.QueryEvents(context.Background(), hive.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
