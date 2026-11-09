package index

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %.9f, want %.9f", name, got, want)
	}
}

func hitByID(t *testing.T, hits []Hit, id string) Hit {
	t.Helper()
	for _, h := range hits {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("no hit %q in %v", id, hitIDs(hits))
	return Hit{}
}

// fusionFixture: x matches the text and points at the query vector, y matches
// the text but is orthogonal to it, z matches nothing but is close to it.
func fusionFixture(t *testing.T) *Index {
	t.Helper()
	ix := New(textFields, 2)
	mustUpsert(t, ix, Doc{ID: "x", Fields: map[string]string{"text": "deploy"}, Vector: []float32{1, 0}})
	mustUpsert(t, ix, Doc{ID: "y", Fields: map[string]string{"text": "deploy"}, Vector: []float32{0, 1}})
	mustUpsert(t, ix, Doc{ID: "z", Fields: map[string]string{"text": "other"}, Vector: []float32{1, 0.1}})
	return ix
}

func TestCosineExact(t *testing.T) {
	ix := New(textFields, 2)
	mustUpsert(t, ix, Doc{ID: "a", Vector: []float32{1, 0}})
	mustUpsert(t, ix, Doc{ID: "b", Vector: []float32{0, 1}})
	mustUpsert(t, ix, Doc{ID: "c", Vector: []float32{1, 1}})

	hits := search(t, ix, Query{Vector: []float32{1, 0}, K: 10})
	if got := hitIDs(hits); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("vector hits = %v, want [a c]", got)
	}
	approx(t, "cos(a)", hitByID(t, hits, "a").Cosine, 1)
	approx(t, "cos(c)", hitByID(t, hits, "c").Cosine, 1/math.Sqrt2)
}

func TestWrongDimError(t *testing.T) {
	ix := New(textFields, 2)
	err := ix.Upsert(Doc{ID: "a", Vector: []float32{1, 0, 0}})
	if err == nil || !strings.Contains(err.Error(), "vector has 3 dims, want 2") {
		t.Fatalf("Upsert error = %v, want a dimension error", err)
	}
}

func TestZeroVectorNoNaN(t *testing.T) {
	ix := New(textFields, 2)
	mustUpsert(t, ix, Doc{ID: "zero", Fields: map[string]string{"text": "deploy"}, Vector: []float32{0, 0}})
	mustUpsert(t, ix, Doc{ID: "one", Fields: map[string]string{"text": "deploy"}, Vector: []float32{1, 0}})

	for _, q := range []Query{
		{Text: "deploy", Vector: []float32{1, 0}, K: 10, Fusion: Fusion{Mode: Convex, Alpha: 0.5}},
		{Vector: []float32{0, 0}, K: 10},
	} {
		for _, h := range search(t, ix, q) {
			for _, v := range []float64{h.BM25, h.Cosine, h.Fused, h.Score} {
				if math.IsNaN(v) {
					t.Fatalf("hit %+v has NaN", h)
				}
			}
		}
	}
}

func TestRRFMath(t *testing.T) {
	hits := search(t, fusionFixture(t), Query{Text: "deploy", Vector: []float32{1, 0}, K: 10})
	// Lexical ranks: x=1, y=2. Vector ranks (cosine > 0): x=1, z=2.
	// RRF with k=60: x = 1/61 + 1/61, y = 1/62, z = 1/62.
	approx(t, "fused(x)", hitByID(t, hits, "x").Fused, 2.0/61)
	approx(t, "fused(y)", hitByID(t, hits, "y").Fused, 1.0/62)
	approx(t, "fused(z)", hitByID(t, hits, "z").Fused, 1.0/62)
	if got := hitIDs(hits); !slices.Equal(got, []string{"x", "y", "z"}) {
		t.Fatalf("hits = %v, want [x y z]", got)
	}
}

func TestConvexMath(t *testing.T) {
	ix := fusionFixture(t)
	hits := search(t, ix, Query{Text: "deploy", Vector: []float32{1, 0}, K: 10,
		Fusion: Fusion{Mode: Convex, Alpha: 0.5}})
	// Over {x, y, z}: bm25 min-max is x=1, y=1, z=0; cosine min-max is
	// x=1, y=0, z=1/sqrt(1.01). Convex with alpha 0.5 gives x=1, y=0.5, z=0.5/sqrt(1.01).
	approx(t, "fused(x)", hitByID(t, hits, "x").Fused, 1)
	approx(t, "fused(y)", hitByID(t, hits, "y").Fused, 0.5)
	approx(t, "fused(z)", hitByID(t, hits, "z").Fused, 0.5/math.Sqrt(1.01))
}

func TestVectorOnlyQuery(t *testing.T) {
	hits := search(t, fusionFixture(t), Query{Vector: []float32{1, 0}, K: 10})
	if got := hitIDs(hits); !slices.Equal(got, []string{"x", "z"}) {
		t.Fatalf("vector-only hits = %v, want [x z]", got)
	}
	approx(t, "fused(x)", hitByID(t, hits, "x").Fused, 1.0/61)
	if h := hitByID(t, hits, "x"); h.BM25 != 0 {
		t.Fatalf("vector-only BM25 = %v, want 0", h.BM25)
	}
}

func TestHitCarriesStageScores(t *testing.T) {
	weight := func(id string) float64 { return 2 }
	hits := search(t, fusionFixture(t), Query{Text: "deploy", Vector: []float32{1, 0}, K: 10, Weight: weight})
	h := hitByID(t, hits, "x")
	if h.BM25 <= 0 || h.Cosine <= 0 || h.Fused <= 0 {
		t.Fatalf("stage scores missing: %+v", h)
	}
	approx(t, "score", h.Score, h.Fused*2)
}
