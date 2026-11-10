package index

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"slices"

	"github.com/RoaringBitmap/roaring/v2"
)

const snapshotMagic = "INSCIDX1"

var (
	// ErrStale means the snapshot was built for another schema, field set, or dimension.
	ErrStale = errors.New("index: snapshot does not match the expected schema, fields, or dimension")
	// ErrCorrupt means the snapshot failed its checksum or does not decode.
	ErrCorrupt = errors.New("index: snapshot is corrupt")
)

// Stamp identifies what a snapshot was built from.
type Stamp struct {
	Schema int
	Cursor int64
	Fields []Field
	Dim    int
	// DBID is the database the snapshot was built from; a snapshot of another database is stale.
	DBID string
}

// body is the gob payload: the live state of a compacted index.
type body struct {
	Stamp    Stamp
	IDs      []string
	Vecs     []float32
	Lens     [][]int
	Dict     [][]string
	Postings [][][]snapPosting
	TotalLen []int
	Tags     map[string][]byte
}

type snapPosting struct{ Ord, TF int }

// Save writes ix as a snapshot stamped with s. It compacts first.
func (ix *Index) Save(w io.Writer, s Stamp) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.compact()
	ix.refreshDict()
	b := body{Stamp: s, IDs: ix.ids, Vecs: ix.vecs, Lens: ix.lens, Dict: ix.dict,
		Postings: ix.snapshotPostings(), TotalLen: ix.totalLen, Tags: map[string][]byte{}}
	for t, bm := range ix.tags {
		data, err := bm.MarshalBinary()
		if err != nil {
			return err
		}
		b.Tags[t] = data
	}
	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(b); err != nil {
		return err
	}
	out := make([]byte, 0, len(snapshotMagic)+4+payload.Len())
	out = append(out, snapshotMagic...)
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(payload.Bytes()))
	out = append(out, payload.Bytes()...)
	_, err := w.Write(out)
	return err
}

func (ix *Index) snapshotPostings() [][][]snapPosting {
	out := make([][][]snapPosting, len(ix.fields))
	for fi := range ix.fields {
		out[fi] = make([][]snapPosting, len(ix.dict[fi]))
		for i, term := range ix.dict[fi] {
			for _, p := range ix.postings[fi][term].list {
				out[fi][i] = append(out[fi][i], snapPosting{Ord: p.ord, TF: p.tf})
			}
		}
	}
	return out
}

// Load reads a snapshot. It returns ErrStale when the stamp does not match
// want, and ErrCorrupt on a bad magic, checksum, or payload.
func Load(r io.Reader, want Stamp) (*Index, Stamp, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Stamp{}, err
	}
	if len(data) < len(snapshotMagic)+4 || string(data[:len(snapshotMagic)]) != snapshotMagic {
		return nil, Stamp{}, ErrCorrupt
	}
	payload := data[len(snapshotMagic)+4:]
	if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(data[len(snapshotMagic):]) {
		return nil, Stamp{}, ErrCorrupt
	}
	var b body
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&b); err != nil {
		return nil, Stamp{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	got := b.Stamp
	if got.Schema != want.Schema || got.Dim != want.Dim || got.DBID != want.DBID || !slices.Equal(got.Fields, want.Fields) {
		return nil, got, ErrStale
	}
	ix, err := fromBody(b)
	return ix, got, err
}

func fromBody(b body) (*Index, error) {
	s, n := b.Stamp, len(b.IDs)
	if len(b.Lens) != n || len(b.Vecs) != n*s.Dim || len(b.Dict) != len(s.Fields) ||
		len(b.Postings) != len(s.Fields) || len(b.TotalLen) != len(s.Fields) {
		return nil, ErrCorrupt
	}
	ix := New(s.Fields, s.Dim)
	for fi := range s.Fields {
		if len(b.Postings[fi]) != len(b.Dict[fi]) {
			return nil, ErrCorrupt
		}
		ix.dict[fi] = b.Dict[fi]
		for i, term := range b.Dict[fi] {
			tp := newTermPostings()
			for _, p := range b.Postings[fi][i] {
				if p.Ord < 0 || p.Ord >= n {
					return nil, ErrCorrupt
				}
				tp.add(p.Ord, p.TF)
			}
			ix.postings[fi][term] = tp
		}
	}
	for _, l := range b.Lens {
		if len(l) != len(s.Fields) {
			return nil, ErrCorrupt
		}
	}
	for t, data := range b.Tags {
		bm := roaring.New()
		if err := bm.UnmarshalBinary(data); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		ix.tags[t] = bm
	}
	ix.ids, ix.lens, ix.vecs, ix.totalLen, ix.live = b.IDs, b.Lens, b.Vecs, b.TotalLen, n
	for i, id := range b.IDs {
		ix.byID[id] = i
	}
	return ix, nil
}
