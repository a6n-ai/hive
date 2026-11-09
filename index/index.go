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
	ids      []string
	lens     [][]int // ordinal -> token count per field
	dict     [][]string
	postings []map[string][]posting
	totalLen []int
	tags     map[string]*roaring.Bitmap
}

func New(fields []Field, _ int) *Index {
	ix := &Index{
		fields:   fields,
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
	ord := len(ix.ids)
	ix.ids = append(ix.ids, d.ID)
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
	terms := queryTerms(q.Text)
	if len(terms) == 0 {
		return nil, nil
	}
	allowed := ix.tagFilter(q.Tags)
	var hits []Hit
	for ord, s := range ix.lexical(terms) {
		if allowed != nil && !allowed.Contains(uint32(ord)) { //nolint:gosec // G115: see Upsert
			continue
		}
		id := ix.ids[ord]
		hits = append(hits, Hit{ID: id, BM25: s, Score: s * weightOf(q, id)})
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
