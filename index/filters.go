package index

import "github.com/RoaringBitmap/roaring/v2"

// tagFilter returns the ordinals that carry every tag, or nil when no tags
// are asked for.
func (ix *Index) tagFilter(tags []string) *roaring.Bitmap {
	var out *roaring.Bitmap
	for _, t := range tags {
		bm, ok := ix.tags[t]
		if !ok {
			return roaring.New()
		}
		if out == nil {
			out = bm.Clone()
		} else {
			out.And(bm)
		}
	}
	return out
}
