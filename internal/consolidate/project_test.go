package consolidate_test

import (
	"context"
	"testing"

	"github.com/a6n-ai/hive/internal/consolidate"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

func TestInputOnlySessionProjectAndGlobal(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)
	mine := e.memory("mine", ids[0])
	var other, global string
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		for _, m := range []*model.Memory{
			{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "/other", Key: "theirs", Text: "deploys elsewhere", Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1},
			{Type: model.Preference, ScopeKind: model.Global, Key: "always", Text: "deploys globally", Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1},
		} {
			if _, err := tx.UpsertMemory(m, nil, 0); err != nil {
				return err
			}
			if m.Key == "theirs" {
				other = m.ID
			} else {
				global = m.ID
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	in, err := consolidate.BuildInput(e.st, session, 0, 8000, e.scrub)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range in.Memories {
		got[m.ID] = true
	}
	if got[other] {
		t.Error("input includes a memory from another project")
	}
	if !got[mine] || !got[global] {
		t.Errorf("input = %v, want the session's memory and the global one", got)
	}
}

func TestOpsOnOtherProjectRejected(t *testing.T) {
	const project = "/proj"
	mems := []model.Memory{
		{ID: "other", Type: model.Fix, Key: "o", ScopeKind: model.ProjectScope, Scope: "/other", Status: model.Candidate},
		{ID: "glob", Type: model.Preference, Key: "g", ScopeKind: model.Global, Status: model.Active},
		{ID: "globc", Type: model.Preference, Key: "gc", ScopeKind: model.Global, Status: model.Candidate},
		{ID: "mine", Type: model.Fix, Key: "m", ScopeKind: model.ProjectScope, Scope: project, Status: model.Active},
	}
	ops := []string{
		`{"op":"reject","id":"other"}`,
		`{"op":"stale","id":"other"}`,
		`{"op":"merge","keep":"mine","drop":["other"]}`,
		`{"op":"supersede","old":"other","new":"mine"}`,
		`{"op":"promote","id":"other"}`,
		`{"op":"stale","id":"glob"}`,
		`{"op":"reject","id":"glob"}`,
		`{"op":"reject","id":"globc"}`,
	}
	parsed, err := consolidate.ParseOps(`{"ops":[` + joinOps(ops) + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	in := consolidate.Input{Project: project, Memories: mems}
	valid, rejected := consolidate.Validate(parsed, in, func(s string) string { return s }, noFind)
	if len(rejected) != 7 || len(valid) != 1 || valid[0].ID != "globc" {
		t.Errorf("rejected = %d valid = %d, want 7 and 1 (only the reject of the global candidate applies): %+v", len(rejected), len(valid), valid)
	}
}

func joinOps(ops []string) string {
	out := ""
	for i, op := range ops {
		if i > 0 {
			out += ","
		}
		out += op
	}
	return out
}
