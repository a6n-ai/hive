// Package scrub removes secrets from text before Hive stores it.
package scrub

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

type Finding struct {
	Kind       string
	Start, End int
}

type Scrubber struct {
	rules []rule
}

// New builds a scrubber with the built-in rules plus user regexes, which are
// redacted as kind "custom".
func New(extra []string) (*Scrubber, error) {
	rules := append([]rule{}, builtin...)
	for _, p := range extra {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("scrub pattern %q: %w", p, err)
		}
		rules = append(rules, rule{"custom", re, 0})
	}
	return &Scrubber{rules: rules}, nil
}

const redactedPrefix = "[REDACTED:"

func tag(kind string) string { return redactedPrefix + kind + "]" }

// Scrub returns s with every detected secret replaced by [REDACTED:<kind>].
func (sc *Scrubber) Scrub(s string) (string, []Finding) {
	var findings []Finding
	for _, r := range sc.rules {
		s = replace(s, r, &findings)
	}
	s = redactAssignments(s, &findings)
	s = scrubEntropy(s, &findings)
	return s, findings
}

func replace(s string, r rule, findings *[]Finding) string {
	locs := r.re.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		start, end := loc[2*r.group], loc[2*r.group+1]
		if start < 0 || strings.HasPrefix(s[start:end], redactedPrefix) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(tag(r.kind))
		*findings = append(*findings, Finding{r.kind, start, end})
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func scrubEntropy(s string, findings *[]Finding) string {
	locs := entropyCandidate.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		tok := s[loc[0]:loc[1]]
		// Only a bounded window before the token is checked, so large inputs
		// with many candidates stay linear.
		if !looksSecret(tok) || dataURIPrefix.MatchString(s[max(0, loc[0]-128):loc[0]]) {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(tag("high_entropy"))
		*findings = append(*findings, Finding{"high_entropy", loc[0], loc[1]})
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func looksSecret(tok string) bool {
	hexish, letter, digit := true, false, false
	for _, c := range tok {
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
			letter = true
		case c == '-':
		default:
			hexish = false
			letter = letter || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		}
	}
	// Hex (git SHAs, hashes, UUIDs) is never redacted by entropy alone.
	return !hexish && letter && digit && entropy(tok) >= 4.0
}

func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, c := range s {
		counts[c]++
		n++
	}
	h := 0.0
	for _, k := range counts {
		p := float64(k) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// ScrubAny returns a copy of v with every string inside maps and slices
// scrubbed. Other values are returned unchanged.
func (sc *Scrubber) ScrubAny(v any) any {
	switch x := v.(type) {
	case string:
		out, _ := sc.Scrub(x)
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = sc.ScrubAny(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = sc.ScrubAny(val)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, val := range x {
			out[i], _ = sc.Scrub(val)
		}
		return out
	default:
		return v
	}
}
