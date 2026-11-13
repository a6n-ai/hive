package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/a6n-ai/inscribe/internal/config"
)

// testEnv returns an Env whose files and PATH are fakes, with HOME and
// INSCRIBE_HOME under a temp dir so nothing touches the real home.
func testEnv(t *testing.T, onPath []string, files map[string]string) (Env, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("INSCRIBE_HOME", filepath.Join(home, ".inscribe"))
	t.Setenv("CODEX_HOME", "")
	byPath := map[string][]byte{}
	for rel, body := range files {
		byPath[filepath.Join(home, rel)] = []byte(body)
	}
	return Env{
		Home:        home,
		InscribeBin: "/opt/inscribe/bin/inscribe",
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
			`"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/inscribe hook --agent claude-code Stop"}]}]}}`,
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
		{Name: "claude-code", Found: true, OnPath: true, Installed: true, Verified: true, Settings: filepath.Join(home, ".claude", "settings.json")},
		{Name: "codex", Found: false, Installed: false, Verified: true, Settings: filepath.Join(home, ".codex", "hooks.json")},
		{Name: "gemini", Found: true, OnPath: true, Installed: false, Verified: false, Settings: filepath.Join(home, ".gemini", "settings.json")},
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
	if d.ConfigPath != filepath.Join(home, ".inscribe", "config.toml") {
		t.Errorf("ConfigPath = %q", d.ConfigPath)
	}
	if d.InscribeBin != env.InscribeBin {
		t.Errorf("InscribeBin = %q", d.InscribeBin)
	}
}

func TestDefaultsFollowDetection(t *testing.T) {
	d := Detected{
		Agents: []AgentInfo{
			{Name: "claude-code", Found: true, OnPath: true},
			{Name: "codex", Found: false},
			{Name: "gemini", Found: true, OnPath: true},
		},
		ClaudeMem: true,
		Current:   config.Defaults(),
	}
	c := Defaults(d)
	want := Choices{Agents: []string{"claude-code", "gemini"}, DisableClaudeMem: true, ImportSince: "", Retention: "rolling", Consolidate: true}
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
		ClaudeMem: true, ClaudeMemID: "claude-mem@thedotmack", ConfigPath: "/h/.inscribe/config.toml",
		HasHistory: true, InscribeBin: "/bin/inscribe",
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
	if !reflect.DeepEqual(plan[4].Command, []string{"claude", "mcp", "add", "--scope", "user", "inscribe", "--", "/bin/inscribe", "mcp"}) {
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
	orig := "# my inscribe settings\n[recall]\n# keep my budget\nsession_budget = 100\n\n[retention]\n# how long\nmode = \"rolling\"\nevent_days = 90\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigKeys(path, map[string]any{"retention.mode": "keep_all", "consolidate.auto": true}); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	for _, want := range []string{"# my inscribe settings", "# keep my budget", "session_budget = 100", "# how long",
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

func TestDefaultsKeepExistingConfig(t *testing.T) {
	env, home := testEnv(t, nil, nil)
	if err := os.MkdirAll(filepath.Join(home, ".inscribe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".inscribe", "config.toml"),
		[]byte("[retention]\nmode = \"keep_all\"\n\n[consolidate]\nauto = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Detect(env)
	if err != nil {
		t.Fatal(err)
	}
	c := Defaults(d)
	if c.Retention != "keep_all" || !c.Consolidate {
		t.Fatalf("defaults = %+v, want existing keep_all and consolidate on", c)
	}
	for _, a := range Plan(d, c) {
		if a.Kind == "write-config" {
			t.Errorf("write-config planned for unchanged config: %+v", a)
		}
	}
	for _, a := range Plan(d, Choices{Retention: "rolling", Consolidate: true}) {
		if a.Kind == "write-config" && !reflect.DeepEqual(a.Keys, map[string]any{"retention.mode": "rolling"}) {
			t.Errorf("write-config keys = %v, want only retention.mode", a.Keys)
		}
	}
}

func TestDefaultsFreshMachineWritesNoConfig(t *testing.T) {
	env, _ := testEnv(t, nil, nil)
	d, err := Detect(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range Plan(d, Defaults(d)) {
		if a.Kind == "write-config" {
			t.Errorf("write-config planned on a fresh machine: %+v", a)
		}
	}
}

func TestDefaultsTickOnlyAgentsOnPath(t *testing.T) {
	env, home := testEnv(t, []string{"claude"}, map[string]string{
		".codex/hooks.json":     `{}`,
		".gemini/settings.json": `{}`,
	})
	d, err := Detect(env)
	if err != nil {
		t.Fatal(err)
	}
	c := Defaults(d)
	if !slices.Equal(c.Agents, []string{"claude-code"}) {
		t.Errorf("default agents = %v, want [claude-code]", c.Agents)
	}
	for _, name := range []string{"codex", "gemini"} {
		var found bool
		for _, a := range d.Agents {
			if a.Name == name {
				found = a.Found && !a.OnPath
			}
		}
		if !found {
			t.Errorf("%s: want Found with a settings file and not OnPath; agents = %+v (home %s)", name, d.Agents, home)
		}
	}
}

func TestSetConfigKeysRejectsResultThatDoesNotParse(t *testing.T) {
	kv := map[string]any{"retention.mode": "keep_all"}
	fixtures := map[string]string{
		"top-level dotted key": "retention.mode = \"rolling\"\n",
		"inline table":         "retention = { mode = \"rolling\" }\n",
		"quoted key":           "[retention]\n\"mode\" = \"rolling\"\n",
		"multi-line string":    "[retention]\nmode = \"\"\"\nrolling\"\"\"\n",
	}
	for name, orig := range fixtures {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeTestFile(t, path, orig)
			err := SetConfigKeys(path, kv)
			got := readFile(t, path)
			if err != nil {
				if got != orig {
					t.Errorf("file changed although SetConfigKeys failed with %v:\n%s", err, got)
				}
				return
			}
			var m map[string]any
			if _, derr := toml.Decode(got, &m); derr != nil {
				t.Fatalf("SetConfigKeys wrote text that does not parse: %v\n%s", derr, got)
			}
			if tbl, _ := m["retention"].(map[string]any); tbl["mode"] != "keep_all" {
				t.Errorf("retention.mode = %v, want keep_all", tbl["mode"])
			}
		})
	}
}

func TestSetConfigKeysAtomicWithBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := "# keep\n[retention]\nmode = \"rolling\"\n"
	writeTestFile(t, path, orig)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetConfigKeys(path, map[string]any{"retention.mode": "keep_all"}); err != nil {
		t.Fatal(err)
	}
	backup := path + ".inscribe-backup"
	if got := readFile(t, backup); got != orig {
		t.Errorf("backup = %q, want the original", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("config was rewritten in place, not replaced atomically")
	}
	if after.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", after.Mode().Perm())
	}
	if err := SetConfigKeys(path, map[string]any{"retention.mode": "storage_time"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, backup); got != orig {
		t.Errorf("second change replaced the backup: %q", got)
	}
}
