package index

import (
	"cmp"
	"slices"
)

// FusionMode picks how lexical and vector scores are combined.
type FusionMode int

const (
	RRF FusionMode = iota
	Convex
)

// Fusion combines the lexical and vector rankings. K is the RRF constant
// (0 means 60); Alpha is the convex weight of the cosine score.
type Fusion struct {
	Mode  FusionMode
	K     float64
	Alpha float64
}

const (
	candidateLimit = 100
	defaultRRFK    = 60
)

// fuse scores the candidates of a vector query: the top lexical and top
// vector ordinals.
func (ix *Index) fuse(f Fusion, lex, cos map[int]float64) map[int]float64 {
	lexTop := ix.topRanks(lex)
	vecTop := ix.topRanks(cos)
	if f.Mode == Convex {
		return convexFuse(f.Alpha, lexTop, vecTop, lex, cos)
	}
	k := f.K
	if k == 0 {
		k = defaultRRFK
	}
	out := map[int]float64{}
	for ord, r := range lexTop {
		out[ord] += 1 / (k + float64(r))
	}
	for ord, r := range vecTop {
		out[ord] += 1 / (k + float64(r))
	}
	return out
}

// topRanks maps the best candidateLimit ordinals of scores to their 1-based rank.
func (ix *Index) topRanks(scores map[int]float64) map[int]int {
	ords := make([]int, 0, len(scores))
	for ord := range scores {
		ords = append(ords, ord)
	}
	slices.SortFunc(ords, func(a, b int) int {
		if c := cmp.Compare(scores[b], scores[a]); c != 0 {
			return c
		}
		return cmp.Compare(ix.ids[a], ix.ids[b])
	})
	ranks := map[int]int{}
	for i, ord := range ords[:min(len(ords), candidateLimit)] {
		ranks[ord] = i + 1
	}
	return ranks
}

// convexFuse min-max normalizes both scores over the union of candidates and
// blends them with alpha on the cosine.
func convexFuse(alpha float64, lexTop, vecTop map[int]int, lex, cos map[int]float64) map[int]float64 {
	cands := make([]int, 0, len(lexTop)+len(vecTop))
	for ord := range lexTop {
		cands = append(cands, ord)
	}
	for ord := range vecTop {
		if _, ok := lexTop[ord]; !ok {
			cands = append(cands, ord)
		}
	}
	bm := make([]float64, len(cands))
	cs := make([]float64, len(cands))
	for i, ord := range cands {
		bm[i] = lex[ord]
		cs[i] = cos[ord]
	}
	bmN, csN := minMax(bm), minMax(cs)
	out := make(map[int]float64, len(cands))
	for i, ord := range cands {
		out[ord] = alpha*csN[i] + (1-alpha)*bmN[i]
	}
	return out
}

// minMax rescales xs to [0, 1]. A flat, positive list maps to 1s.
func minMax(xs []float64) []float64 {
	out := make([]float64, len(xs))
	if len(xs) == 0 {
		return out
	}
	lo, hi := slices.Min(xs), slices.Max(xs)
	for i, x := range xs {
		switch {
		case hi > lo:
			out[i] = (x - lo) / (hi - lo)
		case hi > 0:
			out[i] = 1
		}
	}
	return out
}
