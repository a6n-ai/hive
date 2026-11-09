package index

import (
	"strings"

	"github.com/RoaringBitmap/roaring/v2"
)

// tagFilter returns the ordinals that carry every tag and match every AnyOf
// group, or nil when neither is asked for.
func (ix *Index) tagFilter(tags []string, anyOf [][]string) *roaring.Bitmap {
	if len(tags) == 0 && len(anyOf) == 0 {
		return nil
	}
	var sets []*roaring.Bitmap
	for _, t := range tags {
		bm, ok := ix.tags[t]
		if !ok {
			return roaring.New()
		}
		sets = append(sets, bm)
	}
	for _, g := range anyOf {
		u := roaring.New()
		for _, t := range g {
			if bm, ok := ix.tags[t]; ok {
				u.Or(bm)
			}
		}
		if u.IsEmpty() {
			return roaring.New()
		}
		sets = append(sets, u)
	}
	out := sets[0].Clone()
	for _, s := range sets[1:] {
		out.And(s)
	}
	return out
}

// TagsWithPrefix returns every tag that starts with prefix.
func (ix *Index) TagsWithPrefix(prefix string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []string
	for t := range ix.tags {
		if strings.HasPrefix(t, prefix) {
			out = append(out, t)
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
