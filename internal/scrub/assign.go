package scrub

import "strings"

// maxValueSpan bounds how far a quoted value is scanned for its closing quote,
// keeping inputs with many unclosed values linear.
const maxValueSpan = 64 * 1024

// knownKinds is every kind the scrubber emits; only these tags are trusted.
var knownKinds = func() map[string]bool {
	m := map[string]bool{"assignment": true, "high_entropy": true, "custom": true}
	for _, r := range builtin {
		m[r.kind] = true
	}
	return m
}()

// isRedaction reports whether the value spanning s[start:end] is just a tag the
// scrubber itself wrote at start. A rescrub must not count it again, or a
// prepared event scrubbed twice reports extra secrets. It counts only when the
// tag is the whole value.
func isRedaction(s string, start, end int) bool {
	rest, ok := strings.CutPrefix(s[start:], redactedPrefix)
	if !ok {
		return false
	}
	kind, _, ok := strings.Cut(rest, "]")
	if !ok || !knownKinds[kind] {
		return false
	}
	return end == start+len(redactedPrefix)+len(kind)+1
}

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
		end := valueEnd(s, start, loc[3] > loc[2], sepByte(s, loc[1]) == '=')
		if end <= start || isRedaction(s, start, end) && yamlContinuation(s, loc[0], loc[1], start, end) == end {
			continue
		}
		switch {
		case spanStart >= 0 && start <= spanEnd:
			// The current span already covers this value's YAML block.
			spanEnd = max(spanEnd, end)
		case start < last:
			// Inside text already written; nothing new to cover.
		default:
			end = yamlContinuation(s, loc[0], loc[1], start, end)
			flush()
			spanStart, spanEnd = start, end
		}
	}
	flush()
	b.WriteString(s[last:])
	return b.String()
}

// sepByte returns the ':' or '=' that ends the key match at sepEnd.
func sepByte(s string, sepEnd int) byte {
	i := sepEnd - 1
	for i > 0 && (s[i] == ' ' || s[i] == '\t') {
		i--
	}
	return s[i]
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

// yamlContinuation extends a YAML value over the lines that belong to it: when
// the value is a block-scalar indicator (| or > with modifiers) or started on
// the line after its key, every following line indented deeper than the key
// is part of the value. Other values are returned unchanged.
func yamlContinuation(s string, keyStart, sepEnd, start, end int) int {
	nextLineValue := start > sepEnd && strings.ContainsRune(s[sepEnd:start], '\n')
	from := end
	plain := false
	if !nextLineValue {
		header, lineEnd := headerAt(s, start)
		if blockIndicator.MatchString(header) {
			from = lineEnd
		} else if q := s[start]; (q != '"' && q != '\'' || closedQuote(s, start, q)) && sepByte(s, sepEnd) == ':' && !isQuotedKey(s, keyStart, sepEnd) {
			plain = true // same-line plain scalar: deeper lines that are not keys continue it
		} else {
			return end
		}
	}
	keyIndent := lineIndent(s, lineStart(s, keyStart))
	for j := from; j < len(s) && j-end <= maxValueSpan; {
		k := j
		if s[k] == '\r' {
			k++
		}
		if k >= len(s) || s[k] != '\n' {
			return end
		}
		ls := k + 1
		le := ls + len(nextLine(s, ls))
		line := strings.TrimRight(s[ls:le], "\r")
		if strings.TrimSpace(line) != "" && (lineIndent(s, ls) <= keyIndent || plain && assignmentStart.MatchString(line)) {
			return end
		}
		if strings.TrimSpace(line) != "" {
			end = ls + len(line)
		}
		j = le
	}
	return end
}

// maxHeaderSpan bounds the YAML node header read after a key. shortcut: a
// header longer than this (a very long tag or comment) is not recognised as a
// block scalar, so its body is not redacted; raise it if that is seen in practice.
const maxHeaderSpan = 256

// headerAt reads the YAML node header at start: the rest of its line without
// tags, anchors and a trailing comment. It returns the header and where the
// line ends.
func headerAt(s string, start int) (string, int) {
	end := min(len(s), start+maxHeaderSpan)
	if k := strings.IndexByte(s[start:end], '\n'); k >= 0 {
		end = start + k
	}
	var fields []string
	for _, f := range strings.Fields(s[start:end]) {
		if f[0] == '#' {
			break
		}
		fields = append(fields, f)
	}
	for len(fields) > 0 && (fields[0][0] == '!' || fields[0][0] == '&') {
		fields = fields[1:]
	}
	return strings.Join(fields, " "), end
}

// isQuotedKey reports whether the key ending at sepEnd closes with a quote.
func isQuotedKey(s string, keyStart, sepEnd int) bool {
	m := assignKey.FindStringSubmatchIndex(s[keyStart:sepEnd])
	return m != nil && m[3] > m[2]
}

func lineStart(s string, i int) int { return strings.LastIndexByte(s[:i], '\n') + 1 }

func lineIndent(s string, ls int) int {
	n := 0
	for ls+n < len(s) && (s[ls+n] == ' ' || s[ls+n] == '\t') {
		n++
	}
	return n
}

// valueEnd returns the end of the value starting at i. A quoted value ends at
// its closing quote. After a quoted key (JSON) an unquoted value ends at , }
// or a line break. Otherwise it runs to the end of the line, except that a
// key=value shell assignment also stops before &&, ;, | or a following KEY=, but only when whitespace precedes the separator (or follows ;).
func valueEnd(s string, i int, quotedKey, shell bool) int {
	if i >= len(s) {
		return i
	}
	if q := s[i]; q == '"' || q == '\'' {
		e := quotedEnd(s, i, q)
		if quotedKey || s[e-1] != q {
			return e
		}
		// Text after a closed quote is redacted too, so a rescrub is stable;
		// shell values still end at their separators.
		i = e - 1
		j := e
		for j < len(s) && !valueStop(s, i, j, false, shell) {
			j++
		}
		for j > e && isSpace(s[j-1]) {
			j--
		}
		return j
	}
	j := i
	for j < len(s) && !valueStop(s, i, j, quotedKey, shell) {
		j++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return j
}

func closedQuote(s string, i int, q byte) bool {
	e := quotedEnd(s, i, q)
	return e-i > 1 && s[e-1] == q
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

func valueStop(s string, i, j int, quotedKey, shell bool) bool {
	switch c := s[j]; {
	case c == '\n' || c == '\r':
		return true
	case quotedKey:
		return c == ',' || c == '}'
	case !shell:
		return false
	case c == ';' && j > i:
		return isSpace(s[j-1]) || j+1 >= len(s) || isSpace(s[j+1]) || s[j+1] == '\n' || s[j+1] == '\r'
	case c == '|' && j > i, c == '&' && j > i && j+1 < len(s) && s[j+1] == '&':
		return isSpace(s[j-1])
	case c == ' ' || c == '\t':
		return shellAssign.MatchString(s[j:min(len(s), j+maxHeaderSpan)])
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
