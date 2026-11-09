package extract

import (
	"regexp"
	"sort"
	"strings"
)

// taxonomy is checked in order; the first class with a matching keyword wins.
var taxonomy = []struct {
	class string
	re    *regexp.Regexp
}{
	{"debug", regexp.MustCompile(`\b(?:fix|bug|bugs|error|errors|failing|fails|broken|crash|crashes)\b`)},
	{"test", regexp.MustCompile(`\b(?:test|tests|spec|specs|coverage)\b`)},
	{"build", regexp.MustCompile(`\b(?:build|compile|bundle)\b`)},
	{"design", regexp.MustCompile(`\b(?:design|ui|ux|layout|style|styles|page|pages)\b`)},
	{"docs", regexp.MustCompile(`\b(?:doc|docs|readme|explain|document)\b`)},
	{"deploy", regexp.MustCompile(`\b(?:deploy|deployment|release|ship|publish)\b`)},
	{"review", regexp.MustCompile(`\b(?:review|pr|diff)\b`)},
}

// Classify assigns a prompt to one task class by keyword.
func Classify(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, c := range taxonomy {
		if c.re.MatchString(lower) {
			return c.class
		}
	}
	return "other"
}

const maxTerms = 20

// topCounts keeps the n highest counts (ties broken by key for stable output).
func topCounts(m map[string]any, n int) map[string]any {
	if len(m) <= n {
		return m
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := countOf(m[keys[i]]), countOf(m[keys[j]])
		if ci != cj {
			return ci > cj
		}
		return keys[i] < keys[j]
	})
	out := make(map[string]any, n)
	for _, k := range keys[:n] {
		out[k] = m[k]
	}
	return out
}

func countOf(v any) int {
	n, _ := intVal(map[string]any{"v": v}, "v")
	return n
}
