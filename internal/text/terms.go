// Package text holds the tokenizer shared by extraction and recall, so both
// agree on what a term is.
package text

import (
	"regexp"
	"strings"
	"unicode"
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
	for _, g := range Groups(s) {
		for _, t := range g {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// Groups returns the terms of each word of s, one group per word. A CJK run
// is one word, so 如何部署 is one group holding 如何, 何部 and 部署.
func Groups(s string) [][]string {
	var out [][]string
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		var g []string
		for _, piece := range splitCJK(w) {
			if !stopWords[piece] && (isCJK(piece) || longEnough(piece)) {
				g = append(g, piece)
			}
		}
		if len(g) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// splitCJK breaks a word into its non-CJK parts and, for each run of CJK
// characters, overlapping bigrams. A one-character CJK run is kept whole, so
// unspaced text like 如何部署 yields 部署.
func splitCJK(w string) []string {
	var out []string
	var run []rune
	flush := func() {
		switch {
		case len(run) == 0:
		case len(run) == 1:
			out = append(out, string(run))
		default:
			for i := 0; i+1 < len(run); i++ {
				out = append(out, string(run[i:i+2]))
			}
		}
		run = run[:0]
	}
	var plain []rune
	for _, r := range w {
		if isCJKRune(r) {
			if len(plain) > 0 {
				out = append(out, string(plain))
				plain = plain[:0]
			}
			run = append(run, r)
			continue
		}
		flush()
		plain = append(plain, r)
	}
	flush()
	if len(plain) > 0 {
		out = append(out, string(plain))
	}
	return out
}

func isCJK(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return isCJKRune(r)
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
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
