package trajectory

import (
	"encoding/json"
	"strings"
)

// SearchText is the part of a stored trajectory record that recall may match.
// It is the same JSON Normalize writes: user content, meta source and cwd,
// the tool name, and the command, path, and skill kept in the tool arguments.
// Timestamps and call ids are left out.
func SearchText(record []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(record, &raw); err != nil {
		return ""
	}
	var b strings.Builder
	writeField(&b, raw["content"])
	writeField(&b, raw["source"])
	writeField(&b, raw["cwd"])
	calls, _ := raw["tool_calls"].([]any)
	for _, c := range calls {
		call, _ := c.(map[string]any)
		if call == nil {
			continue
		}
		writeField(&b, call["name"])
		args, _ := call["args"].(string)
		if args == "" {
			continue
		}
		var inner map[string]any
		if err := json.Unmarshal([]byte(args), &inner); err != nil {
			writeField(&b, args)
			continue
		}
		writeField(&b, inner["command"])
		writeField(&b, inner["path"])
		writeField(&b, inner["skill"])
		writeField(&b, inner["paths"])
	}
	return b.String()
}

func writeField(b *strings.Builder, v any) {
	switch x := v.(type) {
	case string:
		if x == "" {
			return
		}
		b.WriteString(x)
		b.WriteByte(' ')
	case []any:
		for _, item := range x {
			writeField(b, item)
		}
	}
}
