package store

import (
	"github.com/a6n-ai/inscribe/model"
)

// ScoreOutcome is one injection waiting for the idle pass, or already labeled.
type ScoreOutcome struct {
	ID                int64
	MemoryID, Session string
	Agent             string
	TS                int64
	Features          string
	Score             float64
}

// InsertScoreOutcome records that memoryID was injected into session at ts.
func (t *Tx) InsertScoreOutcome(memoryID, session, agent string, ts int64, features string, score float64) error {
	_, err := t.tx.Exec(`INSERT INTO score_outcomes(memory_id, session, agent, ts, features, score)
		VALUES (?, ?, ?, ?, ?, ?)`, memoryID, session, agent, ts, features, score)
	return err
}

// OpenScoreOutcomes returns unlabeled injections whose session has ended,
// oldest first, up to limit.
func (s *Store) OpenScoreOutcomes(limit int) ([]ScoreOutcome, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT o.id, o.memory_id, o.session, o.agent, o.ts, o.features, o.score
		FROM score_outcomes o
		JOIN sessions s ON s.id = o.session
		WHERE o.resolved IS NULL AND s.ended IS NOT NULL
		ORDER BY o.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ScoreOutcome
	for rows.Next() {
		var o ScoreOutcome
		if err := rows.Scan(&o.ID, &o.MemoryID, &o.Session, &o.Agent, &o.TS, &o.Features, &o.Score); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ResolveScoreOutcome closes an outcome. label nil means the session ended
// with nothing to learn; the row is not returned again.
func (t *Tx) ResolveScoreOutcome(id int64, label *int, ts int64) error {
	var v any
	if label != nil {
		v = *label
	}
	_, err := t.tx.Exec(`UPDATE score_outcomes SET label = ?, resolved = ? WHERE id = ?`, v, ts, id)
	return err
}

// ScoreOutcomeCount returns how many injections have been recorded.
func (s *Store) ScoreOutcomeCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM score_outcomes`).Scan(&n)
	return n, err
}

// MemoriesTouching returns active memories whose scope, key, or body contains
// token. It covers columns the full-text index does not store.
func (s *Store) MemoriesTouching(project, token string, statuses []model.Status, limit int) ([]model.Memory, error) {
	if token == "" || limit <= 0 {
		return nil, nil
	}
	like := "%" + escapeLike(token) + "%"
	where, args := filterSQL("memories", ListFilter{Project: project, Statuses: statuses})
	q := memorySelect + ` WHERE (scope LIKE ? ESCAPE '\' OR key LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\')` + where +
		` ORDER BY last_seen DESC LIMIT ?` //nolint:gosec // G202: only "?" placeholders are concatenated; every value is bound
	args = append([]any{like, like, like}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
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
