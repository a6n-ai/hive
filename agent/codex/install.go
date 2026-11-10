package codex

import (
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/agent/hookjson"
)

// installEvents omits PreToolUse: Hive never blocks a tool call.
var installEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd"}

func (a *Adapter) SettingsPath() string { return agent.UserPath(".codex", "hooks.json") }

func (a *Adapter) Install(settingsPath, hiveBin string) error {
	entries := make([]hookjson.Entry, 0, len(installEvents))
	for _, ev := range installEvents {
		entries = append(entries, hookjson.Entry{Event: ev, Command: agent.HookCommand(hiveBin, name, ev)})
	}
	return hookjson.Install(settingsPath, entries, hookjson.Owns(name))
}

func (a *Adapter) Uninstall(settingsPath string) error {
	return hookjson.Uninstall(settingsPath, hookjson.Owns(name))
}

func (a *Adapter) InstallNote() string {
	return "Codex: open /hooks in codex and approve the hive hooks before they run."
}

// Warnings reminds the user that Codex runs Hive's hooks only once approved.
func (a *Adapter) Warnings([]byte) []string {
	return []string{"codex: approve hooks in /hooks"}
}
