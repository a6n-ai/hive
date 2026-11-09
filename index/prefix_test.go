package index

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestPrefixRangesAfterManyUpserts pins prefix matching against a brute-force
// oracle while documents are upserted, replaced, and deleted between searches.
func TestPrefixRangesAfterManyUpserts(t *testing.T) {
	ix := New(textFields, 0)
	live := map[string]string{}
	check := func(prefix string) {
		t.Helper()
		var want []string
		for id, text := range live {
			for _, tok := range strings.Fields(text) {
				if strings.HasPrefix(tok, prefix) {
					want = append(want, id)
					break
				}
			}
		}
		slices.Sort(want)
		hits := search(t, ix, Query{Text: prefix, K: 0})
		got := hitIDs(hits)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("prefix %q: got %d docs, want %d", prefix, len(got), len(want))
		}
	}
	for i := range 5000 {
		id := fmt.Sprintf("d%d", i%4000)
		text := fmt.Sprintf("w%04d w%04d", i%700, (i*7)%900)
		mustUpsert(t, ix, Doc{ID: id, Fields: map[string]string{"text": text}})
		live[id] = text
		if i%97 == 0 {
			gone := fmt.Sprintf("d%d", (i*13)%4000)
			ix.Delete(gone)
			delete(live, gone)
		}
		if i%50 == 0 {
			check(fmt.Sprintf("w%02d", i%70))
			check("w00")
			check("w0999")
		}
	}
	check("w012")
}
