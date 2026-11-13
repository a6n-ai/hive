package scrub

import (
	"strings"
	"testing"
	"time"
)

// Secrets are assembled from pieces so repository secret scanners do not flag
// the test corpus. None of these are real credentials.
func rep(s string, n int) string { return strings.Repeat(s, n) }

var anthropic = "sk-" + "ant-" + "api03-" + rep("Ab9x", 8)
var github = "gh" + "p_" + rep("A1b2", 9)

var positive = []struct{ in, secret string }{
	{"key " + anthropic, anthropic},
	{"openai " + "sk-" + "proj-" + rep("Qw3r", 8), "sk-" + "proj-" + rep("Qw3r", 8)},
	{"token " + github, github},
	{"oauth " + "gh" + "o_" + rep("Z9y8", 9), "gh" + "o_" + rep("Z9y8", 9)},
	{"server " + "gh" + "s_" + rep("K7j6", 9), "gh" + "s_" + rep("K7j6", 9)},
	{"pat " + "github_" + "pat_" + rep("11AB", 10), "github_" + "pat_" + rep("11AB", 10)},
	{"aws " + "AK" + "IA" + "IOSFODNN7EXAMPL1", "AK" + "IA" + "IOSFODNN7EXAMPL1"},
	{"sts " + "AS" + "IA" + "IOSFODNN7EXAMPL2", "AS" + "IA" + "IOSFODNN7EXAMPL2"},
	{"slack " + "xo" + "xb-" + "1234567890-" + rep("ab12", 6), "xo" + "xb-" + "1234567890-" + rep("ab12", 6)},
	{"slack " + "xo" + "xp-" + "1234567890-" + rep("cd34", 6), "xo" + "xp-" + "1234567890-" + rep("cd34", 6)},
	{"google " + "AI" + "za" + "Sy" + rep("D1e2", 8) + "x", "AI" + "za" + "Sy" + rep("D1e2", 8) + "x"},
	{"stripe " + "sk" + "_live_" + rep("4eC3", 6), "sk" + "_live_" + rep("4eC3", 6)},
	{"stripe " + "rk" + "_live_" + rep("9fD2", 6), "rk" + "_live_" + rep("9fD2", 6)},
	{"jwt " + "eyJ" + "hbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
	{"-----BEGIN RSA " + "PRIVATE KEY-----\\nMIIEowIBAAKCAQEA7x\\n-----END RSA " + "PRIVATE KEY-----", "MIIEowIBAAKCAQEA7x"},
	{"db https://admin:" + "hunter2pass@db.example.com/x", "hunter2pass"},
	{"password=" + "correcthorse", "correcthorse"},
	{"api_key: " + "zz-local-value-9", "zz-local-value-9"},
	{"export TOKEN=" + "abcd1234efgh", "abcd1234efgh"},
	{"blob " + "q8Zr2Lm7Vx4Kp9Tn3Wc6Hb1Fs5Gd0Jy2Ue8Ra4Xo", "q8Zr2Lm7Vx4Kp9Tn3Wc6Hb1Fs5Gd0Jy2Ue8Ra4Xo"},
}

var negative = []string{
	"commit 3ffc179ddb6d3ec3b953576443cb3098d0c1689c",
	"id 123e4567-e89b-12d3-a456-426614174000",
	"go test ./...",
	"/Users/lawbringr/IdeaProjects/a6n-ai/inscribe/internal/store/store.go",
	"see https://github.com/a6n-ai/inscribe/pull/12",
	"send the Bearer header as documented",
	`img := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="`,
	"TOKEN_LIMIT=4000",
	"sha256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
}

func newScrubber(t *testing.T) *Scrubber {
	t.Helper()
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPositiveCorpusAllRedacted(t *testing.T) {
	s := newScrubber(t)
	for _, c := range positive {
		out, findings := s.Scrub(c.in)
		if !strings.Contains(out, "[REDACTED:") || strings.Contains(out, c.secret) || len(findings) == 0 {
			t.Errorf("not redacted:\n in:  %s\n out: %s", c.in, out)
		}
	}
}

func TestNegativeCorpusUnchanged(t *testing.T) {
	s := newScrubber(t)
	for _, in := range negative {
		if out, _ := s.Scrub(in); out != in {
			t.Errorf("changed:\n in:  %s\n out: %s", in, out)
		}
	}
}

func TestKindNames(t *testing.T) {
	s := newScrubber(t)
	cases := map[string]string{
		anthropic:                        "[REDACTED:anthropic_key]",
		github:                           "[REDACTED:github_token]",
		"AK" + "IA" + "IOSFODNN7EXAMPL1": "[REDACTED:aws_access_key]",
	}
	for in, want := range cases {
		if out, _ := s.Scrub(in); out != want {
			t.Errorf("Scrub(%q) = %q, want %q", in[:6], out, want)
		}
	}
}

func TestScrubAnyNested(t *testing.T) {
	s := newScrubber(t)
	in := map[string]any{
		"command": "curl -H 'x-api-key: " + anthropic + "'",
		"list":    []any{"ok", map[string]any{"deep": github}},
		"n":       3,
	}
	out := s.ScrubAny(in).(map[string]any)
	if strings.Contains(out["command"].(string), anthropic) {
		t.Fatal("command not scrubbed")
	}
	deep := out["list"].([]any)[1].(map[string]any)["deep"].(string)
	if deep != "[REDACTED:github_token]" {
		t.Fatalf("deep = %q", deep)
	}
	if out["n"] != 3 || out["list"].([]any)[0] != "ok" {
		t.Fatal("non-secret values changed")
	}
}

// Bypasses reported by the security review.
func TestReviewBypassesRedacted(t *testing.T) {
	s := newScrubber(t)
	cases := []struct{ in, secret string }{
		{`{"password": "` + "hunter2json" + `"}`, "hunter2json"},
		{`{'api_key': '` + "zz9singleq" + `'}`, "zz9singleq"},
		{`"token":"` + `ab\"cd9escaped` + `"`, `cd9escaped`},
		{"https://user:" + "p@ss9word" + "@db.example.com/x", "ss9word"},
		{"blob base64," + "q8Zr2Lm7Vx4Kp9Tn3Wc6Hb1Fs5Gd0Jy2Ue8Ra4Xo", "q8Zr2Lm7Vx4Kp9Tn3Wc6Hb1Fs5Gd0Jy2Ue8Ra4Xo"},
	}
	for _, c := range cases {
		if out, _ := s.Scrub(c.in); strings.Contains(out, c.secret) {
			t.Errorf("leaked:\n in:  %s\n out: %s", c.in, out)
		}
	}
}

// Second security review round.
func TestReviewRegressionsRedacted(t *testing.T) {
	s := newScrubber(t)
	cases := []struct{ in, secret string }{
		{`password="` + `C:\vault9key\` + `"`, `vault9key`},
		{`password="` + "unterminated9value", "unterminated9value"},
		{"password=" + "abc9def,ghi9jkl", "ghi9jkl"},
		{"secret=" + "part1}part2tail", "part2tail"},
	}
	for _, c := range cases {
		if out, _ := s.Scrub(c.in); strings.Contains(out, c.secret) {
			t.Errorf("leaked:\n in:  %s\n out: %s", c.in, out)
		}
	}
}

// Many entropy candidates in a large string must not rescan the prefix for
// every candidate. Compares growth, not wall time, so -race does not flake it:
// 10x input should cost about 10x, not about 100x.
func TestLargeInputLinearTime(t *testing.T) {
	s := newScrubber(t)
	unit := "q8Zr2Lm7Vx4Kp9Tn3Wc6Hb1Fs5Gd0Jy2Ue8Ra4Xo "
	timeFor := func(n int) time.Duration {
		in := strings.Repeat(unit, n)
		start := time.Now()
		s.Scrub(in)
		return time.Since(start)
	}
	small, large := timeFor(1000), timeFor(10000)
	if ratio := float64(large) / float64(small); ratio > 30 {
		t.Fatalf("10x input took %.1fx longer (%v vs %v), want roughly linear", ratio, large, small)
	}
}

func TestManyUnclosedValuesLinearTime(t *testing.T) {
	s := newScrubber(t)
	timeFor := func(n int) time.Duration {
		in := strings.Repeat("echo password=\"x9unclosed value here\n", n)
		start := time.Now()
		s.Scrub(in)
		return time.Since(start)
	}
	small, large := timeFor(2000), timeFor(20000)
	if ratio := float64(large) / float64(small); ratio > 30 {
		t.Fatalf("10x input took %.1fx longer (%v vs %v), want roughly linear", ratio, large, small)
	}
}

func TestQuotedValueDoesNotCrossLines(t *testing.T) {
	s := newScrubber(t)
	in := "password=\"first9value\napi_key=\"second9value\"\n"
	out, _ := s.Scrub(in)
	if strings.Contains(out, "first9value") || strings.Contains(out, "second9value") {
		t.Fatalf("leaked:\n in:  %q\n out: %q", in, out)
	}
	if strings.Count(out, "[REDACTED:assignment]") != 2 {
		t.Fatalf("want two separate assignment redactions, got %q", out)
	}
}

// Security review round 4: a properly closed value may span lines.
func TestClosedMultilineValueRedacted(t *testing.T) {
	s := newScrubber(t)
	for _, in := range []string{
		"password=\"line1\nline2tail9\"\n",
		"{\"password\": \"line1\nline2tail9\", \"x\": 1}",
		"secret='line1\nline2tail9'",
	} {
		if out, _ := s.Scrub(in); strings.Contains(out, "line2tail9") || strings.Contains(out, "line1") {
			t.Errorf("leaked:\n in:  %q\n out: %q", in, out)
		}
	}
}

// Security review round 5: a multi-line value must not end on a quote that
// opens another assignment, with LF or CRLF line ends.
func TestValueStopsBeforeNextAssignment(t *testing.T) {
	s := newScrubber(t)
	cases := []struct{ in, secret string }{
		{"password=\"first9value\r\napi_key=\"second9value\"\r\n", "second9value"},
		{"password=\"first9value\nfoo = 1\ntoken=\"third9value\"", "third9value"},
		{"{\"password\": \"first9value\n\"api_key\": \"second9value\"}", "second9value"},
		{"password=\"first9value\r\n", "first9value"},
		// The quote ending line 2 opens api_key's own multi-line value.
		{"password: \"first9value\napi_key: \"\n  second9value\"\n", "second9value"},
		// A secret key in the middle of the next line also ends the value.
		{"echo password=\"first9value\necho token=\"second9value\n", "second9value"},
	}
	for _, c := range cases {
		if out, _ := s.Scrub(c.in); strings.Contains(out, c.secret) || strings.Contains(out, "first9value") {
			t.Errorf("leaked:\n in:  %q\n out: %q", c.in, out)
		}
	}
}

func TestExtraPatterns(t *testing.T) {
	s, err := New([]string{`ACME-[0-9]{6}`})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Scrub("ticket ACME-123456"); out != "ticket [REDACTED:custom]" {
		t.Fatalf("custom = %q", out)
	}
	if _, err := New([]string{"("}); err == nil {
		t.Fatal("invalid user pattern accepted")
	}
}

// Security review round 6.
func TestRound6Bypasses(t *testing.T) {
	s, err := New([]string{`ACME-[0-9]{6}`})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, in, secret string }{
		{"escaped newline", "password=\"first9\\\napi_key=\"second9value\"\n", "second9value"},
		{"single-quote backslash", "secret='abc9\\' token=second9tail", "second9tail"},
		{"custom pattern at value start", "password=ACME-123456tail9secret", "tail9secret"},
		{"yaml value on next line", "password:\n  hunter9two\nother: 1\n", "hunter9two"},
	}
	for _, c := range cases {
		if out, _ := s.Scrub(c.in); strings.Contains(out, c.secret) {
			t.Errorf("%s leaked:\n in:  %q\n out: %q", c.name, c.in, out)
		}
	}
	if out, _ := s.Scrub("password:\n\nnext: 1"); strings.Contains(out, "next") == false {
		t.Errorf("blank line after key must not pull in the next key: %q", out)
	}
}

// Security review round 7: YAML block scalars and multi-line plain values.
func TestYAMLMultilineValues(t *testing.T) {
	s := newScrubber(t)
	cases := []struct{ name, in string }{
		{"literal block", "db:\n  password: |\n    line1tail9\n    line2tail9\n  host: x\n"},
		{"folded block with chomp", "password: >-\n  line1tail9\n  line2tail9\nother: 1\n"},
		{"plain continuation", "password:\n  line1tail9\n  line2tail9\nother: 1\n"},
	}
	for _, c := range cases {
		out, _ := s.Scrub(c.in)
		if strings.Contains(out, "line1tail9") || strings.Contains(out, "line2tail9") {
			t.Errorf("%s leaked:\n in:  %q\n out: %q", c.name, c.in, out)
		}
		if !strings.Contains(out, "host: x") && !strings.Contains(out, "other: 1") {
			t.Errorf("%s over-redacted the next key:\n out: %q", c.name, out)
		}
	}
}

func TestRound8YAMLPropertiesAndSameLineKeys(t *testing.T) {
	s := newScrubber(t)
	for _, in := range []string{
		"password: !secret |\n  line1tail9\n  line2tail9\nhost: x\n",
		"password: &anchor >-\n  line1tail9\nhost: x\n",
		"password: |  # the db pass\n  line1tail9\nhost: x\n",
	} {
		out, _ := s.Scrub(in)
		if strings.Contains(out, "tail9") {
			t.Errorf("leaked for %q: out %q", in, out)
		}
		if !strings.Contains(out, "host: x") {
			t.Errorf("over-redacted for %q: out %q", in, out)
		}
	}
}

func TestManySameLineKeysLinear(t *testing.T) {
	s := newScrubber(t)
	timeFor := func(n int) time.Duration {
		in := "token=a password=b secret=c auth=d:\n" + strings.Repeat("  deeper line9 here\n", n)
		start := time.Now()
		s.Scrub(in)
		return time.Since(start)
	}
	small, large := timeFor(2000), timeFor(20000)
	if ratio := float64(large) / float64(small); ratio > 30 {
		t.Fatalf("10x input took %.1fx longer (%v vs %v), want roughly linear", ratio, large, small)
	}
}

func TestSecretKeysInsideIdentifiers(t *testing.T) {
	s := newScrubber(t)
	for _, c := range []struct{ in, value string }{
		{"DB_PASSWORD=hunter9two", "hunter9two"},
		{"AWS_SECRET_ACCESS_KEY=wJalr9EXAMPLEKEY/K7MDENG", "wJalr9EXAMPLEKEY/K7MDENG"},
		{"client_secret=cs9value", "cs9value"},
		{"access_token=at9value", "at9value"},
		{"export API_TOKEN=tok9value", "tok9value"},
		{"refresh_token: rt9value", "rt9value"},
		{`"private_key": "pk9value"`, "pk9value"},
	} {
		out, _ := s.Scrub(c.in)
		if strings.Contains(out, c.value) {
			t.Errorf("%q leaked %q: out %q", c.in, c.value, out)
		}
	}
	for _, in := range []string{"TOKEN_LIMIT=4000", "monkey=banana9"} {
		if out, _ := s.Scrub(in); out != in {
			t.Errorf("%q changed to %q", in, out)
		}
	}
}

func TestCLIPasswordArgs(t *testing.T) {
	s := newScrubber(t)
	for _, in := range []string{
		"mysql -uroot -pmy9pass db",
		"mysqldump -p'my9pass' db",
		"docker login -u me -p my9pass reg.io",
		"podman login --password my9pass",
		"psql --password=my9pass",
		"curl -u user:my9pass https://x",
		"curl --user user:my9pass https://x",
	} {
		out, _ := s.Scrub(in)
		if strings.Contains(out, "my9pass") {
			t.Errorf("%q leaked: out %q", in, out)
		}
	}
	for _, in := range []string{"git log -p", "docker login --password-stdin", "mkdir -p build/out"} {
		if out, _ := s.Scrub(in); out != in {
			t.Errorf("%q changed to %q", in, out)
		}
	}
}

// A redaction tag is skipped only when it is the whole value, so a secret
// appended after one is still redacted. A rescrub reports nothing new.
func TestRescrubIdempotentAndTagPrefixNotTrusted(t *testing.T) {
	s := newScrubber(t)
	in := "export ANTHROPIC_API_KEY=sk-ant-api03-" + strings.Repeat("Q", 30)
	once, f1 := s.Scrub(in)
	twice, f2 := s.Scrub(once)
	if twice != once || len(f1) == 0 || len(f2) != 0 {
		t.Fatalf("rescrub: out %q -> %q, findings %d then %d", once, twice, len(f1), len(f2))
	}
	out, _ := s.Scrub("password=[REDACTED:assignment]hunter2zz9")
	if strings.Contains(out, "hunter2zz9") {
		t.Fatalf("leaked after tag prefix: %q", out)
	}
	for _, in := range []string{"password=[REDACTED:hunter2]", "password=[REDACTED:assignment]hunter2zz9"} {
		out, f := s.Scrub(in)
		if len(f) != 1 || strings.Contains(out, "hunter2") {
			t.Fatalf("Scrub(%q) = %q with %d findings", in, out, len(f))
		}
	}
	in = `{"password": "hunter2zz9"}`
	once, f1 = s.Scrub(in)
	twice, f2 = s.Scrub(once)
	if twice != once || len(f1) != 1 || len(f2) != 0 {
		t.Fatalf("quoted-key rescrub: %q -> %q, findings %d then %d", once, twice, len(f1), len(f2))
	}
}

// The tag skip is decided on the full value span, so YAML continuation lines
// after a next-line tag are still redacted.
func TestTagSkipUsesFullYAMLSpan(t *testing.T) {
	s := newScrubber(t)
	out, f := s.Scrub("password:\n  [REDACTED:assignment]\n  hunter2zz9\n")
	if strings.Contains(out, "hunter2zz9") || len(f) != 1 {
		t.Fatalf("leaked continuation: %q, %d findings", out, len(f))
	}
	in := "password:\n  [REDACTED:assignment]\n"
	out, f = s.Scrub(in)
	if out != in || len(f) != 0 {
		t.Fatalf("rescrub not idempotent: %q, %d findings", out, len(f))
	}
}

func TestValueBoundaries(t *testing.T) {
	s := newScrubber(t)
	cases := []struct {
		name, in string
		gone     []string
		keep     []string
		findings int
	}{
		{"unquoted to eol", "password: correct horse battery\n", []string{"horse", "battery"}, nil, 1},
		{"unquoted eq to eol", "password=correct horse\n", []string{"horse"}, nil, 1},
		{"yaml plain continuation", "password: abc\n  def\nnext: x", []string{"def"}, []string{"next: x"}, 1},
		{"yaml continuation stops at key line", "password: abc\n  user: bob\n", []string{"abc"}, []string{"user: bob"}, 1},
		{"tag then secret in json", `{"password": [REDACTED:assignment]hunter2zz9}`, []string{"hunter2zz9"}, nil, 1},
		{"json unquoted ends at comma", `{"password": hunter2zz9, "user": "bob"}`, []string{"hunter2zz9"}, []string{`"user": "bob"`}, 1},
		{"json unquoted spans spaces", `{"password": hunter 2zz9}`, []string{"2zz9"}, nil, 1},
		{"shell and", "TOKEN=abc123 && make build", []string{"abc123"}, []string{"&& make build"}, 1},
		{"shell semicolon", "TOKEN=abc123; make build", []string{"abc123"}, []string{"; make build"}, 1},
		{"shell pipe", "TOKEN=abc123 | cat", []string{"abc123"}, []string{"| cat"}, 1},
		{"shell next assignment", "TOKEN=abc123 USER=bob make", []string{"abc123"}, []string{"USER=bob make"}, 1},
		{"prose not a key", "description: use the password manager", nil, []string{"use the password manager"}, 0},
	}
	for _, c := range cases {
		out, f := s.Scrub(c.in)
		for _, g := range c.gone {
			if strings.Contains(out, g) {
				t.Errorf("%s: leaked %q in %q", c.name, g, out)
			}
		}
		for _, k := range c.keep {
			if !strings.Contains(out, k) {
				t.Errorf("%s: lost %q in %q", c.name, k, out)
			}
		}
		if len(f) != c.findings {
			t.Errorf("%s: %d findings, want %d (%q)", c.name, len(f), c.findings, out)
		}
		again, f2 := s.Scrub(out)
		if again != out || len(f2) != 0 {
			t.Errorf("%s: rescrub %q -> %q, %d findings", c.name, out, again, len(f2))
		}
	}
}

func TestGluedSeparatorCorpus(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"password", "secret", "token", "api_key", "DB_PASSWORD", "auth"}
	seps := []string{"=", ": ", `": `, " = ", ":"}
	vals := []string{"hun2zz;qq9x", "hun2zz|qq9x", "hun2zz&&qq9x", "hun2zz&qq9x", "hun2zz#qq9x",
		"hun2zz'qq9x", "hun2zz\"qq9x", "hun2zz]qq9x", "hun2zz,qq9x", "hun2zz}qq9x", "[REDACTED:assignment]qq9x",
		"hun2zz)qq9x", "hun2zz`qq9x", "hun2zz<qq9x", "|\n  hun2zz\n  qq9x"}
	for _, k := range keys {
		for _, sp := range seps {
			for _, v := range vals {
				pre := k
				if strings.HasPrefix(sp, `"`) {
					pre = `{"` + k
				}
				in := pre + sp + v + "\nnext: ok"
				out, _ := s.Scrub(in)
				if strings.Contains(out, "hun2zz") || strings.Contains(out, "qq9x") && !strings.Contains(v, "[REDACTED") && !strings.HasPrefix(sp, `"`) {
					t.Errorf("leak: %q -> %q", in, out)
				}
				out2, f2 := s.Scrub(out)
				if out2 != out || len(f2) != 0 {
					t.Errorf("unstable: %q -> %q -> %q (%d)", in, out, out2, len(f2))
				}
			}
		}
	}
}

func TestSeparatorFindings(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ in, want string }{
		{"password=a;b", "password=[REDACTED:assignment]"},
		{"K_SECRET=hun2zz|qq9x", "K_SECRET=[REDACTED:assignment]"},
		{"K_TOKEN=abc&&def", "K_TOKEN=[REDACTED:assignment]"},
		{"password=|\n  hun2zz\n  qq9x\nnext: ok", "password=[REDACTED:assignment]\nnext: ok"},
		{"TOKEN=abc123 && make build", "TOKEN=[REDACTED:assignment] && make build"},
		{"TOKEN=abc123 ; make", "TOKEN=[REDACTED:assignment] ; make"},
		{"TOKEN=abc123 | base64", "TOKEN=[REDACTED:assignment] | base64"},
		{`password: "abc" trailing`, "password: [REDACTED:assignment]"},
		{"password: 'abc' then words", "password: [REDACTED:assignment]"},
	} {
		out, _ := s.Scrub(c.in)
		if out != c.want {
			t.Errorf("%q -> %q, want %q", c.in, out, c.want)
		}
		out2, f2 := s.Scrub(out)
		if out2 != out || len(f2) != 0 {
			t.Errorf("unstable %q -> %q (%d)", out, out2, len(f2))
		}
	}
}
