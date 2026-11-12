package recall

import (
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

func TestPromptScoreLeaves(t *testing.T) {
	m := model.Memory{
		Text: "login bug fixed in auth.ts", Confidence: 0.9,
		ScopeKind: model.ProjectScope, LastSeen: now.UnixMilli(),
	}
	none := promptScore(text.Groups("what time is it"), m, now, 30, nil)
	partial := promptScore(text.Groups("login deploy database"), m, now, 30, nil)
	swapped := promptScore(text.Groups("bug login"), m, now, 30, nil)
	phrase := promptScore(text.Groups("login bug"), m, now, 30, nil)
	if none != 0 {
		t.Fatalf("unrelated score = %v, want 0", none)
	}
	if !(phrase > swapped && swapped > partial) {
		t.Fatalf("phrase %v, swapped %v, partial %v; want phrase > swapped > partial", phrase, swapped, partial)
	}
}

func TestPromptScoreIgnoresAStaleUnrelatedMemory(t *testing.T) {
	old := now.Add(-120 * 24 * time.Hour).UnixMilli()
	m := model.Memory{
		Text: "deploy with make", Confidence: 0.5,
		ScopeKind: model.Global, LastSeen: old,
	}
	if s := promptScore(text.Groups("fix the login bug"), m, now, 30, nil); s != 0 {
		t.Fatalf("score = %v, want 0", s)
	}
}
