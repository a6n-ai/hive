package store

// IdleSessions returns the ids of an agent's sessions whose last event is before the given time, in any order.
func (s *Store) IdleSessions(agent string, before int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM sessions WHERE agent = ? AND last_event < ?`, agent, before)
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
