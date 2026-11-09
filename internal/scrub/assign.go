package scrub

import "strings"

// maxValueSpan bounds how far a quoted value is scanned for its closing quote,
// keeping inputs with many unclosed values linear.
const maxValueSpan = 64 * 1024

// redactAssignments replaces the value of every secret assignment in s.
// Overlapping values merge into one redaction, so an assignment that starts
// inside an earlier value is still covered to its own end.
func redactAssignments(s string, findings *[]Finding) string {
	locs := assignKey.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last, spanStart, spanEnd := 0, -1, -1
	flush := func() {
		if spanStart < 0 {
			return
		}
		b.WriteString(s[last:spanStart])
		b.WriteString(tag("assignment"))
		*findings = append(*findings, Finding{"assignment", spanStart, spanEnd})
		last = spanEnd
		spanStart, spanEnd = -1, -1
	}
	for _, loc := range locs {
		start := valueStart(s, loc[1])
		end := valueEnd(s, start, loc[3] > loc[2])
		if end <= start {
			continue
		}
		switch {
		case spanStart >= 0 && start <= spanEnd:
			spanEnd = max(spanEnd, end)
		case start < last:
			// Inside text already written; nothing new to cover.
		default:
			flush()
			spanStart, spanEnd = start, end
		}
	}
	flush()
	b.WriteString(s[last:])
	return b.String()
}

// valueStart returns where a value begins after the separator at i. A YAML
// value may sit on the next, indented line when the key ends its line.
func valueStart(s string, i int) int {
	j := i
	if j < len(s) && s[j] == '\r' {
		j++
	}
	if j >= len(s) || s[j] != '\n' {
		return i
	}
	k := j + 1
	for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
		k++
	}
	if k == j+1 || k >= len(s) || s[k] == '\n' || s[k] == '\r' {
		return i // not indented, or a blank line: no value here
	}
	return k
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

// quotedEnd scans a quoted value opening at i. Backslash escapes are honored
// but never skip a line break. The value may continue onto the next line
// unless that line starts any assignment or contains a secret key; an unclosed
// value ends at the end of its first line.
func quotedEnd(s string, i int, q byte) int {
	firstEOL := -1
	limit := min(len(s), i+maxValueSpan)
	for j := i + 1; j < limit; j++ {
		switch s[j] {
		case '\\':
			if j+1 < limit && s[j+1] != '\n' && s[j+1] != '\r' {
				j++ // skip the escaped byte
			}
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
