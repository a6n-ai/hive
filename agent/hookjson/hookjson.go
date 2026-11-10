// Package hookjson installs and removes Inscribe's hook entries in an agent's JSON
// settings file. Claude Code, Codex and Gemini CLI share the shape
// "hooks": {event: [{matcher?, hooks: [{type, command, ...}]}]}.
package hookjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/a6n-ai/inscribe/agent"
)

// Entry is one hook to install. Extra keys are merged in: "matcher" sets the
// group's matcher, and every other key sets a field on the hook itself.
type Entry struct {
	Event, Command string
	Extra          map[string]any
}

// Owns reports whether a command was installed by Inscribe for the named agent,
// whatever binary path it was installed from.
func Owns(agentName string) func(cmd string) bool {
	return func(cmd string) bool { return agent.IsHookCommand(cmd, agentName) }
}

// errComments means a settings file has comments, which a rewrite would drop.
var errComments = errors.New("settings have comments, so inscribe cannot edit them safely")

// Install replaces every hook owns recognises with entries. It writes only
// when the file changes. A file with comments is refused, with the hooks to
// paste in by hand.
func Install(path string, entries []Entry, owns func(cmd string) bool) error {
	err := editSettings(path, func(m map[string]any) (bool, error) {
		return installHooks(m, entries, owns)
	})
	if errors.Is(err, errComments) {
		return fmt.Errorf("%w; paste this into the file by hand:\n%s", err, snippet(entries))
	}
	return err
}

// snippet is the hooks block Install would write, for pasting by hand.
func snippet(entries []Entry) string {
	m := map[string]any{}
	_, _ = installHooks(m, entries, func(string) bool { return false })
	b, _ := json.MarshalIndent(map[string]any{"hooks": m["hooks"]}, "", "  ")
	return string(b)
}

// Inspect decodes settings for reading only. Comments are allowed, and
// commented reports whether they were needed. Writers never use it.
func Inspect(raw []byte, v any) (commented bool, err error) {
	err = json.Unmarshal(raw, v)
	if err == nil {
		return false, nil
	}
	if json.Unmarshal(stripComments(raw), v) != nil {
		return false, err
	}
	return true, nil
}

// stripComments removes // and /* */ comments outside string literals.
func stripComments(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		switch {
		case raw[i] == '"':
			end := i + 1
			for end < len(raw) && raw[end] != '"' {
				if raw[end] == '\\' {
					end++
				}
				end++
			}
			end = min(end+1, len(raw))
			out = append(out, raw[i:end]...)
			i = end - 1
		case raw[i] == '/' && i+1 < len(raw) && raw[i+1] == '/':
			k := bytes.IndexByte(raw[i:], '\n')
			if k < 0 {
				return out
			}
			i += k - 1
		case raw[i] == '/' && i+1 < len(raw) && raw[i+1] == '*':
			k := bytes.Index(raw[i+2:], []byte("*/"))
			if k < 0 {
				return out
			}
			i += k + 3
		default:
			out = append(out, raw[i])
		}
	}
	return out
}

// Uninstall removes every hook owns recognises.
func Uninstall(path string, owns func(cmd string) bool) error {
	return editSettings(path, func(m map[string]any) (bool, error) {
		return removeHooks(m, owns), nil
	})
}

func installHooks(m map[string]any, entries []Entry, owns func(cmd string) bool) (bool, error) {
	if err := checkShape(m, entries); err != nil {
		return false, err
	}
	before, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	// Any existing hook of ours goes first, so a reinstall from another binary
	// path replaces the old command rather than adding a second one.
	removeHooks(m, owns)
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}
	for _, e := range entries {
		groups, _ := hooks[e.Event].([]any)
		hooks[e.Event] = append(groups, group(e))
	}
	m["hooks"] = hooks
	after, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(before, after), nil
}

func checkShape(m map[string]any, entries []Entry) error {
	hm, ok := m["hooks"].(map[string]any)
	if !ok {
		if m["hooks"] != nil {
			return errors.New("hooks is not an object")
		}
		return nil
	}
	for _, e := range entries {
		if v, ok := hm[e.Event]; ok && v != nil {
			if _, isArr := v.([]any); !isArr {
				return fmt.Errorf("hooks.%s is not an array", e.Event)
			}
		}
	}
	return nil
}

func group(e Entry) map[string]any {
	hook := map[string]any{"type": "command", "command": e.Command}
	g := map[string]any{}
	for k, v := range e.Extra {
		if k == "matcher" {
			g[k] = v
		} else {
			hook[k] = v
		}
	}
	g["hooks"] = []any{hook}
	return g
}

// removeHooks deletes every owned hook, and the groups, events and hooks key
// that this leaves empty. Anything it did not add is left as it was.
func removeHooks(m map[string]any, owns func(cmd string) bool) bool {
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
				if !isOwned(h, owns) {
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

func isOwned(h any, owns func(cmd string) bool) bool {
	hm, _ := h.(map[string]any)
	c, _ := hm["command"].(string)
	return owns(c)
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
			var scratch map[string]any
			if commented, cerr := Inspect(raw, &scratch); commented && cerr == nil {
				return fmt.Errorf("%s: %w", path, errComments)
			}
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
		backup := path + ".inscribe-backup"
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".inscribe-settings-*")
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
