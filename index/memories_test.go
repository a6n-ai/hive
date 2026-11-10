package index

import (
	"slices"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func TestMemoryDocFieldsAndTags(t *testing.T) {
	d := MemoryDoc(model.Memory{ID: "m1", Type: model.Note, ScopeKind: model.ProjectScope, Scope: "/p",
		Key: "go test", Text: "run tests", Status: model.Active})
	if d.ID != "m1" || d.Fields["text"] != "run tests" || d.Fields["key"] != "go test" {
		t.Fatalf("doc = %+v", d)
	}
	for _, want := range []string{"project:/p", "type:note", "status:active"} {
		if !slices.Contains(d.Tags, want) {
			t.Fatalf("tags %v missing %s", d.Tags, want)
		}
	}
}

func TestMemoryDocScopeTags(t *testing.T) {
	g := MemoryDoc(model.Memory{ID: "g", ScopeKind: model.Global, Type: model.Preference, Status: model.Candidate})
	if !slices.Contains(g.Tags, "scope:global") || !slices.Contains(g.Tags, "status:candidate") {
		t.Fatalf("global tags = %v", g.Tags)
	}
	f := MemoryDoc(model.Memory{ID: "f", ScopeKind: model.File, Scope: "/p/a.go", Type: model.Note, Status: model.Active})
	if !slices.Contains(f.Tags, "file:/p/a.go") {
		t.Fatalf("file tags = %v", f.Tags)
	}
}
