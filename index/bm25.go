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
		if _, ok := ix.postings[fi][t]; !ok {
			ix.dirty = true
		}
		ix.postings[fi][t] = append(ix.postings[fi][t], posting{ord: ord, tf: n})
	}
}

// lexical scores every document that matches a query term, summing the
// weighted BM25 of each field.
func (ix *Index) lexical(terms []string, allowed *roaring.Bitmap) map[int]float64 {
	scores := map[int]float64{}
	n := float64(ix.live)
	for fi, f := range ix.fields {
		if ix.totalLen[fi] == 0 {
			continue
		}
		avg := float64(ix.totalLen[fi]) / n
		for _, term := range terms {
			tfs := ix.prefixTF(fi, term)
			df := float64(len(tfs))
			if df == 0 {
				continue
			}
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			for ord, tf := range tfs {
				if !ix.visible(ord, allowed) {
					continue
				}
				tfv := float64(tf)
				norm := tfv * (k1 + 1) / (tfv + k1*(1-b+b*float64(ix.lens[ord][fi])/avg))
				scores[ord] += f.Weight * idf * norm
			}
		}
	}
	return scores
}

// prefixTF sums term frequencies per document over every dictionary term of
// field fi that starts with prefix.
func (ix *Index) prefixTF(fi int, prefix string) map[int]int {
	out := map[int]int{}
	d := ix.dict[fi]
	for i := sort.SearchStrings(d, prefix); i < len(d) && strings.HasPrefix(d[i], prefix); i++ {
		for _, p := range ix.postings[fi][d[i]] {
			if ix.visible(p.ord, nil) {
				out[p.ord] += p.tf
			}
		}
	}
	return out
}
