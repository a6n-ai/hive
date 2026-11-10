package consolidate_test

import (
	"testing"

	"github.com/a6n-ai/inscribe/internal/consolidate"
)

func TestAddScopeLimitedToSessionProject(t *testing.T) {
	const project = "/proj"
	cases := []struct {
		name, op string
		ok       bool
	}{
		{"global preference rejected", `"scope_kind":"global","scope":""`, false},
		{"project of another path rejected", `"scope_kind":"project","scope":"/other"`, false},
		{"project of this session accepted", `"scope_kind":"project","scope":"/proj"`, true},
		{"file outside the project rejected", `"scope_kind":"file","scope":"/elsewhere/x.go"`, false},
		{"file inside the project accepted", `"scope_kind":"file","scope":"/proj/x.go"`, true},
	}
	identity := func(s string) string { return s }
	for _, c := range cases {
		raw := `{"op":"add","type":"preference","key":"k","text":"t","evidence":[1],` + c.op + `}`
		ops, err := consolidate.ParseOps(`{"ops":[` + raw + `]}`)
		if err != nil {
			t.Fatal(err)
		}
		in := consolidate.Input{Project: project, Events: inputEvents([]int64{1})}
		valid, _ := consolidate.Validate(ops, in, identity, noFind)
		if got := len(valid) == 1; got != c.ok {
			t.Errorf("%s: valid = %v, want %v", c.name, got, c.ok)
		}
	}
}
