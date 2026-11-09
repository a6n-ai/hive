package hive

import (
	"os"
	"path/filepath"

	"github.com/a6n-ai/hive/index"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

// indexFile is the index snapshot, kept under HIVE_HOME.
const indexFile = "index.snap"

// openIndex loads the snapshot when it matches this schema and starts empty
// otherwise. Either way the index then catches up with the store.
func (h *Hive) openIndex() error {
	schema, err := h.store.SchemaVersion()
	if err != nil {
		return err
	}
	dbid, _, err := h.store.GetMeta("db_id")
	if err != nil {
		return err
	}
	latest, err := h.store.LatestChange()
	if err != nil {
		return err
	}
	h.stamp = index.Stamp{Schema: schema, Fields: index.MemoryFields, DBID: dbid}
	// A cursor past the newest change means the database went back in time, as a restore does.
	if ix, got, ok := loadSnapshot(filepath.Join(h.home, indexFile), h.stamp); ok && got.Cursor <= latest {
		h.index, h.stamp.Cursor = ix, got.Cursor
	} else {
		h.index = index.New(index.MemoryFields, 0)
	}
	return h.syncIndex()
}

// loadSnapshot reports false for a missing, stale, or damaged snapshot; the
// caller rebuilds.
func loadSnapshot(path string, want index.Stamp) (*index.Index, index.Stamp, bool) {
	f, err := os.Open(path) //nolint:gosec // G304: the snapshot path is under the user's HIVE_HOME
	if err != nil {
		return nil, index.Stamp{}, false
	}
	defer func() { _ = f.Close() }()
	ix, got, err := index.Load(f, want)
	return ix, got, err == nil
}

// syncIndex applies the store's memory and deletion changes since the cursor.
// Changes stamped with the cursor's own millisecond are read again, because a
// write in that millisecond after the last sync would otherwise be skipped.
// The cursor moves only after both are applied.
func (h *Hive) syncIndex() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed, gone, next, err := h.store.ChangesSince(h.stamp.Cursor - 1)
	if err != nil {
		return err
	}
	for _, id := range gone {
		h.index.Delete(id)
	}
	for _, m := range changed {
		if m.Status != model.Active && m.Status != model.Candidate {
			h.index.Delete(m.ID)
			continue
		}
		if err := h.index.Upsert(index.MemoryDoc(m)); err != nil {
			return err
		}
	}
	h.stamp.Cursor = max(h.stamp.Cursor, next)
	return nil
}

// afterWrite brings the index up to date after a write. A failed sync leaves
// the cursor in place, so the next write or open retries it.
func (h *Hive) afterWrite() {
	if h.index != nil {
		_ = h.syncIndex()
	}
}

// saveIndex writes the snapshot through a temp file, so a crash never leaves a
// half-written one.
func (h *Hive) saveIndex() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	tmp, err := os.CreateTemp(h.home, indexFile+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if err := h.index.Save(tmp, h.stamp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(h.home, indexFile))
}

// source is where recall reads candidates: the index when one is open, else the store.
func (h *Hive) source() recall.Source {
	if h.index == nil {
		return nil
	}
	return recall.IndexSource{Idx: h.index, Store: h.store}
}

// closeIndex saves the index unless the store is read-only.
func (h *Hive) closeIndex() error {
	if h.index == nil || h.readOnly {
		return nil
	}
	return h.saveIndex()
}
