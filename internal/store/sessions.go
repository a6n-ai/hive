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

// IngestTarget is an ended session whose transcript has usage to read.
type IngestTarget struct {
	Session, Path string
	LastEvent     int64
}

// SessionsToIngest returns ended sessions of agent that were never ingested or
// had events after their last ingest, newest first.
func (s *Store) SessionsToIngest(agent string, limit int) ([]IngestTarget, error) {
	rows, err := s.db.Query(`SELECT id, transcript_path, last_event FROM sessions
		WHERE agent = ? AND ended IS NOT NULL AND transcript_path IS NOT NULL
		AND (usage_ingested_at IS NULL OR last_event > usage_ingested_at)
		ORDER BY last_event DESC LIMIT ?`, agent, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []IngestTarget
	for rows.Next() {
		var t IngestTarget
		if err := rows.Scan(&t.Session, &t.Path, &t.LastEvent); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
