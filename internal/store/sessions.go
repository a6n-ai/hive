package store

// OpenSession is a session with no end time yet.
type OpenSession struct {
	ID, Agent string
	LastEvent int64
}

// OpenSessions returns every session with no end time, newest first.
func (s *Store) OpenSessions() ([]OpenSession, error) {
	rows, err := s.db.Query(`SELECT id, agent, last_event FROM sessions WHERE ended IS NULL ORDER BY last_event DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OpenSession
	for rows.Next() {
		var o OpenSession
		if err := rows.Scan(&o.ID, &o.Agent, &o.LastEvent); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CloseSessions ends sessions whose agent stopped without a SessionEnd event,
// at their last event. Ended sessions keep their end time.
func (t *Tx) CloseSessions(ids []string) error {
	for _, id := range ids {
		if _, err := t.tx.Exec(`UPDATE sessions SET ended = last_event WHERE id = ? AND ended IS NULL`, id); err != nil {
			return err
		}
	}
	return nil
}
