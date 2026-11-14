package stats

import (
	"testing"

	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
)

func TestSessionLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   store.SessionLineCounts
		want string
	}{
		{"hits", store.SessionLineCounts{Sessions: 5, Hits: 4, Shown: 9, InjectedTokens: 1234, NextTryFixes: 3},
			"last 7d: 4/9 prompt lines hit, 1.2k tok injected, 3 next-try fixes"},
		{"no sessions", store.SessionLineCounts{}, "no finished sessions in the last 7d"},
		{"none shown", store.SessionLineCounts{Sessions: 2},
			"last 7d: no prompt lines shown, 0 tok injected, 0 next-try fixes"},
		{"agent", store.SessionLineCounts{Sessions: 2, Hits: 1, Shown: 2, AgentAnswered: 12, AgentHelpful: 9},
			"last 7d: 1/2 prompt lines hit, 0 tok injected, 0 next-try fixes · agent rated 75% helpful"},
		{"few agent rows", store.SessionLineCounts{Sessions: 2, Hits: 1, Shown: 2, AgentAnswered: 9, AgentHelpful: 9},
			"last 7d: 1/2 prompt lines hit, 0 tok injected, 0 next-try fixes"},
	} {
		got := SessionLine(tc.in)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if n := recall.EstimateTokens(got); n >= 40 {
			t.Errorf("%s: %d tokens, want < 40", tc.name, n)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	for in, want := range map[int]string{0: "0", 950: "950", 999: "999", 1000: "1.0k", 1234: "1.2k"} {
		if got := FormatTokens(in); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}
