// Package config loads Inscribe settings: defaults, then $INSCRIBE_HOME/config.toml,
// then <project>/.inscribe.toml.
package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
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
	Share       ShareCfg       `toml:"share"`
	// Debug shows extra diagnostics, such as what each hook event saved. INSCRIBE_DEBUG=1 also turns it on.
	Debug bool `toml:"debug"`
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

// ShareCfg is global-only: a project file cannot turn on uploads or pick the URL.
type ShareCfg struct {
	Analytics bool   `toml:"analytics"`
	URL       string `toml:"url"`
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

// Home is $INSCRIBE_HOME or ~/.inscribe.
func Home() string {
	if h := os.Getenv("INSCRIBE_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".inscribe"
	}
	return filepath.Join(h, ".inscribe")
}

// Load returns the effective config for project (which may be ""), plus a
// warning per unknown or ignored key.
//
// A project's .inscribe.toml comes from whatever repository is checked out, so it
// is untrusted: it may tune [recall] and [skills], may add to exclude.paths and
// scrub.patterns, and nothing else.
func Load(home, project string) (Config, []string, error) {
	c := Defaults()
	var warns []string
	global := filepath.Join(home, "config.toml")
	md, err := toml.DecodeFile(global, &c)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, warns, fmt.Errorf("%s: %w", global, err)
	default:
		warns = append(warns, unknownKeys(global, md)...)
	}
	if project != "" {
		// A project file is untrusted and optional: its errors are warnings, and the
		// global config still applies.
		pw, err := overlayProject(&c, filepath.Join(project, ".inscribe.toml"))
		warns = append(warns, pw...)
		if err != nil {
			warns = append(warns, err.Error()+" (project file ignored)")
		}
	}
	if os.Getenv("INSCRIBE_DEBUG") == "1" {
		c.Debug = true
	}
	return c, warns, validate(c)
}

func unknownKeys(file string, md toml.MetaData) []string {
	var out []string
	for _, k := range md.Undecoded() {
		out = append(out, fmt.Sprintf("%s: unknown setting %s", file, k.String()))
	}
	return out
}

func overlayProject(c *Config, file string) ([]string, error) {
	p := *c
	p.Scrub.Patterns, p.Exclude.Paths = nil, nil
	// Copy reference fields so decoding the project file cannot write into c.
	p.Consolidate.Model = maps.Clone(c.Consolidate.Model)
	p.Skills.Enrich = slices.Clone(c.Skills.Enrich)
	md, err := toml.DecodeFile(file, &p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	warns := unknownKeys(file, md)
	var valid []string
	for _, re := range p.Scrub.Patterns {
		if _, err := regexp.Compile(re); err != nil {
			warns = append(warns, fmt.Sprintf("%s: ignored scrub pattern %q: %v", file, re, err))
			continue
		}
		valid = append(valid, re)
	}
	p.Scrub.Patterns = valid
	for _, k := range md.Keys() {
		if len(k) < 2 {
			continue
		}
		switch k[0] {
		case "recall", "skills":
		case "scrub":
			if k[1] != "patterns" {
				warns = append(warns, fmt.Sprintf("%s: ignored %s (projects may only add scrub.patterns)", file, k.String()))
			}
		case "exclude":
			if k[1] != "paths" {
				warns = append(warns, fmt.Sprintf("%s: ignored %s (projects may only add exclude.paths)", file, k.String()))
			}
		default:
			if len(k) == 2 || k[0] == "consolidate" && k[1] == "model" && len(k) == 3 {
				warns = append(warns, fmt.Sprintf("%s: ignored %s (only your global config.toml may set it)", file, k.String()))
			}
		}
	}
	c.Recall, c.Skills = p.Recall, p.Skills
	c.Scrub.Patterns = union(c.Scrub.Patterns, p.Scrub.Patterns)
	c.Exclude.Paths = union(c.Exclude.Paths, p.Exclude.Paths)
	return warns, nil
}

func union(a, b []string) []string {
	out := append([]string{}, a...)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
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
