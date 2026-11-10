package gemini

import (
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/agent/hookjson"
)

var installEvents = []string{"SessionStart", "BeforeAgent", "AfterTool", "AfterAgent", "SessionEnd"}

func (a *Adapter) SettingsPath() string { return agent.UserPath(".gemini", "settings.json") }

func (a *Adapter) Install(settingsPath, hiveBin string) error {
	extra := map[string]any{"matcher": "*", "name": "hive"}
	entries := make([]hookjson.Entry, 0, len(installEvents))
	for _, ev := range installEvents {
		entries = append(entries, hookjson.Entry{Event: ev, Command: agent.HookCommand(hiveBin, name, ev), Extra: extra})
	}
	return hookjson.Install(settingsPath, entries, hookjson.Owns(name))
}

func (a *Adapter) Uninstall(settingsPath string) error {
	return hookjson.Uninstall(settingsPath, hookjson.Owns(name))
}

// Warnings reports that Gemini's hooks are unverified, and that a disabled
// hooksConfig stops them from running at all.
func (a *Adapter) Warnings(settings []byte) []string {
	var m struct {
		HooksConfig struct {
			Enabled *bool `json:"enabled"`
		} `json:"hooksConfig"`
	}
	_, _ = hookjson.Inspect(settings, &m)
	out := []string{"gemini adapter: written from documentation, not verified against a live Gemini CLI"}
	if m.HooksConfig.Enabled != nil && !*m.HooksConfig.Enabled {
		out = append(out, "gemini: hooksConfig.enabled is false — hooks will not run")
	}
	return out
}
