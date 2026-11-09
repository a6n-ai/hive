package store

import (
	"database/sql"
	"errors"
)

// IdleSessions returns the ids of an agent's sessions whose last event falls in
// [after, before), newest first.
func (s *Store) IdleSessions(agent string, after, before int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM sessions WHERE agent = ? AND last_event >= ? AND last_event < ? ORDER BY last_event DESC`,
		agent, after, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SessionAttempted returns when a session last had a consolidation attempt, or 0.
func (s *Store) SessionAttempted(session string) (int64, error) {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT consolidated_at FROM sessions WHERE id = ?`, session).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v.Int64, err
}

// SetSessionAttempt records a consolidation attempt that did not move the cursor.
func (t *Tx) SetSessionAttempt(session string, ts int64) error {
	_, err := t.tx.Exec(`UPDATE sessions SET consolidated_at = ? WHERE id = ?`, ts, session)
	return err
}
