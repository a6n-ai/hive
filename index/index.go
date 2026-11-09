// Package index is an in-memory retrieval engine: BM25 over weighted text
// fields, exact vectors, tag filters, and fusion. It is rebuilt from the store
// and never the source of truth.
package index

import (
	"cmp"
	"slices"

	"github.com/RoaringBitmap/roaring/v2"
)

// Field is a named text field and its score multiplier.
type Field struct {
	Name   string
	Weight float64
}

// Doc is one indexed document. Tags look like "project:/p" or "status:active".
type Doc struct {
	ID     string
	Fields map[string]string
	Vector []float32
	Tags   []string
}

// Query asks for the top K documents. Tags are ANDed. Weight multiplies each
// hit's score (nil means 1).
type Query struct {
	Text   string
	Vector []float32
	Tags   []string
	K      int
	Fusion Fusion
	Weight func(id string) float64
}

// Hit carries the stage scores so callers can log why a document ranked.
type Hit struct {
	ID     string
	BM25   float64
	Cosine float64
	Fused  float64
	Score  float64
}

type Index struct {
	fields   []Field
	dim      int
	ids      []string
	vecs     []float32 // ordinal-major, unit length; dim floats per ordinal
	lens     [][]int   // ordinal -> token count per field
	dict     [][]string
	postings []map[string][]posting
	totalLen []int
	tags     map[string]*roaring.Bitmap
}

// New makes an index over fields. dim 0 disables vectors.
func New(fields []Field, dim int) *Index {
	ix := &Index{
		fields:   fields,
		dim:      dim,
		dict:     make([][]string, len(fields)),
		postings: make([]map[string][]posting, len(fields)),
		totalLen: make([]int, len(fields)),
		tags:     map[string]*roaring.Bitmap{},
	}
	for i := range ix.postings {
		ix.postings[i] = map[string][]posting{}
	}
	return ix
}

func (ix *Index) Upsert(d Doc) error {
	var vec []float32
	if ix.dim > 0 {
		v, err := ix.normalize(d.Vector)
		if err != nil {
			return err
		}
		vec = v
	}
	ord := len(ix.ids)
	ix.ids = append(ix.ids, d.ID)
	ix.vecs = append(ix.vecs, vec...)
	ix.lens = append(ix.lens, make([]int, len(ix.fields)))
	for fi, f := range ix.fields {
		ix.addField(fi, ord, d.Fields[f.Name])
	}
	for _, t := range d.Tags {
		if ix.tags[t] == nil {
			ix.tags[t] = roaring.New()
		}
		ix.tags[t].Add(uint32(ord)) //nolint:gosec // G115: a corpus never reaches 2^32 documents
	}
	return nil
}

func (ix *Index) Search(q Query) ([]Hit, error) {
	qv, err := ix.queryVector(q.Vector)
	if err != nil {
		return nil, err
	}
	allowed := ix.tagFilter(q.Tags)
	lex := ix.lexical(queryTerms(q.Text), allowed)
	cos := ix.cosines(qv, allowed)
	fused := lex
	if qv != nil {
		fused = ix.fuse(q.Fusion, lex, cos)
	}
	hits := make([]Hit, 0, len(fused))
	for ord, f := range fused {
		id := ix.ids[ord]
		hits = append(hits, Hit{ID: id, BM25: lex[ord], Cosine: cos[ord], Fused: f, Score: f * weightOf(q, id)})
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if q.K > 0 && len(hits) > q.K {
		hits = hits[:q.K]
	}
	return hits, nil
}

func weightOf(q Query, id string) float64 {
	if q.Weight == nil {
		return 1
	}
	return q.Weight(id)
}
