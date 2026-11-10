package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	blockBegin = "<!-- hive:begin -->"
	blockEnd   = "<!-- hive:end -->"
)

// WriteBlock replaces the managed block in an instructions file with body, or
// appends a block when there is none. Everything outside the markers is kept
// byte for byte, and the file's own line endings are used for the block.
func WriteBlock(path, body string) error {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the instructions path is the user's own project file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := spliceBlock(string(raw), body)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(out), 0o644) //nolint:gosec // G306: instructions files are meant to be read by the agent and the user
}

func spliceBlock(text, body string) (string, error) {
	for _, l := range strings.Split(body, "\n") {
		if t := strings.TrimRight(l, "\r"); t == blockBegin || t == blockEnd {
			return "", errors.New("body contains a hive marker line")
		}
	}
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	bodyText := ""
	if body != "" {
		bodyText = strings.ReplaceAll(strings.TrimSuffix(body, "\n"), "\n", eol) + eol
	}
	lines := strings.SplitAfter(text, "\n")
	begin, end, err := markerLines(lines)
	if err != nil {
		return "", err
	}
	if begin < 0 {
		block := blockBegin + eol + bodyText + blockEnd + eol
		if text == "" {
			return block, nil
		}
		if !strings.HasSuffix(text, "\n") {
			text += eol
		}
		return text + eol + block, nil
	}
	return strings.Join(lines[:begin+1], "") + bodyText + strings.Join(lines[end:], ""), nil
}

// markerLines finds the begin and end marker lines. Both are -1 when absent.
func markerLines(lines []string) (begin, end int, err error) {
	begin, end = -1, -1
	for i, l := range lines {
		switch strings.TrimRight(l, "\r\n") {
		case blockBegin:
			if begin >= 0 {
				return 0, 0, errors.New("two hive:begin markers")
			}
			begin = i
		case blockEnd:
			if end >= 0 {
				return 0, 0, errors.New("two hive:end markers")
			}
			end = i
		}
	}
	switch {
	case begin < 0 && end >= 0:
		return 0, 0, errors.New("hive:end without hive:begin")
	case begin >= 0 && end < 0:
		return 0, 0, errors.New("hive:begin without hive:end")
	case end < begin:
		return 0, 0, errors.New("hive:end before hive:begin")
	}
	return begin, end, nil
}
