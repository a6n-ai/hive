package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/a6n-ai/hive/model"
)

// LatestChange is the newest memory update or deletion time, or 0 for an empty store.
func (s *Store) LatestChange() (int64, error) {
	var ts int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(t), 0) FROM (SELECT MAX(updated_at) AS t FROM memories UNION ALL SELECT MAX(ts) FROM memory_deletions)`).Scan(&ts)
	return ts, err
}

// ChangesSince returns the memories changed after ms, oldest first, the ids of
// memories deleted after ms, and the next cursor. Both reads share one snapshot,
// so a write by another process cannot fall between them.
func (s *Store) ChangesSince(ms int64) ([]model.Memory, []string, int64, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, ms, err
	}
	defer func() { _ = tx.Rollback() }()
	changed, next, err := memoriesSince(tx, ms)
	if err != nil {
		return nil, nil, ms, err
	}
	if s.betweenReads != nil {
		s.betweenReads()
	}
	deleted, dnext, err := deletionsSince(tx, ms)
	if err != nil {
		return nil, nil, ms, err
	}
	return changed, deleted, max(next, dnext), nil
}

func memoriesSince(tx *sql.Tx, ms int64) ([]model.Memory, int64, error) {
	q := strings.Replace(memorySelect, " FROM memories", ", updated_at FROM memories", 1) + ` WHERE updated_at > ? ORDER BY updated_at` //nolint:gosec // G202: constant SQL; the cursor is bound
	rows, err := tx.Query(q, ms)
	if err != nil {
		return nil, ms, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Memory
	next := ms
	for rows.Next() {
		var updated int64
		m, err := scanMemoryWith(rows, &updated)
		if err != nil {
			return nil, ms, err
		}
		out = append(out, *m)
		next = max(next, updated)
	}
	return out, next, rows.Err()
}

func deletionsSince(tx *sql.Tx, ms int64) ([]string, int64, error) {
	rows, err := tx.Query(`SELECT id, ts FROM memory_deletions WHERE ts > ? ORDER BY ts`, ms)
	if err != nil {
		return nil, ms, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	next := ms
	for rows.Next() {
		var id string
		var ts int64
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, ms, err
		}
		ids = append(ids, id)
		next = max(next, ts)
	}
	return ids, next, rows.Err()
}

// MemoriesByID returns the memories with the given ids, in that order. Unknown
// ids are skipped.
func (s *Store) MemoriesByID(ids []string) ([]model.Memory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(memorySelect+` WHERE id IN (`+placeholders(len(ids))+`)`, args...) //nolint:gosec // G202: only "?" placeholders are concatenated; every value is bound
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byID := make(map[string]model.Memory, len(ids))
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		byID[m.ID] = *m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]model.Memory, 0, len(byID))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}
