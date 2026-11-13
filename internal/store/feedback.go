package store

import (
	"database/sql"
	"fmt"
)

// Ask is one feedback question put to the agent and the outcome rows it covers.
type Ask struct {
	ID, Session string
	TS          int64
	Answered    bool
	Rows        []ScoreOutcome
}

// UnaskedOutcomes returns the unresolved outcomes of session that no ask covers,
// from the newest maxGroups prompt groups, newest first.
func (s *Store) UnaskedOutcomes(session string, maxGroups int) ([]ScoreOutcome, error) {
	q := `WITH e AS (SELECT o.id, o.prompt_digest FROM score_outcomes o
		WHERE o.session = ? AND o.resolved IS NULL
		AND NOT EXISTS (SELECT 1 FROM feedback_ask_rows r WHERE r.outcome_id = o.id)),
		g AS (SELECT prompt_digest FROM e GROUP BY prompt_digest ORDER BY MIN(id) DESC LIMIT ?)
		` + outcomeSelect + ` JOIN e ON e.id = o.id JOIN g ON g.prompt_digest = o.prompt_digest
		ORDER BY o.id DESC`
	rows, err := s.db.Query(q, session, maxGroups)
	if err != nil {
		return nil, err
	}
	return flatOutcomes(rows)
}

// LastAskTS is the time of the session's newest ask, 0 when it has none.
func (s *Store) LastAskTS(session string) (int64, error) {
	return lastAskTS(s.db, session)
}

// Checked inside the write lock, so two hooks cannot both insert an ask.
func (t *Tx) LastAskTS(session string) (int64, error) {
	return lastAskTS(t.tx, session)
}

func lastAskTS(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, session string) (int64, error) {
	var ts int64
	err := q.QueryRow(`SELECT COALESCE(MAX(ts), 0) FROM feedback_asks WHERE session = ?`, session).Scan(&ts)
	return ts, err
}

// SessionProject is the project column of a session.
func (s *Store) SessionProject(session string) (string, error) {
	var p string
	err := s.db.QueryRow(`SELECT project FROM sessions WHERE id = ?`, session).Scan(&p)
	return p, err
}

func (t *Tx) InsertAsk(id, session string, ts int64, outcomeIDs []int64) error {
	if _, err := t.tx.Exec(`INSERT INTO feedback_asks(id, session, ts) VALUES (?, ?, ?)`, id, session, ts); err != nil {
		return err
	}
	for _, oid := range outcomeIDs {
		if _, err := t.tx.Exec(`INSERT INTO feedback_ask_rows(ask_id, outcome_id) VALUES (?, ?)`, id, oid); err != nil {
			return err
		}
	}
	return nil
}

// GetAsk returns an ask with all of its rows, resolved ones included. It wraps
// sql.ErrNoRows when the id is unknown.
func (t *Tx) GetAsk(id string) (*Ask, error) {
	a := &Ask{ID: id}
	if err := t.tx.QueryRow(`SELECT session, ts, answered FROM feedback_asks WHERE id = ?`, id).
		Scan(&a.Session, &a.TS, &a.Answered); err != nil {
		return nil, fmt.Errorf("feedback ask %s: %w", id, err)
	}
	rows, err := t.tx.Query(outcomeSelect+` JOIN feedback_ask_rows r ON r.outcome_id = o.id
		WHERE r.ask_id = ? ORDER BY o.id`, id)
	if err != nil {
		return nil, err
	}
	if a.Rows, err = flatOutcomes(rows); err != nil {
		return nil, err
	}
	return a, nil
}

func (t *Tx) AnswerAsk(id string) error {
	_, err := t.tx.Exec(`UPDATE feedback_asks SET answered = 1 WHERE id = ?`, id)
	return err
}

func flatOutcomes(rows *sql.Rows) ([]ScoreOutcome, error) {
	defer func() { _ = rows.Close() }()
	var out []ScoreOutcome
	for rows.Next() {
		o, err := scanOutcome(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
