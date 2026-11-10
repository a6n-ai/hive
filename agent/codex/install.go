package codex

import (
	"os"
	"path/filepath"

	"github.com/a6n-ai/inscribe/agent"
	"github.com/a6n-ai/inscribe/agent/hookjson"
)

// installEvents omits PreToolUse: Inscribe never blocks a tool call.
var installEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd"}

// codexHome is where Codex reads its hooks and skills: $CODEX_HOME when set.
func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return agent.UserPath(".codex")
}

func (a *Adapter) SettingsPath() string { return filepath.Join(codexHome(), "hooks.json") }

func (a *Adapter) Install(settingsPath, inscribeBin string) error {
	entries := make([]hookjson.Entry, 0, len(installEvents))
	for _, ev := range installEvents {
		entries = append(entries, hookjson.Entry{Event: ev, Command: agent.HookCommand(inscribeBin, name, ev)})
	}
	return hookjson.Install(settingsPath, entries, hookjson.Owns(name))
}

func (a *Adapter) Uninstall(settingsPath string) error {
	return hookjson.Uninstall(settingsPath, hookjson.Owns(name))
}

func (a *Adapter) InstallNote() string {
	return "Codex: open /hooks in codex and approve the inscribe hooks before they run."
}

// Warnings reminds the user that Codex runs Inscribe's hooks only once approved.
func (a *Adapter) Warnings([]byte) []string {
	return []string{"codex: approve hooks in /hooks"}
}
