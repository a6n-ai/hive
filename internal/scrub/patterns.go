package scrub

import "regexp"

type rule struct {
	kind string
	re   *regexp.Regexp
	// group is the submatch to redact; 0 redacts the whole match.
	group int
}

// Order matters: specific token formats first, then credentials in URLs and
// assignments; high-entropy strings are checked last, in scrub.go.
var builtin = []rule{
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?s:.*?)-----END [A-Z ]*PRIVATE KEY-----`), 0},
	{"anthropic_key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`), 0},
	{"openai_key", regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_\-]{20,}`), 0},
	{"github_token", regexp.MustCompile(`\b(?:ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]{30,}`), 0},
	{"github_token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`), 0},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"slack_token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`), 0},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`), 0},
	{"stripe_key", regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{20,}`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), 0},
	// Greedy up to the last '@' before the host, so a password containing '@'
	// is redacted whole.
	{"url_credentials", regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://([^\s:/@]+:[^\s/]*)@[^\s@/]+`), 1},
}

// Assignments (password=..., "api_key": "...") are found by assignKey and
// their values measured by the scanner in assign.go, not by a regex: quoted
// values that span lines cannot be bounded correctly by a regular expression.
const secretKeys = `(?:password|passwd|secret|token|api[_\-]?key|auth|credential)`

// assignKey matches a secret key and its separator; group 1 is a quote that
// closes a quoted key (JSON, YAML). The value starts at the end of the match.
var assignKey = regexp.MustCompile(`(?i)\b` + secretKeys + `s?\b(["']?)[ \t]*[:=][ \t]*`)

// assignmentStart matches a line that begins any key: or key= assignment.
var assignmentStart = regexp.MustCompile(`^[ \t]*["']?[A-Za-z_][\w.\-]*["']?[ \t]*[:=]`)

// entropyCandidate finds runs that may be random secrets.
var entropyCandidate = regexp.MustCompile(`[A-Za-z0-9_=+\-]{32,}`)

// dataURIPrefix matches a media data URI right before a base64 payload; only
// those payloads skip the entropy rule.
var dataURIPrefix = regexp.MustCompile(`data:[a-zA-Z0-9.+\-]+/[a-zA-Z0-9.+\-]+;base64,$`)
