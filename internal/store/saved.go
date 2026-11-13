package store

import "github.com/a6n-ai/inscribe/model"

// SavedBy returns the memories evidenced by the events of session at ts: one
// captured event and the events derived from it, which share its timestamp.
func (s *Store) SavedBy(session string, ts int64) ([]model.Memory, error) {
	rows, err := s.db.Query(memorySelect+` WHERE id IN (SELECT me.memory_id FROM memory_evidence me
		JOIN events e ON e.id = me.event_id WHERE e.session = ? AND e.ts = ?) ORDER BY id`, session, ts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
