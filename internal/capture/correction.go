package capture

import (
	"regexp"
	"strings"
)

var (
	reUseNot     = regexp.MustCompile(`\buse\s+([^\s,.;!]+)\s*,?\s+(?:not|rather than)\s+([^\s,.;!]+)`)
	reUseInstead = regexp.MustCompile(`\buse\s+([^\s,.;!]+)\s+instead\s+of\s+([^\s,.;!]+)`)
	reRedirect   = regexp.MustCompile(`^(?:no|nope|don't|do not|stop|wrong|not that)\b`)
)

// correctionRule returns the normalized rule a correcting prompt states, or ""
// when the prompt is not a correction.
func correctionRule(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if m := reUseInstead.FindStringSubmatch(lower); m != nil {
		return "use " + m[1] + " not " + m[2]
	}
	if m := reUseNot.FindStringSubmatch(lower); m != nil {
		return "use " + m[1] + " not " + m[2]
	}
	if reRedirect.MatchString(lower) || strings.Contains(lower, "instead") {
		return truncRunes(lower, 80)
	}
	return ""
}
