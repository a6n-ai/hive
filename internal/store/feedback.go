package store

import (
	"database/sql"
	"fmt"

	"github.com/a6n-ai/inscribe/model"
)

// unansweredRow matches an outcome no answered ask covers: unasked, or asked and still unanswered.
const unansweredRow = `NOT EXISTS (SELECT 1 FROM feedback_ask_rows r JOIN feedback_asks a ON a.id = r.ask_id
	WHERE r.outcome_id = score_outcomes.id AND a.answered = 1)`

// UnratedMemories returns the memories a session injected that still have an
// unresolved outcome no answered ask covers.
func (s *Store) UnratedMemories(session string) ([]model.Memory, error) {
	rows, err := s.db.Query(memorySelect+` WHERE id IN (SELECT memory_id FROM score_outcomes
		WHERE session = ? AND resolved IS NULL AND `+unansweredRow+`)`, session)
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

// ResolveSessionMemoryRows labels the unresolved outcomes of a memory in a session
// that no answered ask covers. It returns how many rows it resolved.
func (t *Tx) ResolveSessionMemoryRows(session, memoryID string, label int, reason string, scale float64, ts int64) (int64, error) {
	res, err := t.tx.Exec(`UPDATE score_outcomes SET label = ?, reason = ?, scale = ?, resolved = ?
		WHERE session = ? AND memory_id = ? AND resolved IS NULL AND `+unansweredRow,
		label, reason, scale, ts, session, memoryID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UnresolvedSessionRows counts a memory's unresolved outcomes in a session that no answered ask covers.
func (t *Tx) UnresolvedSessionRows(session, memoryID string) (int64, error) {
	var n int64
	err := t.tx.QueryRow(`SELECT COUNT(*) FROM score_outcomes WHERE session = ? AND memory_id = ? AND resolved IS NULL AND `+unansweredRow,
		session, memoryID).Scan(&n)
	return n, err
}

// UnresolveSessionMemoryRows reopens a memory's outcomes in a session that were
// resolved at resolvedAt with one of reasons, clearing their label, reason and scale.
func (t *Tx) UnresolveSessionMemoryRows(session, memoryID string, resolvedAt int64, reasons []string) error {
	args := []any{session, memoryID, resolvedAt}
	for _, r := range reasons {
		args = append(args, r)
	}
	_, err := t.tx.Exec(`UPDATE score_outcomes SET label = NULL, reason = '', scale = 1, resolved = NULL
		WHERE session = ? AND memory_id = ? AND resolved = ? AND reason IN (`+placeholders(len(reasons))+`)`, args...) //nolint:gosec // G202: only "?" placeholders are concatenated; every value is bound
	return err
}

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
