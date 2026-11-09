// Package config loads Hive settings: defaults, then $HIVE_HOME/config.toml,
// then <project>/.hive.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Recall      RecallCfg      `toml:"recall"`
	Consolidate ConsolidateCfg `toml:"consolidate"`
	Retention   RetentionCfg   `toml:"retention"`
	Skills      SkillsCfg      `toml:"skills"`
	Scrub       ScrubCfg       `toml:"scrub"`
	Exclude     ExcludeCfg     `toml:"exclude"`
}

type RecallCfg struct {
	Inject          bool    `toml:"inject"`
	SessionBudget   int     `toml:"session_budget"`
	PromptMaxLines  int     `toml:"prompt_max_lines"`
	PromptThreshold float64 `toml:"prompt_threshold"`
	HalfLifeDays    float64 `toml:"half_life_days"`
}

type ConsolidateCfg struct {
	Auto        bool              `toml:"auto"`
	MinEvents   int               `toml:"min_events"`
	InputBudget int               `toml:"input_budget"`
	Model       map[string]string `toml:"model"`
}

type RetentionCfg struct {
	Mode            string `toml:"mode"`
	PromptTextDays  int    `toml:"prompt_text_days"`
	PromptClock     string `toml:"prompt_clock"`
	EventDays       int    `toml:"event_days"`
	ReplaySampleMax int    `toml:"replay_sample_max"`
	ReplayDays      int    `toml:"replay_days"`
}

type SkillsCfg struct {
	Enabled Tristate `toml:"enabled"`
	Enrich  []string `toml:"enrich"`
}

type ScrubCfg struct {
	Patterns []string `toml:"patterns"`
}

type ExcludeCfg struct {
	Paths []string `toml:"paths"`
}

// Tristate is "auto", "true", or "false"; TOML may give a bool or a string.
type Tristate string

func (t *Tristate) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case bool:
		if x {
			*t = "true"
		} else {
			*t = "false"
		}
	case string:
		*t = Tristate(strings.ToLower(x))
	default:
		return fmt.Errorf("want true, false, or \"auto\", got %v", v)
	}
	return nil
}

// Home is $HIVE_HOME or ~/.hive.
func Home() string {
	if h := os.Getenv("HIVE_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".hive"
	}
	return filepath.Join(h, ".hive")
}

// Load returns the effective config for project (which may be ""), plus a
// warning per unknown key.
func Load(home, project string) (Config, []string, error) {
	c := Defaults()
	var warns []string
	files := []string{filepath.Join(home, "config.toml")}
	if project != "" {
		files = append(files, filepath.Join(project, ".hive.toml"))
	}
	for _, f := range files {
		md, err := toml.DecodeFile(f, &c)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return c, warns, fmt.Errorf("%s: %w", f, err)
		}
		for _, k := range md.Undecoded() {
			warns = append(warns, fmt.Sprintf("%s: unknown setting %s", f, k.String()))
		}
	}
	return c, warns, validate(c)
}

func validate(c Config) error {
	check := func(name, got string, allowed ...string) error {
		if !slices.Contains(allowed, got) {
			return fmt.Errorf("%s = %q: allowed values are %s", name, got, strings.Join(allowed, ", "))
		}
		return nil
	}
	return errors.Join(
		check("retention.mode", c.Retention.Mode, "storage_time", "rolling", "keep_all"),
		check("retention.prompt_clock", c.Retention.PromptClock, "original", "stored"),
		check("skills.enabled", string(c.Skills.Enabled), "auto", "true", "false"),
	)
}
