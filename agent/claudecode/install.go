package claudecode

import (
	"github.com/a6n-ai/inscribe/agent"
	"github.com/a6n-ai/inscribe/agent/hookjson"
)

var installEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "Stop", "SessionEnd"}

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
