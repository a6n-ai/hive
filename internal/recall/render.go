package recall

import "strings"

func renderLine(l Line) string {
	if l.Type == "holdout" {
		return ""
	}
	return "[" + string(l.Type) + "] " + l.Text
}

// Render formats lines for injection, one per line.
func Render(lines []Line) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if s := renderLine(l); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// EstimateTokens approximates model tokens as characters / 4.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }
