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
)

var sectionRe = regexp.MustCompile(`^\s*\[([^\[\]]+)\]`)

// SetConfigKeys sets "section.key" values in a TOML file. It edits lines in
// place, so comments and other keys survive; a missing file is created.
func SetConfigKeys(path string, kv map[string]any) error {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the config path is the user's own file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var lines []string
	if t := strings.TrimSuffix(string(raw), "\n"); t != "" {
		lines = strings.Split(t, "\n")
	}
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		sec, name, ok := strings.Cut(k, ".")
		if !ok {
			return fmt.Errorf("config key %q: want section.key", k)
		}
		val, err := tomlValue(kv[k])
		if err != nil {
			return err
		}
		lines = setKey(lines, sec, name, val)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600) //nolint:gosec // G703: the config path is the user's own file
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
