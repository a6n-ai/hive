package recall

import (
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("abcde"); got != 2 {
		t.Fatalf("EstimateTokens(abcde) = %d, want 2", got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("EstimateTokens() = %d, want 0", got)
	}
}

func TestRenderSkipsHoldout(t *testing.T) {
	if got := Render([]Line{{Type: "holdout"}}); got != "" {
		t.Fatalf("Render(holdout) = %q, want empty", got)
	}
	got := Render([]Line{{Type: model.Note, Text: "a"}, {Type: "holdout"}, {Type: model.Note, Text: "b"}})
	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("Render = %q, want exactly one newline", got)
	}
}
