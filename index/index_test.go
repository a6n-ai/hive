package index

import (
	"slices"
	"strings"
	"testing"
)

var textFields = []Field{{Name: "text", Weight: 1}, {Name: "key", Weight: 0.5}}

func mustUpsert(t *testing.T, ix *Index, d Doc) {
	t.Helper()
	if err := ix.Upsert(d); err != nil {
		t.Fatalf("Upsert(%s): %v", d.ID, err)
	}
}

func hitIDs(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out
}

func search(t *testing.T, ix *Index, q Query) []Hit {
	t.Helper()
	hits, err := ix.Search(q)
	if err != nil {
		t.Fatalf("Search(%q): %v", q.Text, err)
	}
	return hits
}

func TestBM25RanksMoreRelevantFirst(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "a", Fields: map[string]string{"text": "deploy deploy deploy build"}})
	mustUpsert(t, ix, Doc{ID: "b", Fields: map[string]string{"text": "deploy notes for the team lead of ops"}})
	mustUpsert(t, ix, Doc{ID: "c", Fields: map[string]string{"text": "unrelated words entirely"}})

	hits := search(t, ix, Query{Text: "deploy", K: 10})
	if got := hitIDs(hits); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("hits = %v, want [a b]", got)
	}
}

func TestFieldWeights(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "text-match", Fields: map[string]string{"text": "alpha gamma", "key": "zzzz"}})
	mustUpsert(t, ix, Doc{ID: "key-match", Fields: map[string]string{"text": "gamma delta", "key": "alpha"}})

	hits := search(t, ix, Query{Text: "alpha", K: 10})
	if got := hitIDs(hits); !slices.Equal(got, []string{"text-match", "key-match"}) {
		t.Fatalf("hits = %v, want text-match before key-match", got)
	}
}

func TestPrefixMatch(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "a", Fields: map[string]string{"text": "run the tests before release"}})

	if got := hitIDs(search(t, ix, Query{Text: "test", K: 10})); !slices.Equal(got, []string{"a"}) {
		t.Fatalf(`"test" hits = %v, want [a]`, got)
	}
}

func TestCJKBigrams(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "deploy", Fields: map[string]string{"text": "部署命令"}})
	mustUpsert(t, ix, Doc{ID: "other", Fields: map[string]string{"text": "写测试"}})

	if got := hitIDs(search(t, ix, Query{Text: "如何部署", K: 10})); !slices.Equal(got, []string{"deploy"}) {
		t.Fatalf("CJK hits = %v, want [deploy]", got)
	}
}

func TestTagsAND(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "both", Tags: []string{"project:/p", "type:command"}, Fields: map[string]string{"text": "deploy"}})
	mustUpsert(t, ix, Doc{ID: "project", Tags: []string{"project:/p"}, Fields: map[string]string{"text": "deploy"}})
	mustUpsert(t, ix, Doc{ID: "type", Tags: []string{"type:command"}, Fields: map[string]string{"text": "deploy"}})

	hits := search(t, ix, Query{Text: "deploy", Tags: []string{"project:/p", "type:command"}, K: 10})
	if got := hitIDs(hits); !slices.Equal(got, []string{"both"}) {
		t.Fatalf("AND-filtered hits = %v, want [both]", got)
	}
}

func TestEmptyTextAndHugeDoc(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "empty", Fields: map[string]string{"text": ""}})
	mustUpsert(t, ix, Doc{ID: "huge", Fields: map[string]string{"text": strings.Repeat("abcd ", 200_000)}})

	if hits := search(t, ix, Query{Text: "", K: 10}); len(hits) != 0 {
		t.Fatalf("empty query hits = %v, want none", hitIDs(hits))
	}
	if got := hitIDs(search(t, ix, Query{Text: "abcd", K: 10})); !slices.Equal(got, []string{"huge"}) {
		t.Fatalf("huge doc hits = %v, want [huge]", got)
	}
}

func TestWeightMultiplier(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "down", Fields: map[string]string{"text": "deploy"}})
	mustUpsert(t, ix, Doc{ID: "up", Fields: map[string]string{"text": "deploy"}})
	weight := func(id string) float64 {
		if id == "down" {
			return 0.5
		}
		return 1
	}

	hits := search(t, ix, Query{Text: "deploy", K: 10, Weight: weight})
	if got := hitIDs(hits); !slices.Equal(got, []string{"up", "down"}) {
		t.Fatalf("hits = %v, want [up down]", got)
	}
	if hits[1].Score != hits[1].BM25*0.5 {
		t.Fatalf("Score = %v, want BM25 %v x 0.5", hits[1].Score, hits[1].BM25)
	}
}
