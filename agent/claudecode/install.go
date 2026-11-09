package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if hm, ok := m["hooks"].(map[string]any); ok {
		for _, ev := range installEvents {
			if v, ok := hm[ev]; ok && v != nil {
				if _, isArr := v.([]any); !isArr {
					return false, fmt.Errorf("hooks.%s is not an array", ev)
				}
			}
		}
	} else if m["hooks"] != nil {
		return false, errors.New("hooks is not an object")
	}
	before, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	// Any existing Hive hook goes first, so a reinstall from another binary
	// path replaces the old command rather than adding a second one.
	removeHooks(m)
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}
	cmdPrefix := shellQuote(hiveBin) + hookMarker
	for _, ev := range installEvents {
		groups, _ := hooks[ev].([]any)
		hook := map[string]any{"type": "command", "command": cmdPrefix + " " + ev}
		hooks[ev] = append(groups, map[string]any{"hooks": []any{hook}})
	}
	m["hooks"] = hooks
	after, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(before, after), nil
}

// shellQuote quotes a binary path for the shell that runs hook commands.
func shellQuote(p string) string {
	if p != "" && strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
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

func isHiveHook(h any) bool {
	hm, _ := h.(map[string]any)
	c, _ := hm["command"].(string)
	return strings.Contains(c, hookMarker+" ")
}

// editSettings applies change to the JSON settings file at path. It writes
// only when change reports a change, keeps the file's mode, backs up the
// original once, and refuses to touch a file it cannot parse.
func editSettings(path string, change func(map[string]any) (bool, error)) error {
	// A symlinked settings file (dotfiles managers) is edited where it points,
	// so the link survives and the target is the file that changes.
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
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
		if err := decodeSettings(raw, &m); err != nil {
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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes(), mode)
}

// decodeSettings parses one JSON object. Numbers stay as written, so a large
// value in another tool's settings is not rounded.
func decodeSettings(raw []byte, m *map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(m); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the JSON object")
	}
	return nil
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
