package agent

import (
	"encoding/json"
	"testing"
)

func TestAddContext(t *testing.T) {
	ctxOf := func(out []byte) map[string]any {
		t.Helper()
		var reply map[string]any
		if err := json.Unmarshal(out, &reply); err != nil {
			t.Fatalf("reply %q: %v", out, err)
		}
		hso, _ := reply["hookSpecificOutput"].(map[string]any)
		return hso
	}

	created := ctxOf(AddContext(nil, "PostToolUse", "ask"))
	if created["hookEventName"] != "PostToolUse" || created["additionalContext"] != "ask" {
		t.Fatalf("created = %v", created)
	}

	out := AddContext([]byte(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"memories"},"systemMessage":"status"}`), "PostToolUse", "ask")
	hso := ctxOf(out)
	if hso["additionalContext"] != "memories\n\nask" {
		t.Fatalf("additionalContext = %q", hso["additionalContext"])
	}
	var reply map[string]any
	if err := json.Unmarshal(out, &reply); err != nil {
		t.Fatal(err)
	}
	if reply["systemMessage"] != "status" {
		t.Fatalf("systemMessage = %v", reply["systemMessage"])
	}
}
