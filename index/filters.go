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

// visible reports whether ord is live and carries every tag in allowed (nil
// means no tag filter).
func (ix *Index) visible(ord int, allowed *roaring.Bitmap) bool {
	u := uint32(ord) //nolint:gosec // G115: a corpus never reaches 2^32 documents
	return !ix.tomb.Contains(u) && (allowed == nil || allowed.Contains(u))
}
