// Package text holds the tokenizer shared by extraction and recall, so both
// agree on what a term is.
package text

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "this": true, "that": true, "with": true, "from": true,
	"you": true, "can": true, "please": true, "into": true, "are": true, "was": true, "but": true,
	"not": true, "all": true, "have": true, "use": true, "our": true, "now": true, "then": true,
	"how": true, "what": true, "why": true, "when": true, "where": true, "which": true, "who": true,
	"does": true, "did": true, "its": true, "any": true, "some": true,
}

// Terms returns the distinct lowercase content words of s, in order. ASCII
// words need 3+ characters; other scripts (CJK and so on) need 2+ runes.
func Terms(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if stopWords[w] || seen[w] || !longEnough(w) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func longEnough(w string) bool {
	if len(w) == utf8.RuneCountInString(w) { // ASCII
		return len(w) >= 3
	}
	return utf8.RuneCountInString(w) >= 2
}

// Matches reports whether two terms match as prefixes of each other, the way
// FTS prefix queries do.
func Matches(a, b string) bool { return strings.HasPrefix(a, b) || strings.HasPrefix(b, a) }
