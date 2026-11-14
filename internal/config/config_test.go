package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
		Consolidate: ConsolidateCfg{Auto: true, MinEvents: 20, InputBudget: 8000, Model: map[string]string{"claude-code": "haiku"}},
		Retention:   RetentionCfg{Mode: "rolling", PromptTextDays: 30, PromptClock: "original", EventDays: 180, ReplaySampleMax: 20000, ReplayDays: 0},
		Skills:      SkillsCfg{Enabled: "auto", Enrich: []string{}},
		Scrub:       ScrubCfg{Patterns: []string{}},
		Exclude:     ExcludeCfg{Paths: []string{}},
		Stats:       StatsCfg{SessionLine: true, HoldoutMinSample: 100, IdleMinutes: 5},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("defaults:\n got  %+v\n want %+v", c, want)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "[recall]\nsession_budget = 500\n")
	write(t, filepath.Join(proj, ".inscribe.toml"), "[recall]\nsession_budget = 200\n")
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

// A project's .inscribe.toml comes from whatever repository is checked out, so it
// must not weaken the user's own settings.
func TestProjectCannotWeakenSecurity(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "[exclude]\npaths = [\"/a\"]\n[scrub]\npatterns = [\"X1\"]\n")
	write(t, filepath.Join(proj, ".inscribe.toml"), `[exclude]
paths = []
[scrub]
patterns = ["Y2"]
[retention]
mode = "keep_all"
[consolidate]
auto = false
[consolidate.model]
claude-code = "opus"
[recall]
session_budget = 100
`)
	c, warns, err := Load(home, proj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Exclude.Paths, []string{"/a"}) {
		t.Errorf("exclude.paths = %v, want [/a]", c.Exclude.Paths)
	}
	if !reflect.DeepEqual(c.Scrub.Patterns, []string{"X1", "Y2"}) {
		t.Errorf("scrub.patterns = %v, want [X1 Y2]", c.Scrub.Patterns)
	}
	if c.Retention.Mode != "rolling" || !c.Consolidate.Auto || c.Consolidate.Model["claude-code"] != "haiku" {
		t.Errorf("project changed protected settings: retention=%s auto=%v model=%v", c.Retention.Mode, c.Consolidate.Auto, c.Consolidate.Model)
	}
	if c.Recall.SessionBudget != 100 {
		t.Errorf("recall.session_budget = %d, want 100 (projects may tune recall)", c.Recall.SessionBudget)
	}
	joined := strings.Join(warns, "\n")
	for _, k := range []string{"retention.mode", "consolidate.auto", "consolidate.model"} {
		if !strings.Contains(joined, k) {
			t.Errorf("no warning for ignored %s; warns=%v", k, warns)
		}
	}
}

func TestShareGlobalOnly(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "[share]\nanalytics = true\nurl = \"https://x\"\n")
	write(t, filepath.Join(proj, ".inscribe.toml"), "[share]\nanalytics = false\nurl = \"https://evil\"\n")
	c, warns, err := Load(home, proj)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Share.Analytics || c.Share.URL != "https://x" {
		t.Fatalf("share = %+v, want global settings", c.Share)
	}
	joined := strings.Join(warns, "\n")
	for _, k := range []string{"share.analytics", "share.url"} {
		if !strings.Contains(joined, k) {
			t.Errorf("no warning for ignored %s; warns=%v", k, warns)
		}
	}
}

func TestHome(t *testing.T) {
	t.Setenv("INSCRIBE_HOME", "/tmp/hh")
	if Home() != "/tmp/hh" {
		t.Fatal(Home())
	}
	t.Setenv("INSCRIBE_HOME", "")
	h, _ := os.UserHomeDir()
	if Home() != filepath.Join(h, ".inscribe") {
		t.Fatal(Home())
	}
}

func TestStatsDefaults(t *testing.T) {
	c, _, err := Load(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := (StatsCfg{true, 0, 100, 5}); c.Stats != want {
		t.Fatalf("stats = %+v, want %+v", c.Stats, want)
	}
}

func TestStatsValidate(t *testing.T) {
	for body, wantErr := range map[string]bool{
		"[stats]\nholdout = -0.1\n":         true,
		"[stats]\nholdout = 0.6\n":          true,
		"[stats]\nholdout = 0.5\n":          false,
		"[stats]\nidle_minutes = 0\n":       true,
		"[stats]\nholdout_min_sample = 0\n": true,
		"[stats]\nholdout = nan\n":          true,
	} {
		home := t.TempDir()
		write(t, filepath.Join(home, "config.toml"), body)
		_, _, err := Load(home, "")
		if (err != nil) != wantErr {
			t.Fatalf("%q: err = %v, wantErr %v", body, err, wantErr)
		}
		if wantErr && strings.Contains(body, "holdout") && !strings.Contains(err.Error(), "stats.holdout") {
			t.Fatalf("%q: err = %v, want stats.holdout in text", body, err)
		}
	}
}

func TestStatsProjectIgnored(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, filepath.Join(proj, ".inscribe.toml"), "[stats]\nholdout = 0.2\n")
	c, warns, err := Load(home, proj)
	if err != nil {
		t.Fatal(err)
	}
	if c.Stats.Holdout != 0 {
		t.Fatalf("holdout = %v, project must not set it", c.Stats.Holdout)
	}
	want := filepath.Join(proj, ".inscribe.toml") + ": ignored stats.holdout (only your global config.toml may set it)"
	if !slices.Contains(warns, want) {
		t.Fatalf("warns = %v, want %q", warns, want)
	}
}
