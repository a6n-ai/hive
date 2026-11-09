package extract

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive/model"
)

// lookback bounds how many earlier session events an extractor reads.
const lookback = 200

// str returns p[k] as a string, or "".
func str(p map[string]any, k string) string {
	s, _ := p[k].(string)
	return s
}

// intVal returns p[k] as an int. Payloads decoded from JSON hold float64.
func intVal(p map[string]any, k string) (int, bool) {
	switch v := p[k].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	}
	return 0, false
}

// relPath returns path relative to project when it is inside it.
func relPath(project, path string) string {
	if r, err := filepath.Rel(project, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

// firstLine returns the first line of s, trimmed.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// projectMemory builds a project-scoped memory seen at e's time.
func projectMemory(e *model.Event, t model.MemoryType, key, text string, body map[string]any, status model.Status) *model.Memory {
	if body == nil {
		body = map[string]any{}
	}
	return &model.Memory{Type: t, ScopeKind: model.ProjectScope, Scope: e.Project, Key: key, Body: body, Text: text,
		Confidence: 0.5, Status: status, FirstSeen: e.TS, LastSeen: e.TS}
}

// isEdit reports whether e is a file-changing tool call, and its path.
func isEdit(e *model.Event) (string, bool) {
	if e.Kind != model.PostTool || (e.Tool != "edit" && e.Tool != "write") {
		return "", false
	}
	p := str(e.Payload, "path")
	return p, p != ""
}

// Defaults returns the built-in extractors. callerIsScout turns skill memory
// on when skills.enabled is "auto".
func Defaults(callerIsScout bool) []Extractor {
	return []Extractor{NewProject(), NewCommand(), NewFix(), NewCochange(), NewNote(), NewPreference(), NewSkill(callerIsScout)}
}
