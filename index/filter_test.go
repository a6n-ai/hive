package index

import (
	"fmt"
	"math"
	"testing"
)

const filterDocs, filterProjects = 5000, 20

func filterFixture(t *testing.T) (*Index, map[string]string) {
	t.Helper()
	ix := New(textFields, 0)
	projOf := map[string]string{}
	for i := range filterDocs {
		id := fmt.Sprintf("d%d", i)
		proj := fmt.Sprintf("/p%d", i%filterProjects)
		projOf[id] = proj
		text := fmt.Sprintf("w%03d w%03d", i%300, (i*7)%500)
		if i%3 == 0 {
			text += " deploy"
		}
		mustUpsert(t, ix, Doc{ID: id, Tags: []string{"project:" + proj}, Fields: map[string]string{"text": text, "key": "k"}})
	}
	return ix, projOf
}

// TestFilteredSearchMatchesUnfiltered pins that a tag filter changes which
// documents are scored, not how they are scored.
func TestFilteredSearchMatchesUnfiltered(t *testing.T) {
	ix, projOf := filterFixture(t)
	for q := range 30 {
		proj := fmt.Sprintf("/p%d", q%filterProjects)
		text := fmt.Sprintf("w%03d deploy", q*9%300)
		unfiltered := search(t, ix, Query{Text: text})
		var want []Hit
		for _, h := range unfiltered {
			if projOf[h.ID] == proj {
				want = append(want, h)
			}
		}
		want = want[:min(len(want), 10)]
		got := search(t, ix, Query{Text: text, Tags: []string{"project:" + proj}, K: 10})
		if len(got) != len(want) {
			t.Fatalf("query %q in %s: %d hits, want %d", text, proj, len(got), len(want))
		}
		for i := range got {
			if got[i].ID != want[i].ID || math.Abs(got[i].BM25-want[i].BM25) > 1e-9 || math.Abs(got[i].Score-want[i].Score) > 1e-9 {
				t.Fatalf("query %q in %s, hit %d: got %+v, want %+v", text, proj, i, got[i], want[i])
			}
		}
	}
}

// TestFilterFirstScoresFewerPostings: a filter that passes 1/20 of the
// documents must score at most a tenth of the postings an unfiltered search scores.
func TestFilterFirstScoresFewerPostings(t *testing.T) {
	ix, _ := filterFixture(t)
	ix.scored.Store(0)
	search(t, ix, Query{Text: "deploy", K: 10})
	unfiltered := ix.scored.Load()

	ix.scored.Store(0)
	search(t, ix, Query{Text: "deploy", Tags: []string{"project:/p3"}, K: 10})
	filtered := ix.scored.Load()

	if unfiltered == 0 || filtered*10 > unfiltered {
		t.Fatalf("filtered search scored %d postings, unfiltered %d; want filtered <= unfiltered/10", filtered, unfiltered)
	}
}
