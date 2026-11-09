package consolidate_test

import (
	"context"
	"testing"

	"github.com/a6n-ai/hive/internal/consolidate"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// TestApplyRechecksInsideTransaction: a change validated against an older
// snapshot must not resurrect a forgotten memory or take over a new key.
func TestApplyRechecksInsideTransaction(t *testing.T) {
	e := newEnv(t)
	ids := e.events(2)

	cand := e.memory("cand", ids[0])
	if err := e.st.Write(context.Background(), func(tx *store.Tx) error { return tx.SetStatus(cand, model.Candidate) }); err != nil {
		t.Fatal(err)
	}
	promote := consolidate.Op{Op: "promote", ID: cand}
	if err := e.st.Write(context.Background(), func(tx *store.Tx) error { return tx.SetStatus(cand, model.Forgotten) }); err != nil {
		t.Fatal(err)
	}
	rej, err := consolidate.Apply(context.Background(), e.st, "run_x", []consolidate.Op{promote})
	if err != nil || len(rej) != 1 {
		t.Fatalf("apply promote of a forgotten memory: rejected = %v, err = %v; want 1 rejected", rej, err)
	}
	if got := e.get(cand); got.Status != model.Forgotten {
		t.Errorf("forgotten memory status = %s after promote, want forgotten", got.Status)
	}

	taken := e.memory("newkey", 0) // created after the review, outside the input
	add := consolidate.Op{Op: "add", Type: "fix", ScopeKind: "project", Scope: "/p", Key: "newkey", Text: "t", Evidence: []int64{ids[1]}}
	rej, err = consolidate.Apply(context.Background(), e.st, "run_y", []consolidate.Op{add})
	if err != nil || len(rej) != 1 {
		t.Fatalf("apply add onto a new key: rejected = %v, err = %v; want 1 rejected", rej, err)
	}
	if got := e.get(taken); got.Count != 1 {
		t.Errorf("memory count = %d after rejected add, want 1 (add must not upsert)", got.Count)
	}

}
