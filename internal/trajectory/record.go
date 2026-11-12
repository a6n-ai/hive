// Package trajectory turns an Inscribe event into Letta trajectory v1 records.
// Every agent adapter produces the same model.Event. Normalize is the one
// place that turns that event into the shared transcript shape, so Claude
// Code, Codex, and Gemini save identical record kinds.
//
// Extractors do not build these records. They read the event and write typed
// memories. A derived event (command_result, skill_loaded, correction) is
// already represented by its parent, so Normalize returns nothing for it.
package trajectory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/a6n-ai/inscribe/model"
)

// Record is one validated trajectory v1 object ready to store.
type Record struct {
	Role       string
	ToolCallID string
	OK         *bool
	TS         int64 // unix ms; 0 for meta, which has no timestamp
	JSON       []byte
	Digest     string
}

type toolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

type metaRecord struct {
	Role   string `json:"role"`
	Source string `json:"source"`
	Cwd    string `json:"cwd,omitempty"`
}

type userRecord struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

type assistantRecord struct {
	Role      string     `json:"role"`
	Content   *string    `json:"content"`
	Timestamp string     `json:"timestamp"`
	ToolCalls []toolCall `json:"tool_calls"`
}

type toolRecord struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	OK         *bool  `json:"ok,omitempty"`
	Timestamp  string `json:"timestamp"`
}

type observationRecord struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// Normalize returns the trajectory v1 records for e. e.Digest must already be set.
// The JSON keeps only the scrubbed fields Inscribe stores: no file contents.
func Normalize(e *model.Event) []Record {
	if e == nil || e.Digest == "" {
		return nil
	}
	switch e.Kind {
	case model.SessionStart:
		return one(e, "meta", "", nil, metaRecord{Role: "meta", Source: source(e), Cwd: cwd(e)})
	case model.Prompt:
		return one(e, "user", "", nil, userRecord{Role: "user", Content: e.PromptText, Timestamp: stamp(e.TS)})
	case model.Remembered:
		return one(e, "user", "", nil, userRecord{Role: "user", Content: str(e.Payload, "text"), Timestamp: stamp(e.TS)})
	case model.PreTool:
		return assistant(e)
	case model.PostTool:
		return append(assistant(e), toolResult(e)...)
	case model.TurnEnd:
		return one(e, "observation", "", nil, observationRecord{Role: "observation", Content: "turn ended", Timestamp: stamp(e.TS)})
	case model.SessionEnd:
		return one(e, "observation", "", nil, observationRecord{Role: "observation", Content: "session ended", Timestamp: stamp(e.TS)})
	case model.SkillLoaded, model.CommandResult, model.Correction:
		return nil
	default:
		return nil
	}
}

func assistant(e *model.Event) []Record {
	name := e.Tool
	if name == "" {
		name = "tool"
	}
	call := toolCall{ID: callID(e), Name: name, Args: argsJSON(e.Payload)}
	return one(e, "assistant", "", nil, assistantRecord{Role: "assistant", Timestamp: stamp(e.TS), ToolCalls: []toolCall{call}})
}

func toolResult(e *model.Event) []Record {
	ok := outcome(e.Payload)
	return one(e, "tool", callID(e), ok, toolRecord{
		Role: "tool", ToolCallID: callID(e), Content: str(e.Payload, "error"), OK: ok, Timestamp: stamp(e.TS),
	})
}

func one(e *model.Event, role, call string, ok *bool, v any) []Record {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	ts := e.TS
	if role == "meta" {
		ts = 0
	}
	return []Record{{Role: role, ToolCallID: call, OK: ok, TS: ts, JSON: b, Digest: recordDigest(e.Digest, role)}}
}

func source(e *model.Event) string {
	if e.Agent != "" {
		return e.Agent
	}
	return "unknown"
}

func cwd(e *model.Event) string {
	if e.Cwd != "" {
		return e.Cwd
	}
	return e.Project
}

func callID(e *model.Event) string {
	d := e.Digest
	if len(d) > 16 {
		d = d[:16]
	}
	return "call_" + d
}

func recordDigest(eventDigest, role string) string {
	sum := sha256.Sum256([]byte(eventDigest + "|" + role))
	return hex.EncodeToString(sum[:])
}

func stamp(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// argsJSON is a JSON object of the arguments Inscribe keeps. Outcomes stay on
// the tool record, and file contents never enter the arguments.
func argsJSON(p map[string]any) string {
	obj := map[string]any{}
	for _, k := range []string{"command", "path", "skill"} {
		if s := str(p, k); s != "" {
			obj[k] = s
		}
	}
	if paths, ok := stringList(p["paths"]); ok && len(paths) > 0 {
		obj["paths"] = paths
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func outcome(p map[string]any) *bool {
	if code, ok := intVal(p, "exit_code"); ok {
		b := code == 0
		return &b
	}
	if str(p, "error") != "" {
		b := false
		return &b
	}
	return nil
}

func str(p map[string]any, k string) string {
	if p == nil {
		return ""
	}
	s, _ := p[k].(string)
	return s
}

func intVal(p map[string]any, k string) (int, bool) {
	if p == nil {
		return 0, false
	}
	switch n := p[k].(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func stringList(v any) ([]string, bool) {
	switch xs := v.(type) {
	case []string:
		return xs, true
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			s, ok := x.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}
