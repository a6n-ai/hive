package scrub

import (
	"strings"
	"testing"
)

func assertStable(t *testing.T, s *Scrubber, in string) string {
	t.Helper()
	out, _ := s.Scrub(in)
	out2, f2 := s.Scrub(out)
	if out2 != out || len(f2) != 0 {
		t.Errorf("unstable: %q -> %q -> %q (%d findings)", in, out, out2, len(f2))
	}
	return out
}

func TestRescrubStableAdversarial(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Every value here is "abc" (or hunter2zz), so it must never survive.
	ins := []string{
		"TOKEN='abc' qq9x", "password: 'abc'\n  qq9x\nnext: ok", "password: 'abc'|\n  qq9x",
		"TOKEN='abc' && make build", "TOKEN=\"abc\" && make build", "TOKEN='abc'&&qq9x", "TOKEN='abc' ; make",
		"TOKEN='abc';qq9x", "TOKEN='abc' | base64", "TOKEN='abc' X=qq9x",
		"password: 'abc\n  qq9x\nnext: ok", "password: \"abc\n  qq9x\nnext: ok", "password: ''\n  qq9x\nnext: ok",
		"password: '' qq9x", "password: 'abc' qq9x\n  zz8y\nnext: ok", "password='abc'\n  qq9x\nnext: ok",
		"password: 'a\n  b'\n  qq9x\nnext: ok",
		`{"password": "abc" qq9x}`, `{"password": "abc"qq9x, "u": 1}`, `{"password": "abc", "u": "qq9x"}`,
		`password: "abc", host: "db"`, "password: 'abc' # note", "password: 'abc'   ",
		"password: 'abc'\r\n  qq9x\r\nnext: ok", "password: 'abc' \\\n  qq9x",
		"- password: 'abc'\n    qq9x\n  user: bob", "password: 'it''s' qq9x", "password: 'abc\\' qq9x",
		"password: \"abc\\\" qq9x", "TOKEN='abc' 'qq9x' && ok", "token: 'abc'\n  user: bob",
		"token: 'abc'\n  - qq9x", "export TOKEN='abc' && make", "TOKEN='abc'\tqq9x",
		"password: 'abc\n  qq9x", "password: \"abc\n  qq9x", "password: 'abc\n  qq9x\n", "password: '\n  qq9x",
		`{"password": "abc" qq9x}`, `{"password": "abc"qq9x, "u": 1}`,
		"password='[REDACTED:assignment]' hunter2zz",
	}
	for _, in := range ins {
		out := assertStable(t, s, in)
		if strings.Contains(out, "abc") && !strings.Contains(in, "[REDACTED") && !strings.Contains(in, "db") {
			t.Errorf("leak: %q -> %q", in, out)
		}
	}
}

func TestRescrubStableCorpus(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"password", "secret", "token", "api_key", "DB_PASSWORD", "auth"}
	seps := []string{"=", ": ", `": `, " = ", ":"}
	vals := []string{"hun2zz;qq9x", "hun2zz|qq9x", "hun2zz&&qq9x", "hun2zz&qq9x", "hun2zz#qq9x", "hun2zz qq9x", "hun2zz\tqq9x",
		"hun2zz'qq9x", "hun2zz\"qq9x", "hun2zz]qq9x", "hun2zz,qq9x", "hun2zz}qq9x", "'hun2zz qq9x'", "\"hun2zz qq9x\"",
		"[REDACTED:assignment]qq9x", "hun2zz ; qq9x", "hun2zz | qq9x", "hun2zz && qq9x", "hun2zz X=qq9x", "hun2zz\n  qq9x",
		"hun2zz\\\n  qq9x", "|\n  hun2zz\n  qq9x", "\n  hun2zz qq9x", "hun2zz;", "hun2zz)qq9x", "hun2zz`qq9x", "hun2zz<qq9x"}
	for _, k := range keys {
		for _, sp := range seps {
			for _, v := range vals {
				pre := k
				if strings.HasPrefix(sp, `"`) {
					pre = `{"` + k
				}
				in := pre + sp + v + "\nnext: ok"
				out := assertStable(t, s, in)
				if strings.Contains(out, "hun2zz") {
					t.Errorf("leak: %q -> %q", in, out)
				}
				// No internal whitespace: the whole value is one token, as the base hid it.
				if !strings.ContainsAny(v, " \t\n") && !strings.Contains(v, "[REDACTED") && !strings.HasPrefix(sp, `"`) && strings.Contains(out, "qq9x") {
					t.Errorf("leak: %q -> %q", in, out)
				}
			}
		}
	}
	extra := []string{
		"description: use the password manager", "notes: TOKEN is set; run make && echo done",
		"export TOKEN=abc123 && make build", "TOKEN=abc123 make", "password=a;b", "url: https://x/?token=abc&user=me then go",
		"password: abc # comment", `{"password": "hunter2zz9"}`, `{"password": hunter2zz9}`, `{"password": [REDACTED:assignment]hunter2zz9}`,
		"password: abc\n  def\nnext: x", "password: abc\n  user: bob", "- password: abc\n    def", "password: abc\n\n  def",
		`["password", "hunter2zz9"]`, `{"password": ["hunter2zz9", "x"]}`, "password: [REDACTED:assignment] hunter2zz9",
		"password=[REDACTED:assignment];hunter2", "token = [REDACTED:assignment] && x", "Authorization: Bearer abcdef123456 extra",
		"password: \"abc\" trailing", "password: 'abc' then words", "api_key: abc\n  - item\n", "secret: abc\n  # note\n",
		"PASSWORD=abc|base64 -d", "TOKEN=$(cat file) && run", "password=abc&&def", "password: x\r\n  y\r\nnext: z",
	}
	for _, in := range extra {
		assertStable(t, s, in)
	}
}
