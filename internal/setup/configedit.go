package setup

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

var sectionRe = regexp.MustCompile(`^\s*\[([^\[\]]+)\]`)

// SetConfigKeys sets "section.key" values in a TOML file. It edits lines in
// place, so comments and other keys survive. The result must parse and hold
// the new values, or the file is left alone. The first change keeps a
// .inscribe-backup copy, and the write replaces the file atomically.
func SetConfigKeys(path string, kv map[string]any) error {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the config path is the user's own file
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text, err := editText(string(raw), kv)
	if err != nil {
		return err
	}
	if err := checkKeys(text, kv); err != nil {
		return fmt.Errorf("%s: cannot set %s: %w; edit the file by hand", path, strings.Join(slices.Sorted(maps.Keys(kv)), ", "), err)
	}
	if text == string(raw) {
		return nil
	}
	mode := os.FileMode(0o600)
	if info, serr := os.Stat(path); serr == nil {
		mode = info.Mode().Perm()
	}
	if existed {
		if err := keepBackup(path, raw, mode); err != nil {
			return err
		}
	}
	return writeAtomic(path, text, mode)
}

// editText returns orig with each key set in its section.
func editText(orig string, kv map[string]any) (string, error) {
	if len(kv) == 0 {
		return orig, nil
	}
	var lines []string
	if t := strings.TrimSuffix(orig, "\n"); t != "" {
		lines = strings.Split(t, "\n")
	}
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		sec, name, ok := strings.Cut(k, ".")
		if !ok {
			return "", fmt.Errorf("config key %q: want section.key", k)
		}
		val, err := tomlValue(kv[k])
		if err != nil {
			return "", err
		}
		lines = setKey(lines, sec, name, val)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// checkKeys decodes text and confirms each key holds its new value.
func checkKeys(text string, kv map[string]any) error {
	var m map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		return fmt.Errorf("the result does not parse: %w", err)
	}
	for k, want := range kv {
		sec, name, _ := strings.Cut(k, ".")
		tbl, _ := m[sec].(map[string]any)
		if got, ok := tbl[name]; !ok || !sameValue(got, want) {
			return fmt.Errorf("%s did not take the value %v", k, want)
		}
	}
	return nil
}

// sameValue compares a decoded TOML value with a Go value that tomlValue can write.
func sameValue(got, want any) bool {
	if i, ok := want.(int); ok {
		return got == int64(i)
	}
	return got == want
}

// keepBackup writes the original beside the file, unless a backup already exists.
func keepBackup(path string, raw []byte, mode os.FileMode) error {
	backup := path + ".inscribe-backup"
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(backup, raw, mode) //nolint:gosec // G306: the mode is the original file's
}

// writeAtomic writes text to a temp file beside path and renames it over path.
func writeAtomic(path, text string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// setKey replaces the key's line in its section, or adds the key after the
// section header, or appends the section when it is missing.
func setKey(lines []string, sec, name, val string) []string {
	line := name + " = " + val
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(name) + `\s*=`)
	cur := ""
	for i, l := range lines {
		if m := sectionRe.FindStringSubmatch(l); m != nil {
			cur = strings.TrimSpace(m[1])
			continue
		}
		if cur == sec && keyRe.MatchString(l) {
			lines[i] = line
			return lines
		}
	}
	for i, l := range lines {
		if m := sectionRe.FindStringSubmatch(l); m != nil && strings.TrimSpace(m[1]) == sec {
			return slices.Insert(lines, i+1, line)
		}
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return append(lines, "["+sec+"]", line)
}

func tomlValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	}
	return "", fmt.Errorf("config value %v: unsupported type %T", v, v)
}
