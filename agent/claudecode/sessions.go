package claudecode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/a6n-ai/inscribe/agent"
)

// LiveSessions reports which Claude Code sessions have a running process.
// Claude Code writes ~/.claude/sessions/<pid>.json while it runs and removes it
// on a clean exit, so a file whose process is gone is a closed window or crash.
func (a *Adapter) LiveSessions() (map[string]bool, bool) {
	return liveSessions(agent.UserPath(".claude", "sessions"))
}

// shortcut: a live pid counts without checking procStart, so a crashed session
// whose pid was reused looks live; the idle cutoff still closes it.
func liveSessions(dir string) (map[string]bool, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	live := map[string]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: a file in Claude Code's own sessions directory
		if err != nil {
			continue
		}
		var f struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(b, &f) != nil || f.PID <= 0 || f.SessionID == "" {
			continue
		}
		if err := syscall.Kill(f.PID, 0); err == nil || errors.Is(err, syscall.EPERM) {
			live[f.SessionID] = true
		}
	}
	return live, true
}
