package index

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpsertReplaces(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "a", Fields: map[string]string{"text": "alpha"}})
	mustUpsert(t, ix, Doc{ID: "a", Fields: map[string]string{"text": "beta"}})

	if got := hitIDs(search(t, ix, Query{Text: "alpha", K: 10})); len(got) != 0 {
		t.Fatalf("replaced text still matches: %v", got)
	}
	if got := hitIDs(search(t, ix, Query{Text: "beta", K: 10})); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("new text hits = %v, want [a]", got)
	}
	if n := ix.Len(); n != 1 {
		t.Fatalf("Len = %d, want 1", n)
	}
}

func TestDeleteHides(t *testing.T) {
	ix := New(textFields, 0)
	mustUpsert(t, ix, Doc{ID: "a", Fields: map[string]string{"text": "deploy"}})
	mustUpsert(t, ix, Doc{ID: "b", Fields: map[string]string{"text": "deploy"}})
	ix.Delete("a")

	if got := hitIDs(search(t, ix, Query{Text: "deploy", K: 10})); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("hits after delete = %v, want [b]", got)
	}
	if n := ix.Len(); n != 1 {
		t.Fatalf("Len = %d, want 1", n)
	}
}

func TestCompactionKeepsResults(t *testing.T) {
	ix := New(textFields, 2)
	for i := range 8 {
		mustUpsert(t, ix, Doc{ID: fmt.Sprintf("d%d", i), Vector: []float32{1, float32(i)},
			Fields: map[string]string{"text": fmt.Sprintf("deploy step %d", i)}})
	}
	ix.Delete("d2")
	ix.Delete("d5")
	q := Query{Text: "deploy", Vector: []float32{1, 0}, K: 10, Fusion: Fusion{Mode: Convex, Alpha: 0.3}}
	before := search(t, ix, q)

	ix.mu.Lock()
	ix.compact()
	ix.mu.Unlock()

	after := search(t, ix, q)
	if !slices.Equal(before, after) {
		t.Fatalf("results changed by compaction:\nbefore %+v\nafter  %+v", before, after)
	}
	if len(ix.ids) != ix.Len() {
		t.Fatalf("compaction kept %d ordinals for %d live docs", len(ix.ids), ix.Len())
	}
}

func TestConcurrentSearchAndUpdate(t *testing.T) {
	ix := New(textFields, 0)
	var added atomic.Int64
	var clock atomic.Int64
	var deletedAt sync.Map // id -> clock value taken after its Delete returned
	deadline := time.Now().Add(time.Second)

	var writers sync.WaitGroup
	writers.Add(2)
	go func() { // adds each id once
		defer writers.Done()
		for i := 0; time.Now().Before(deadline); i++ {
			_ = ix.Upsert(Doc{ID: fmt.Sprintf("n%d", i), Fields: map[string]string{"text": "deploy step"}})
			added.Add(1)
		}
	}()
	go func() { // deletes ids that were added and never re-added
		defer writers.Done()
		next := 0
		for time.Now().Before(deadline) {
			if int64(next) >= added.Load() {
				continue
			}
			id := fmt.Sprintf("n%d", next)
			ix.Delete(id)
			deletedAt.Store(id, clock.Add(1))
			next++
		}
	}()

	var violations atomic.Int64
	var searchers sync.WaitGroup
	for range 8 {
		searchers.Add(1)
		go func() {
			defer searchers.Done()
			for time.Now().Before(deadline) {
				started := clock.Load()
				hits, err := ix.Search(Query{Text: "deploy", K: 10})
				if err != nil {
					violations.Add(1)
					return
				}
				for _, h := range hits {
					if v, ok := deletedAt.Load(h.ID); ok && v.(int64) <= started {
						violations.Add(1)
					}
				}
			}
		}()
	}
	writers.Wait()
	searchers.Wait()

	if n := violations.Load(); n != 0 {
		t.Fatalf("%d hits returned a document whose delete had already returned", n)
	}
}
