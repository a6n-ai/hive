package index

import "github.com/RoaringBitmap/roaring/v2"

// Delete hides the live document with id from every later search.
func (ix *Index) Delete(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ord, ok := ix.byID[id]; ok {
		ix.kill(ord)
		delete(ix.byID, id)
		ix.maybeCompact()
	}
}

// kill tombstones an ordinal and takes its tokens out of the corpus statistics.
// The caller holds the write lock.
func (ix *Index) kill(ord int) {
	ix.tomb.Add(uint32(ord)) //nolint:gosec // G115: see add
	ix.live--
	for fi, n := range ix.lens[ord] {
		ix.totalLen[fi] -= n
	}
}

// maybeCompact rebuilds once tombstones exceed a quarter of the ordinals.
func (ix *Index) maybeCompact() {
	if ix.tomb.GetCardinality()*4 > uint64(len(ix.ids)) {
		ix.compact()
	}
}

// compact drops tombstoned ordinals and rebuilds postings, vectors, tag
// bitmaps, and the id map. The caller holds the write lock.
func (ix *Index) compact() {
	remap := make(map[int]int, ix.live)
	var ids []string
	var lens [][]int
	var vecs []float32
	for ord, id := range ix.ids {
		if ix.tomb.Contains(uint32(ord)) { //nolint:gosec // G115: see add
			continue
		}
		remap[ord] = len(ids)
		ids = append(ids, id)
		lens = append(lens, ix.lens[ord])
		if ix.dim > 0 {
			vecs = append(vecs, ix.vecs[ord*ix.dim:(ord+1)*ix.dim]...)
		}
	}
	dict, postings := ix.rebuildPostings(remap)
	tags := make(map[string]*roaring.Bitmap, len(ix.tags))
	for t, bm := range ix.tags {
		if nb := remapBitmap(bm, remap); !nb.IsEmpty() {
			tags[t] = nb
		}
	}
	ix.ids, ix.lens, ix.vecs = ids, lens, vecs
	ix.dict, ix.postings, ix.tags = dict, postings, tags
	ix.tomb = roaring.New()
	ix.live = len(ids)
	ix.byID = make(map[string]int, len(ids))
	for i, id := range ids {
		ix.byID[id] = i
	}
}

// rebuildPostings keeps the postings of surviving ordinals, renumbered by remap.
func (ix *Index) rebuildPostings(remap map[int]int) ([][]string, []map[string][]posting) {
	dict := make([][]string, len(ix.fields))
	postings := make([]map[string][]posting, len(ix.fields))
	for fi := range ix.fields {
		postings[fi] = map[string][]posting{}
		for _, term := range ix.dict[fi] {
			var kept []posting
			for _, p := range ix.postings[fi][term] {
				if n, ok := remap[p.ord]; ok {
					kept = append(kept, posting{ord: n, tf: p.tf})
				}
			}
			if len(kept) > 0 {
				postings[fi][term] = kept
				dict[fi] = append(dict[fi], term)
			}
		}
	}
	return dict, postings
}

func remapBitmap(bm *roaring.Bitmap, remap map[int]int) *roaring.Bitmap {
	out := roaring.New()
	it := bm.Iterator()
	for it.HasNext() {
		if n, ok := remap[int(it.Next())]; ok {
			out.Add(uint32(n)) //nolint:gosec // G115: see add
		}
	}
	return out
}
