package scrub

import "strings"

// redactAssignments replaces the value of every secret assignment in s.
func redactAssignments(s string, findings *[]Finding) string {
	locs := assignKey.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		start := loc[1]
		if start < last || strings.HasPrefix(s[start:], redactedPrefix) {
			continue // inside the previous value, or already redacted
		}
		end := valueEnd(s, start, loc[3] > loc[2])
		if end == start {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(tag("assignment"))
		*findings = append(*findings, Finding{"assignment", start, end})
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// valueEnd returns the end of the value starting at i. After a quoted key
// (JSON, YAML) an unquoted value also stops at , } ].
func valueEnd(s string, i int, quotedKey bool) int {
	if i >= len(s) {
		return i
	}
	if q := s[i]; q == '"' || q == '\'' {
		return quotedEnd(s, i, q)
	}
	j := i
	for j < len(s) && !valueStop(s[j], quotedKey) {
		j++
	}
	return j
}

func valueStop(c byte, quotedKey bool) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'':
		return true
	case ',', '}', ']':
		return quotedKey
	}
	return false
}

// maxValueSpan bounds how far a quoted value is scanned for its closing quote,
// keeping inputs with many unclosed values linear.
const maxValueSpan = 64 * 1024

// quotedEnd scans a quoted value opening at i. Escapes are honored. The value
// may continue onto the next line unless that line starts any assignment or
// contains a secret key; an unclosed value ends at the end of its first line.
func quotedEnd(s string, i int, q byte) int {
	firstEOL := -1
	limit := min(len(s), i+maxValueSpan)
	for j := i + 1; j < limit; j++ {
		switch s[j] {
		case '\\':
			j++ // skip the escaped byte
		case q:
			return j + 1
		case '\n':
			if firstEOL < 0 {
				firstEOL = trimCR(s, i, j)
			}
			if line := nextLine(s, j+1); assignmentStart.MatchString(line) || assignKey.MatchString(line) {
				return trimCR(s, i, j)
			}
		}
	}
	if firstEOL >= 0 {
		return firstEOL
	}
	return limit
}

// trimCR returns j, or j-1 when the byte before j is a '\r' after start.
func trimCR(s string, start, j int) int {
	if j > start+1 && s[j-1] == '\r' {
		return j - 1
	}
	return j
}

func nextLine(s string, i int) string {
	if k := strings.IndexByte(s[i:], '\n'); k >= 0 {
		return s[i : i+k]
	}
	return s[i:]
}
