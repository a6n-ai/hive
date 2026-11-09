package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultsWithNoFiles(t *testing.T) {
	c, warns, err := Load(t.TempDir(), t.TempDir())
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	want := Config{
		Recall:      RecallCfg{Inject: true, SessionBudget: 400, PromptMaxLines: 3, PromptThreshold: 0.35, HalfLifeDays: 30},
		Consolidate: ConsolidateCfg{Auto: false, MinEvents: 20, InputBudget: 8000, Model: map[string]string{"claude-code": "haiku"}},
		Retention:   RetentionCfg{Mode: "rolling", PromptTextDays: 30, PromptClock: "original", EventDays: 180, ReplaySampleMax: 20000, ReplayDays: 0},
		Skills:      SkillsCfg{Enabled: "auto", Enrich: []string{}},
		Scrub:       ScrubCfg{Patterns: []string{}},
		Exclude:     ExcludeCfg{Paths: []string{}},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("defaults:\n got  %+v\n want %+v", c, want)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "[recall]\nsession_budget = 500\n")
	write(t, filepath.Join(proj, ".hive.toml"), "[recall]\nsession_budget = 200\n")
	c, _, err := Load(home, proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.Recall.SessionBudget != 200 || c.Recall.PromptMaxLines != 3 {
		t.Fatalf("recall = %+v", c.Recall)
	}
}

func TestUnknownKeyWarns(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "[recall]\nfoo = 1\n")
	_, warns, err := Load(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "recall.foo") {
		t.Fatalf("warns = %v", warns)
	}
}

func TestInvalidRetentionModeErrors(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "[retention]\nmode = \"forever\"\n")
	_, _, err := Load(home, "")
	if err == nil || !strings.Contains(err.Error(), "storage_time") || !strings.Contains(err.Error(), "keep_all") {
		t.Fatalf("err = %v", err)
	}
}

func TestSkillsEnabledAcceptsBoolOrString(t *testing.T) {
	for body, want := range map[string]string{
		"[skills]\nenabled = true\n":     "true",
		"[skills]\nenabled = false\n":    "false",
		"[skills]\nenabled = \"auto\"\n": "auto",
	} {
		home := t.TempDir()
		write(t, filepath.Join(home, "config.toml"), body)
		c, _, err := Load(home, "")
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if string(c.Skills.Enabled) != want {
			t.Fatalf("%q: enabled = %q, want %q", body, c.Skills.Enabled, want)
		}
	}
}

func TestHome(t *testing.T) {
	t.Setenv("HIVE_HOME", "/tmp/hh")
	if Home() != "/tmp/hh" {
		t.Fatal(Home())
	}
	t.Setenv("HIVE_HOME", "")
	h, _ := os.UserHomeDir()
	if Home() != filepath.Join(h, ".hive") {
		t.Fatal(Home())
	}
}
