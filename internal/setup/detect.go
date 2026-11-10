// Package setup detects the machine, plans the steps of hive setup, and applies them.
package setup

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/config"

	_ "github.com/a6n-ai/hive/agent/claudecode"
	_ "github.com/a6n-ai/hive/agent/codex"
	_ "github.com/a6n-ai/hive/agent/gemini"
)

// Env is what Detect reads from the machine; tests inject fakes.
type Env struct {
	Home     string
	HiveBin  string
	LookPath func(string) (string, error)
	ReadFile func(string) ([]byte, error)
}

type AgentInfo struct {
	Name, Settings                     string
	Found, OnPath, Installed, Verified bool
}

type Detected struct {
	Agents      []AgentInfo
	ClaudeMem   bool
	ClaudeMemID string
	ConfigPath  string
	HasHistory  bool
	HiveBin     string
	Current     config.Config
}

type Choices struct {
	Agents           []string
	DisableClaudeMem bool
	ImportSince      string
	Retention        string
	Consolidate      bool
	MCP              bool
}

type Action struct {
	Kind    string
	Desc    string
	Files   []string
	Command []string
	Keys    map[string]any
	Agent   string
	Since   string
}

var agentOrder = []string{"claude-code", "codex", "gemini"}

var agentBinary = map[string]string{"claude-code": "claude", "codex": "codex", "gemini": "gemini"}

// unverified agents are offered with a label saying their hooks are untested.
var unverified = map[string]bool{"gemini": true}

func Detect(env Env) (Detected, error) {
	d := Detected{ConfigPath: filepath.Join(config.Home(), "config.toml"), HiveBin: env.HiveBin}
	cur, _, err := config.Load(config.Home(), "")
	if err != nil {
		return d, err
	}
	d.Current = cur
	for _, name := range agentOrder {
		ad, err := agent.Get(name)
		if err != nil {
			return d, err
		}
		info := AgentInfo{Name: name, Settings: ad.SettingsPath(), Verified: !unverified[name]}
		raw, readErr := env.ReadFile(info.Settings)
		info.Installed = readErr == nil && agent.IsHookCommand(string(raw), name)
		_, pathErr := env.LookPath(agentBinary[name])
		info.OnPath = pathErr == nil
		info.Found = info.OnPath || readErr == nil
		d.Agents = append(d.Agents, info)
	}
	claude := d.Agents[0].Settings
	if raw, err := env.ReadFile(claude); err == nil {
		d.ClaudeMemID = claudeMemPlugin(raw)
		d.ClaudeMem = d.ClaudeMemID != ""
	}
	transcripts, _ := filepath.Glob(filepath.Join(env.Home, ".claude", "projects", "*", "*.jsonl"))
	d.HasHistory = len(transcripts) > 0
	return d, nil
}

// claudeMemPlugin returns the enabled claude-mem plugin key in a Claude Code settings file, or "".
func claudeMemPlugin(raw []byte) string {
	var s struct {
		EnabledPlugins map[string]any `json:"enabledPlugins"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	for _, k := range slices.Sorted(maps.Keys(s.EnabledPlugins)) {
		if on, _ := s.EnabledPlugins[k].(bool); on && strings.Contains(k, "claude-mem") {
			return k
		}
	}
	return ""
}

func Defaults(d Detected) Choices {
	c := Choices{DisableClaudeMem: d.ClaudeMem, Retention: d.Current.Retention.Mode, Consolidate: d.Current.Consolidate.Auto}
	for _, a := range d.Agents {
		if a.OnPath {
			c.Agents = append(c.Agents, a.Name)
		}
	}
	if d.HasHistory {
		c.ImportSince = "30d"
	}
	return c
}

func Plan(d Detected, c Choices) []Action {
	var out []Action
	for _, a := range d.Agents {
		if slices.Contains(c.Agents, a.Name) {
			out = append(out, Action{Kind: "install-hooks", Desc: "install Hive hooks for " + a.Name, Files: []string{a.Settings}, Agent: a.Name})
		}
	}
	if c.DisableClaudeMem && d.ClaudeMem {
		out = append(out, Action{Kind: "disable-plugin", Desc: "disable claude-mem, which Hive replaces",
			Files: []string{d.Agents[0].Settings}, Command: []string{"claude", "plugin", "disable", d.ClaudeMemID}})
	}
	if c.ImportSince != "" && d.HasHistory {
		out = append(out, Action{Kind: "import", Desc: "import Claude Code history, " + importLabel(c.ImportSince),
			Files: []string{filepath.Join(filepath.Dir(d.ConfigPath), "hive.db")}, Since: c.ImportSince})
	}
	if keys := changedKeys(d, c); len(keys) > 0 {
		out = append(out, Action{Kind: "write-config", Desc: "write changed retention and consolidation settings",
			Files: []string{d.ConfigPath}, Keys: keys})
	}
	if c.MCP {
		out = append(out, Action{Kind: "mcp-add", Desc: "add Hive as an MCP server to Claude Code",
			Command: []string{"claude", "mcp", "add", "hive", "--", d.HiveBin, "mcp"}})
	}
	return out
}

// changedKeys returns only the config keys whose chosen value differs from the current one.
func changedKeys(d Detected, c Choices) map[string]any {
	keys := map[string]any{}
	if c.Retention != d.Current.Retention.Mode {
		keys["retention.mode"] = c.Retention
	}
	if c.Consolidate != d.Current.Consolidate.Auto {
		keys["consolidate.auto"] = c.Consolidate
	}
	return keys
}

func importLabel(since string) string {
	if since == "all" {
		return "all of it"
	}
	return "the last " + strings.TrimSuffix(since, "d") + " days"
}

func Warnings(_ Detected, c Choices) []string {
	var out []string
	if c.Retention == "rolling" && c.ImportSince == "all" {
		out = append(out, fmt.Sprintf("events older than %d days will be pruned at the next session start; "+
			"their prompt text is kept in the replay sample", config.Defaults().Retention.EventDays))
	}
	return out
}
