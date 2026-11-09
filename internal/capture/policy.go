package capture

import (
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive/model"
)

const (
	maxPromptRunes = 2000
	maxErrorRunes  = 500
	maxErrorLines  = 2
	maxFieldRunes  = 4000
)

// keptKeys is the payload whitelist: paths, commands, outcomes, short text.
// Anything else (file contents, diffs, tool output) is dropped.
var keptKeys = map[string]bool{
	"command": true, "exit_code": true, "path": true, "paths": true, "skill": true,
	"error": true, "rule": true, "text": true, "type": true, "scope": true,
}

// ApplyPolicy enforces Hive's storage limits on e in place.
func ApplyPolicy(e *model.Event) {
	e.PromptText = truncRunes(e.PromptText, maxPromptRunes)
	if e.Payload == nil {
		return
	}
	if p, ok := e.Payload["path"].(string); ok && secretLikeFile(p) {
		e.Payload = map[string]any{"path": p}
		return
	}
	for k := range e.Payload {
		if !keptKeys[k] {
			delete(e.Payload, k)
		}
	}
	if s, ok := e.Payload["error"].(string); ok {
		e.Payload["error"] = errorSummary(s)
	}
	for k, v := range e.Payload {
		if s, ok := v.(string); ok {
			e.Payload[k] = truncRunes(s, maxFieldRunes)
		}
	}
	if s, ok := e.Payload["text"].(string); ok {
		e.Payload["text"] = truncRunes(s, maxPromptRunes)
	}
}

func errorSummary(s string) string {
	lines := strings.SplitN(s, "\n", maxErrorLines+1)
	if len(lines) > maxErrorLines {
		lines = lines[:maxErrorLines]
	}
	return truncRunes(strings.Join(lines, "\n"), maxErrorRunes)
}

func truncRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func secretLikeFile(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	return strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".pem") ||
		strings.HasPrefix(base, "id_") || strings.Contains(base, "credentials")
}
