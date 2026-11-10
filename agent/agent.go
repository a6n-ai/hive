// Package agent is the seam between Hive and a coding agent: how an agent's
// hook payloads become events, what it is told back, and how its hooks are
// installed. Each agent lives in its own subpackage and registers itself.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

// Caps describes what an agent's hooks allow Hive to do.
type Caps struct {
	CanInject, CanBlock, HasPreTool, HasSessionEnd bool
}

type Adapter interface {
	Name() string
	Parse(event string, stdin []byte) (model.Event, error)
	Reply(event string, lines []recall.Line) []byte
	Capabilities() Caps
	SettingsPath() string
	Install(settingsPath, hiveBin string) error
	Uninstall(settingsPath string) error
	InstructionsFile() string
	SkillDirs() []string
	Headless(model string) HeadlessSpec
}

// InstallNoter is implemented by an adapter whose hooks need a manual step
// after install, such as approving them in the agent.
type InstallNoter interface {
	InstallNote() string
}

// HeadlessSpec describes a one-shot, non-interactive model call: the prompt is
// written to Command's stdin, and ParseOutput extracts the reply text.
type HeadlessSpec struct {
	Command     []string
	ParseOutput func([]byte) (string, error)
}

var registry = map[string]Adapter{}

// Register makes an adapter available to Get. Adapters register from init.
func Register(a Adapter) { registry[a.Name()] = a }

func Get(name string) (Adapter, error) {
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("agent: unknown agent %q", name)
	}
	return a, nil
}

// HookCommand is the command line an agent runs for one hook event.
func HookCommand(bin, agentName, event string) string {
	return ShellQuote(bin) + " hook --agent " + agentName + " " + event
}

// IsHookCommand reports whether cmd is a hook Hive installed for agentName,
// from any binary path.
func IsHookCommand(cmd, agentName string) bool {
	return strings.Contains(cmd, " hook --agent "+agentName+" ")
}

// ShellQuote quotes a word for the shell that runs hook commands.
func ShellQuote(p string) string {
	if p != "" && strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// UserPath joins elem under the user's home directory.
func UserPath(elem ...string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(append([]string{home}, elem...)...)
}

// ContextJSON is the hook reply that injects recalled lines into an agent's
// context, or nil when there is nothing to inject.
func ContextJSON(event string, lines []recall.Line) []byte {
	if len(lines) == 0 {
		return nil
	}
	out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     event,
		"additionalContext": recall.Render(lines),
	}})
	if err != nil {
		return nil
	}
	return out
}

// ExitCode reads a tool's exit code from the response keys agents use, or from
// metadata.exit_code. ok is false when the response carries no code.
func ExitCode(resp any) (code int, ok bool) {
	m, isMap := resp.(map[string]any)
	if !isMap {
		return 0, false
	}
	for _, k := range []string{"exit_code", "exitCode", "returncode", "status_code"} {
		if n, isNum := m[k].(float64); isNum {
			return int(n), true
		}
	}
	if md, isMap := m["metadata"].(map[string]any); isMap {
		if n, isNum := md["exit_code"].(float64); isNum {
			return int(n), true
		}
	}
	return 0, false
}

// ResponseText is the text of a tool response: the string itself, or its
// output or stderr field.
func ResponseText(resp any) string {
	switch v := resp.(type) {
	case string:
		return v
	case map[string]any:
		for _, k := range []string{"output", "stderr"} {
			if s, ok := v[k].(string); ok {
				return s
			}
		}
	}
	return ""
}

// Warner is implemented by an adapter that reports problems with its own
// settings file, given the file's bytes, when Hive hooks are installed.
type Warner interface {
	Warnings(settings []byte) []string
}
