package claudecode

import (
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/agent/hookjson"
)

var installEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "Stop", "SessionEnd"}

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
