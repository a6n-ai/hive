package claudecode

import (
	"strings"

	"github.com/a6n-ai/hive/agent/hookjson"
)

var installEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "Stop", "SessionEnd"}

const hookMarker = " hook --agent " + name

func (a *Adapter) Install(settingsPath, hiveBin string) error {
	cmdPrefix := shellQuote(hiveBin) + hookMarker
	entries := make([]hookjson.Entry, 0, len(installEvents))
	for _, ev := range installEvents {
		entries = append(entries, hookjson.Entry{Event: ev, Command: cmdPrefix + " " + ev})
	}
	return hookjson.Install(settingsPath, entries, hookjson.Owns(name))
}

func (a *Adapter) Uninstall(settingsPath string) error {
	return hookjson.Uninstall(settingsPath, hookjson.Owns(name))
}

// shellQuote quotes a binary path for the shell that runs hook commands.
func shellQuote(p string) string {
	if p != "" && strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
