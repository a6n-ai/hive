package index

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func snapshotFixture(t *testing.T) *Index {
	t.Helper()
	ix := New(textFields, 2)
	for i := range 6 {
		mustUpsert(t, ix, Doc{
			ID:     fmt.Sprintf("d%d", i),
			Vector: []float32{1, float32(i)},
			Tags:   []string{"project:/p", fmt.Sprintf("type:t%d", i%2)},
			Fields: map[string]string{"text": fmt.Sprintf("deploy step %d 部署", i), "key": "k"},
		})
	}
	ix.Delete("d3")
	return ix
}

func stamp() Stamp {
	return Stamp{Schema: 2, Cursor: 99, Fields: textFields, Dim: 2}
}

func saveBytes(t *testing.T, ix *Index, s Stamp) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := ix.Save(&buf, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Save wrote no bytes")
	}
	return buf.Bytes()
}

func TestSnapshotRoundTrip(t *testing.T) {
	ix := snapshotFixture(t)
	q := Query{Text: "deploy", Vector: []float32{1, 0}, Tags: []string{"project:/p"}, K: 10,
		Fusion: Fusion{Mode: Convex, Alpha: 0.4}}
	want := search(t, ix, q)

	loaded, got, err := Load(bytes.NewReader(saveBytes(t, ix, stamp())), stamp())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schema != 2 || got.Cursor != 99 || got.Dim != 2 || !slices.Equal(got.Fields, textFields) {
		t.Fatalf("stamp = %+v, want %+v", got, stamp())
	}
	if loaded.Len() != ix.Len() {
		t.Fatalf("Len = %d, want %d", loaded.Len(), ix.Len())
	}
	if res := search(t, loaded, q); !slices.Equal(res, want) {
		t.Fatalf("loaded results differ:\nwant %+v\ngot  %+v", want, res)
	}
}

func TestSnapshotStaleSchema(t *testing.T) {
	data := saveBytes(t, snapshotFixture(t), stamp())
	for name, want := range map[string]Stamp{
		"schema": {Schema: 3, Cursor: 99, Fields: textFields, Dim: 2},
		"fields": {Schema: 2, Cursor: 99, Fields: []Field{{Name: "text", Weight: 1}}, Dim: 2},
		"dim":    {Schema: 2, Cursor: 99, Fields: textFields, Dim: 3},
	} {
		if _, _, err := Load(bytes.NewReader(data), want); !errors.Is(err, ErrStale) {
			t.Fatalf("%s mismatch: Load err = %v, want ErrStale", name, err)
		}
	}
}

func TestSnapshotCorruptAndTruncated(t *testing.T) {
	data := saveBytes(t, snapshotFixture(t), stamp())

	flipped := bytes.Clone(data)
	flipped[len(flipped)/2] ^= 0xFF
	if _, _, err := Load(bytes.NewReader(flipped), stamp()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("flipped byte: Load err = %v, want ErrCorrupt", err)
	}
	if _, _, err := Load(bytes.NewReader(data[:len(data)/2]), stamp()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: Load err = %v, want ErrCorrupt", err)
	}
}
