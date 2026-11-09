package index

import (
	"fmt"
	"math"

	"github.com/RoaringBitmap/roaring/v2"
)

// queryVector returns q as unit length, or nil when there is no vector to compare.
func (ix *Index) queryVector(q []float32) ([]float32, error) {
	if ix.dim == 0 || len(q) == 0 {
		return nil, nil
	}
	return ix.normalize(q)
}

// normalize returns v scaled to unit length. A missing or zero vector becomes
// all zeros, so its cosine is 0 and never NaN.
func (ix *Index) normalize(v []float32) ([]float32, error) {
	if len(v) != 0 && len(v) != ix.dim {
		return nil, fmt.Errorf("index: vector has %d dims, want %d", len(v), ix.dim)
	}
	out := make([]float32, ix.dim)
	var sq float64
	for _, x := range v {
		sq += float64(x) * float64(x)
	}
	if sq == 0 {
		return out, nil
	}
	inv := 1 / math.Sqrt(sq)
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out, nil
}

// cosines returns the exact cosine of q with each allowed document whose
// cosine is positive.
func (ix *Index) cosines(q []float32, allowed *roaring.Bitmap) map[int]float64 {
	out := map[int]float64{}
	if q == nil {
		return out
	}
	for ord := range ix.ids {
		if allowed != nil && !allowed.Contains(uint32(ord)) { //nolint:gosec // G115: see Upsert
			continue
		}
		v := ix.vecs[ord*ix.dim : (ord+1)*ix.dim]
		var dot float64
		for i, x := range v {
			dot += float64(x) * float64(q[i])
		}
		if dot > 0 {
			out[ord] = dot
		}
	}
	return out
}
