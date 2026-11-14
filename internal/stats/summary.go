// Package stats formats the injection analytics shown to the user.
package stats

import (
	"fmt"

	"github.com/a6n-ai/inscribe/internal/store"
)

// minAgentRows is the fewest agent answers worth quoting a rate for.
const minAgentRows = 10

// FormatTokens prints n as an integer below 1000 and as one-decimal k above.
func FormatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// SessionLine is the one-line session-start summary of the last week.
func SessionLine(c store.SessionLineCounts) string {
	if c.Sessions == 0 {
		return "no finished sessions in the last 7d"
	}
	shown := "no prompt lines shown"
	if c.Shown > 0 {
		shown = fmt.Sprintf("%d/%d prompt lines hit", c.Hits, c.Shown)
	}
	s := fmt.Sprintf("last 7d: %s, %s tok injected, %d next-try fixes", shown, FormatTokens(c.InjectedTokens), c.NextTryFixes)
	if c.AgentAnswered >= minAgentRows {
		s += fmt.Sprintf(" · agent rated %d%% helpful", (c.AgentHelpful*100+c.AgentAnswered/2)/c.AgentAnswered)
	}
	return s
}
