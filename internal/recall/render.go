package recall

import "strings"

func renderLine(l Line) string { return "[" + string(l.Type) + "] " + l.Text }

// Render formats lines for injection, one per line.
func Render(lines []Line) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = renderLine(l)
	}
	return strings.Join(out, "\n")
}

// estimateTokens approximates model tokens as characters / 4.
func estimateTokens(s string) int { return (len(s) + 3) / 4 }
