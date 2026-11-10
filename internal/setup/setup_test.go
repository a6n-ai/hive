package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// testEnv returns an Env whose files and PATH are fakes, with HOME and
// HIVE_HOME under a temp dir so nothing touches the real home.
func testEnv(t *testing.T, onPath []string, files map[string]string) (Env, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_HOME", filepath.Join(home, ".hive"))
	t.Setenv("CODEX_HOME", "")
	byPath := map[string][]byte{}
	for rel, body := range files {
		byPath[filepath.Join(home, rel)] = []byte(body)
	}
	return Env{
		Home:    home,
		HiveBin: "/opt/hive/bin/hive",
		LookPath: func(name string) (string, error) {
			if slices.Contains(onPath, name) {
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		ReadFile: func(p string) ([]byte, error) {
			if b, ok := byPath[p]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
	}, home
}

func TestDetectFindsAgentsAndClaudeMem(t *testing.T) {
	env, home := testEnv(t, []string{"claude", "gemini"}, map[string]string{
		".claude/settings.json": `{"enabledPlugins":{"claude-mem@thedotmack":true,"other@x":true},` +
			`"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/hive hook --agent claude-code Stop"}]}]}}`,
		".gemini/settings.json": `{}`,
	})
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects", "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "projects", "p", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Detect(env)
	if err != nil {
		t.Fatal(err)
	}
	want := []AgentInfo{
		{Name: "claude-code", Found: true, Installed: true, Verified: true, Settings: filepath.Join(home, ".claude", "settings.json")},
		{Name: "codex", Found: false, Installed: false, Verified: true, Settings: filepath.Join(home, ".codex", "hooks.json")},
		{Name: "gemini", Found: true, Installed: false, Verified: false, Settings: filepath.Join(home, ".gemini", "settings.json")},
	}
	if !reflect.DeepEqual(d.Agents, want) {
		t.Errorf("agents = %+v, want %+v", d.Agents, want)
	}
	if !d.ClaudeMem || d.ClaudeMemID != "claude-mem@thedotmack" {
		t.Errorf("claude-mem = %v %q, want true claude-mem@thedotmack", d.ClaudeMem, d.ClaudeMemID)
	}
	if !d.HasHistory {
		t.Error("HasHistory = false, want true")
	}
	if d.ConfigPath != filepath.Join(home, ".hive", "config.toml") {
		t.Errorf("ConfigPath = %q", d.ConfigPath)
	}
	if d.HiveBin != env.HiveBin {
		t.Errorf("HiveBin = %q", d.HiveBin)
	}
}

func TestDefaultsFollowDetection(t *testing.T) {
	d := Detected{
		Agents: []AgentInfo{
			{Name: "claude-code", Found: true},
			{Name: "codex", Found: false},
			{Name: "gemini", Found: true},
		},
		ClaudeMem: true,
	}
	c := Defaults(d)
	want := Choices{Agents: []string{"claude-code", "gemini"}, DisableClaudeMem: true, ImportSince: "", Retention: "rolling"}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("defaults without history = %+v, want %+v", c, want)
	}
	d.HasHistory, d.ClaudeMem = true, false
	c = Defaults(d)
	if c.ImportSince != "30d" || c.DisableClaudeMem {
		t.Errorf("defaults with history = %+v, want ImportSince 30d and DisableClaudeMem false", c)
	}
}

func TestPlanOrderAndContents(t *testing.T) {
	d := Detected{
		Agents: []AgentInfo{
			{Name: "claude-code", Found: true, Settings: "/h/.claude/settings.json"},
			{Name: "gemini", Found: true, Settings: "/h/.gemini/settings.json"},
		},
		ClaudeMem: true, ClaudeMemID: "claude-mem@thedotmack", ConfigPath: "/h/.hive/config.toml",
		HasHistory: true, HiveBin: "/bin/hive",
	}
	c := Choices{Agents: []string{"claude-code"}, DisableClaudeMem: true, ImportSince: "90d", Retention: "keep_all", Consolidate: true, MCP: true}
	plan := Plan(d, c)
	var kinds []string
	for _, a := range plan {
		kinds = append(kinds, a.Kind)
	}
	wantKinds := []string{"install-hooks", "disable-plugin", "import", "write-config", "mcp-add"}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
	}
	if !reflect.DeepEqual(plan[0].Files, []string{"/h/.claude/settings.json"}) {
		t.Errorf("install-hooks files = %v", plan[0].Files)
	}
	if !reflect.DeepEqual(plan[1].Command, []string{"claude", "plugin", "disable", "claude-mem@thedotmack"}) {
		t.Errorf("disable command = %v", plan[1].Command)
	}
	if !reflect.DeepEqual(plan[3].Keys, map[string]any{"retention.mode": "keep_all", "consolidate.auto": true}) {
		t.Errorf("write-config keys = %v", plan[3].Keys)
	}
	if !reflect.DeepEqual(plan[4].Command, []string{"claude", "mcp", "add", "hive", "--", "/bin/hive", "mcp"}) {
		t.Errorf("mcp command = %v", plan[4].Command)
	}
	for _, a := range plan {
		if a.Desc == "" {
			t.Errorf("action %s has no description", a.Kind)
		}
	}
}

func TestWarningsRollingImport(t *testing.T) {
	d := Detected{}
	got := Warnings(d, Choices{Retention: "rolling", ImportSince: "all"})
	if len(got) != 1 || !strings.Contains(got[0], "pruned at the next session start") {
		t.Errorf("rolling + all warnings = %q", got)
	}
	if w := Warnings(d, Choices{Retention: "rolling", ImportSince: "90d"}); len(w) != 0 {
		t.Errorf("rolling + 90d warnings = %q, want none", w)
	}
	if w := Warnings(d, Choices{Retention: "keep_all", ImportSince: "all"}); len(w) != 0 {
		t.Errorf("keep_all + all warnings = %q, want none", w)
	}
}

func TestSetConfigKeysKeepsCommentsAndOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := "# my hive settings\n[recall]\n# keep my budget\nsession_budget = 100\n\n[retention]\n# how long\nmode = \"rolling\"\nevent_days = 90\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigKeys(path, map[string]any{"retention.mode": "keep_all", "consolidate.auto": true}); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	for _, want := range []string{"# my hive settings", "# keep my budget", "session_budget = 100", "# how long",
		`mode = "keep_all"`, "event_days = 90", "[consolidate]", "auto = true"} {
		if !strings.Contains(out, want) {
			t.Errorf("config lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"rolling"`) {
		t.Errorf("old value kept:\n%s", out)
	}
}

func TestSetConfigKeysIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	kv := map[string]any{"retention.mode": "storage_time", "consolidate.auto": false}
	if err := SetConfigKeys(path, kv); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	if err := SetConfigKeys(path, kv); err != nil {
		t.Fatal(err)
	}
	if second := readFile(t, path); second != first {
		t.Errorf("second write changed the file:\n%s\n---\n%s", first, second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
