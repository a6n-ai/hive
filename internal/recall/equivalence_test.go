package recall

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/a6n-ai/hive/index"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

func equivalenceFixture(t *testing.T) *store.Store {
	t.Helper()
	st := newStore(t)
	for _, m := range []model.Memory{
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p1", Key: "go test", Text: "Run go test ./... before every release"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p1", Key: "deploy cmd", Text: "部署命令: make deploy"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p1", Key: "tests dir", Text: "Use the tests directory for fixtures"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p1", Key: "checklist", Text: "deployment checklist: verify deploy health"},
		{Type: model.Note, ScopeKind: model.File, Scope: "/p1/src/main.go", Key: "main", Text: "main entry point tests"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p2", Key: "staging", Text: "Deploy to staging with make staging"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p2", Key: "flaky", Text: "Testing flaky: retry the TestFoo case"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p3", Key: "alpha", Text: "Mixed CASE Keyword Alpha"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p1", Key: "old deploy", Text: "Old deploy procedure", Status: model.Forgotten},
		{Type: model.Preference, ScopeKind: model.Global, Key: "style", Text: "Prefer short answers"},
		{Type: model.Note, ScopeKind: model.Global, Key: "cli", Text: "命令行工具 needs no sudo"},
		{Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p3", Key: "stop", Text: "The and for with stop words only"},
	} {
		put(t, st, m)
	}
	return st
}

func indexOf(t *testing.T, st *store.Store) *index.Index {
	t.Helper()
	ms, err := st.ListMemories(store.ListFilter{Statuses: []model.Status{model.Active, model.Candidate}})
	if err != nil {
		t.Fatal(err)
	}
	ix := index.New(index.MemoryFields, 0)
	for _, m := range ms {
		if err := ix.Upsert(index.MemoryDoc(m)); err != nil {
			t.Fatal(err)
		}
	}
	return ix
}

// TestRecallEquivalence: the index path must rank exactly like the FTS path
// across prefixes, CJK (bigrams and unspaced), stop words, case, and projects.
func TestRecallEquivalence(t *testing.T) {
	ctx := context.Background()
	st := equivalenceFixture(t)
	src := IndexSource{Idx: indexOf(t, st), Store: st}
	c := cfg()
	c.PromptThreshold = 0.01
	projects := []string{"/p1", "/p2", "/p3", ""}
	prompts := []string{"deploy", "how do I deploy to staging", "如何部署", "命令", "go test", "testing flaky",
		"the", "CASE keyword", "alpha", "tests", "release checklist", "make deploy command", "main entry",
		"prefer short answers", "cli sudo", "staging", "keyword ALPHA", "部署", "fixtures tests", "deployment health"}

	withLines := 0
	for i, p := range prompts {
		q := Query{Project: projects[i%len(projects)], Text: p, Mode: Prompt}
		want, err := Recall(ctx, st, nil, c, q, now)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Recall(ctx, st, src, c, q, now)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(linesOf(want), linesOf(got)) {
			t.Errorf("prompt %q project %q:\nstore %v\nindex %v", p, q.Project, linesOf(want), linesOf(got))
		}
		if len(want) > 0 {
			withLines++
		}
	}
	if withLines < 10 {
		t.Fatalf("only %d of %d prompts returned lines; the fixture no longer exercises recall", withLines, len(prompts))
	}
}

func linesOf(ls []Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = fmt.Sprintf("%s|%s|%s|%v", l.MemoryID, l.Type, l.Text, l.Score)
	}
	return out
}
