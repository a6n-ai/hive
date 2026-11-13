package store

import "database/sql"

// shortcut: one correlated subquery per scanned session, fine for the ~30 the hook reads.

// RecentSession is a past session and the last prompt the user typed in it.
type RecentSession struct {
	ID, Agent, Project, LastPrompt string
	LastEvent                      int64
}

// RecentSessions returns up to limit sessions other than exclude, newest
// first, each with its last typed prompt. Sessions with no typed prompt are
// skipped; prompts that start with "<" are injected agent or command messages.
func (s *Store) RecentSessions(exclude string, limit int) ([]RecentSession, error) {
	rows, err := s.db.Query(`SELECT s.id, s.agent, s.project, s.last_event,
		(SELECT e.prompt_text FROM events e WHERE e.session = s.id AND e.kind = 'prompt'
			AND trim(COALESCE(e.prompt_text, '')) <> '' AND substr(ltrim(e.prompt_text), 1, 1) <> '<'
			ORDER BY e.ts DESC, e.id DESC LIMIT 1)
		FROM sessions s WHERE s.id <> ? ORDER BY s.last_event DESC LIMIT ?`, exclude, limit*4)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RecentSession
	for rows.Next() && len(out) < limit {
		var r RecentSession
		var prompt sql.NullString
		if err := rows.Scan(&r.ID, &r.Agent, &r.Project, &r.LastEvent, &prompt); err != nil {
			return nil, err
		}
		if !prompt.Valid {
			continue
		}
		r.LastPrompt = prompt.String
		out = append(out, r)
	}
	return out, rows.Err()
}
