package trajectory

import (
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func TestSearchTextReadsTheStoredJSON(t *testing.T) {
	e := &model.Event{
		Digest: "abcdef0123456789", TS: 1_800_000_000_000, Agent: "codex", Project: "/p",
		Kind: model.PostTool, Tool: "Bash",
		Payload: map[string]any{"command": "npm run lint", "path": "auth.ts", "exit_code": 0},
	}
	recs := Normalize(e)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want assistant and tool", len(recs))
	}
	got := SearchText(recs[0].JSON)
	for _, want := range []string{"Bash", "npm run lint", "auth.ts"} {
		if !strings.Contains(got, want) {
			t.Fatalf("assistant text %q lacks %q", got, want)
		}
	}

	meta := &model.Event{Digest: "abcdef0123456789", Agent: "claude-code", Cwd: "/repo", Kind: model.SessionStart}
	mrecs := Normalize(meta)
	if len(mrecs) != 1 {
		t.Fatalf("meta records = %d", len(mrecs))
	}
	got = SearchText(mrecs[0].JSON)
	if !strings.Contains(got, "claude-code") || !strings.Contains(got, "/repo") {
		t.Fatalf("meta text = %q", got)
	}
}
