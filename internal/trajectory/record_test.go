package trajectory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func TestNormalizeAllAgentsShareTheRecordShape(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex", "gemini"} {
		e := &model.Event{Agent: agent, Session: "s", Project: "/repo", Cwd: "/repo/sub", Kind: model.SessionStart, TS: 1_700_000_000_000, Digest: strings.Repeat("a", 64)}
		recs := Normalize(e)
		if len(recs) != 1 || recs[0].Role != "meta" {
			t.Fatalf("%s: got %#v", agent, recs)
		}
		var meta map[string]any
		if err := json.Unmarshal(recs[0].JSON, &meta); err != nil {
			t.Fatal(err)
		}
		if meta["role"] != "meta" || meta["source"] != agent || meta["cwd"] != "/repo/sub" {
			t.Fatalf("%s meta = %s", agent, recs[0].JSON)
		}
		if _, ok := meta["timestamp"]; ok {
			t.Fatalf("%s meta must not carry a timestamp", agent)
		}
	}
}

func TestNormalizePostToolLinksCallAndResult(t *testing.T) {
	e := &model.Event{
		Agent: "claude-code", Kind: model.PostTool, Tool: "shell", TS: 1_700_000_000_000, Digest: strings.Repeat("b", 64),
		Payload: map[string]any{"command": "go test", "exit_code": 1, "error": "fail"},
	}
	recs := Normalize(e)
	if len(recs) != 2 || recs[0].Role != "assistant" || recs[1].Role != "tool" {
		t.Fatalf("roles = %s %s", recs[0].Role, recs[1].Role)
	}
	if recs[1].ToolCallID != "call_"+e.Digest[:16] || recs[0].JSON == nil {
		t.Fatalf("call id %s", recs[1].ToolCallID)
	}
	var assistant map[string]any
	if err := json.Unmarshal(recs[0].JSON, &assistant); err != nil {
		t.Fatal(err)
	}
	if assistant["content"] != nil {
		t.Fatalf("assistant content = %v, want null", assistant["content"])
	}
	calls := assistant["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	if call["id"] != recs[1].ToolCallID || call["name"] != "shell" || call["args"] != `{"command":"go test"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if recs[1].OK == nil || *recs[1].OK {
		t.Fatal("expected ok false")
	}
	if !strings.Contains(string(recs[1].JSON), `"content":"fail"`) {
		t.Fatalf("tool record = %s", recs[1].JSON)
	}
}

func TestNormalizeSkipsDerivedEvents(t *testing.T) {
	for _, kind := range []model.EventKind{model.CommandResult, model.SkillLoaded, model.Correction} {
		if recs := Normalize(&model.Event{Kind: kind, Digest: "abc"}); len(recs) != 0 {
			t.Fatalf("%s produced %d records", kind, len(recs))
		}
	}
}
