package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var installEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "Stop", "SessionEnd"}

const hookMarker = " hook --agent claude-code"

func (a *Adapter) Install(settingsPath, hiveBin string) error {
	return editSettings(settingsPath, func(m map[string]any) (bool, error) {
		return installHooks(m, hiveBin)
	})
}

func (a *Adapter) Uninstall(settingsPath string) error {
	return editSettings(settingsPath, func(m map[string]any) (bool, error) {
		return removeHooks(m), nil
	})
}

func installHooks(m map[string]any, hiveBin string) (bool, error) {
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		if m["hooks"] != nil {
			return false, errors.New("hooks is not an object")
		}
		hooks = map[string]any{}
	}
	prefix := hiveBin + hookMarker
	changed := false
	for _, ev := range installEvents {
		groups, ok := hooks[ev].([]any)
		if !ok && hooks[ev] != nil {
			return false, fmt.Errorf("hooks.%s is not an array", ev)
		}
		if hasHook(groups, func(c string) bool { return strings.HasPrefix(c, prefix) }) {
			continue
		}
		hook := map[string]any{"type": "command", "command": prefix + " " + ev}
		hooks[ev] = append(groups, map[string]any{"hooks": []any{hook}})
		changed = true
	}
	if changed {
		m["hooks"] = hooks
	}
	return changed, nil
}

// removeHooks deletes every Hive hook, and the groups, events and hooks key
// that this leaves empty. Anything it did not add is left as it was.
func removeHooks(m map[string]any) bool {
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for ev, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		eventChanged := false
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hs, _ := gm["hooks"].([]any)
			var keptHooks []any
			for _, h := range hs {
				if !isHiveHook(h) {
					keptHooks = append(keptHooks, h)
				}
			}
			if len(keptHooks) == len(hs) {
				kept = append(kept, g)
				continue
			}
			eventChanged = true
			if len(keptHooks) > 0 {
				gm["hooks"] = keptHooks
				kept = append(kept, gm)
			}
		}
		if !eventChanged {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if changed && len(hooks) == 0 {
		delete(m, "hooks")
	}
	return changed
}

func hasHook(groups []any, match func(command string) bool) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); match(c) {
				return true
			}
		}
	}
	return false
}

func isHiveHook(h any) bool {
	hm, _ := h.(map[string]any)
	c, _ := hm["command"].(string)
	return strings.Contains(c, hookMarker+" ")
}

// editSettings applies change to the JSON settings file at path. It writes
// only when change reports a change, keeps the file's mode, backs up the
// original once, and refuses to touch a file it cannot parse.
func editSettings(path string, change func(map[string]any) (bool, error)) error {
	m := map[string]any{}
	mode := os.FileMode(0o600)
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the settings path is the user's own file, chosen on the command line
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: not valid JSON, left unchanged: %w", path, err)
		}
		if m == nil {
			m = map[string]any{}
		}
	}
	changed, err := change(m)
	if err != nil || !changed {
		return err
	}
	if raw != nil {
		backup := path + ".hive-backup"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backup, raw, mode); err != nil { //nolint:gosec // G703: the settings path is the user's own file, chosen on the command line
				return err
			}
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'), mode)
}

func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hive-settings-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
