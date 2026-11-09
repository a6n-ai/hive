package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	sigPathLoc = regexp.MustCompile(`(?:[A-Za-z]:)?(?:[\\/][^\s:()'"]+)+(?::\d+)*`)
	sigQuoted  = regexp.MustCompile(`'[^']*'|"[^"]*"|` + "`[^`]*`")
	sigNumber  = regexp.MustCompile(`\d+`)
	sigSpace   = regexp.MustCompile(`\s+`)
)

// normalizeError strips the parts of an error line that change between runs:
// paths with line and column, quoted values, and numbers.
func normalizeError(line string) string {
	s := sigPathLoc.ReplaceAllString(line, "<path>")
	s = sigQuoted.ReplaceAllString(s, "<q>")
	s = sigNumber.ReplaceAllString(s, "<n>")
	return strings.TrimSpace(sigSpace.ReplaceAllString(s, " "))
}

// Signature is the hex SHA-256 of an error line with run-specific parts
// removed, so the same error matches across runs and files.
func Signature(errLine string) string {
	h := sha256.Sum256([]byte(normalizeError(firstLine(errLine))))
	return hex.EncodeToString(h[:])
}
