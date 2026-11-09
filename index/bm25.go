package index

import (
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/a6n-ai/hive/internal/text"
)

const (
	k1 = 1.2
	b  = 0.75
)

type posting struct {
	ord int
	tf  int
}

// termPostings lists a term's postings in ordinal order, and the same ordinals
// as a bitmap for set algebra.
type termPostings struct {
	list []posting
	ords *roaring.Bitmap
}

func newTermPostings() *termPostings { return &termPostings{ords: roaring.New()} }

func (tp *termPostings) add(ord, tf int) {
	tp.list = append(tp.list, posting{ord: ord, tf: tf})
	tp.ords.Add(uint32(ord)) //nolint:gosec // G115: a corpus never reaches 2^32 documents
}

// tf returns the term frequency of ord, or 0 when the term does not occur in it.
func (tp *termPostings) tf(ord int) int {
	i := sort.Search(len(tp.list), func(i int) bool { return tp.list[i].ord >= ord })
	if i < len(tp.list) && tp.list[i].ord == ord {
		return tp.list[i].tf
	}
	return 0
}

// queryTerms returns the distinct terms of s, tokenized like memory text.
func queryTerms(s string) []string {
	var out []string
	for _, g := range text.Groups(s) {
		for _, t := range g {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

// tokens returns every term of s with repeats, which BM25 needs for lengths.
func tokens(s string) []string {
	var out []string
	for _, g := range text.Groups(s) {
		out = append(out, g...)
	}
	return out
}

func (ix *Index) addField(fi, ord int, s string) {
	toks := tokens(s)
	ix.lens[ord][fi] = len(toks)
	ix.totalLen[fi] += len(toks)
	counts := map[string]int{}
	for _, t := range toks {
		counts[t]++
	}
	for t, n := range counts {
		tp, ok := ix.postings[fi][t]
		if !ok {
			tp = newTermPostings()
			ix.postings[fi][t] = tp
			ix.dirty = true
		}
		tp.add(ord, n)
	}
}

// lexical scores the documents that match a query term and pass allowed. Idf
// and average length come from all live documents, so a filter changes which
// documents are scored, not how they are scored. The filter is applied before
// scoring: only documents in the intersection are visited.
func (ix *Index) lexical(terms []string, allowed *roaring.Bitmap) map[int]float64 {
	scores := map[int]float64{}
	n := float64(ix.live)
	for fi, f := range ix.fields {
		if ix.totalLen[fi] == 0 {
			continue
		}
		avg := float64(ix.totalLen[fi]) / n
		for _, term := range terms {
			matched := ix.prefixPostings(fi, term)
			docs := ix.liveDocs(matched)
			df := float64(docs.GetCardinality())
			if df == 0 {
				continue
			}
			idf := fts5Idf(n, df)
			cands := docs
			if allowed != nil {
				cands = roaring.And(docs, allowed)
			}
			it := cands.Iterator()
			for it.HasNext() {
				ord := int(it.Next()) //nolint:gosec // G115: see add
				tf := 0
				for _, tp := range matched {
					tf += tp.tf(ord)
				}
				ix.scored.Add(1)
				tfv := float64(tf)
				norm := tfv * (k1 + 1) / (tfv + k1*(1-b+b*float64(ix.lens[ord][fi])/avg))
				scores[ord] += f.Weight * idf * norm
			}
		}
	}
	return scores
}

// fts5Idf is SQLite FTS5's bm25 idf, so the index and the FTS path score alike.
func fts5Idf(n, df float64) float64 {
	idf := math.Log((n - df + 0.5) / (df + 0.5))
	if idf <= 0 {
		return 1e-6
	}
	return idf
}

// prefixPostings returns the postings of every dictionary term of field fi that
// starts with prefix.
func (ix *Index) prefixPostings(fi int, prefix string) []*termPostings {
	var out []*termPostings
	d := ix.dict[fi]
	for i := sort.SearchStrings(d, prefix); i < len(d) && strings.HasPrefix(d[i], prefix); i++ {
		out = append(out, ix.postings[fi][d[i]])
	}
	return out
}

// liveDocs returns the ordinals that contain any of the terms and are not tombstoned.
func (ix *Index) liveDocs(matched []*termPostings) *roaring.Bitmap {
	u := roaring.New()
	for _, tp := range matched {
		u.Or(tp.ords)
	}
	u.AndNot(ix.tomb)
	return u
}
