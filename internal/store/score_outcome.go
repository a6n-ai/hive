package store

import (
	"database/sql"

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
	PromptDigest      string
	ShownKey          string
	ShownText         string
	Reason            string
	LabelRule         string
	Label             *int
	Scale             float64
	// Ended is the session's end time, set only by OpenScoreGroups and LabeledScoreOutcomes.
	Ended int64
}

// OutcomeInput is one injection as recall shows it.
type OutcomeInput struct {
	MemoryID, Session, Agent                     string
	TS                                           int64
	Features                                     string
	Score                                        float64
	PromptDigest, ShownKey, ShownText, LabelRule string
}

// InsertScoreOutcome records one injection.
func (t *Tx) InsertScoreOutcome(in OutcomeInput) error {
	_, err := t.tx.Exec(`INSERT INTO score_outcomes(memory_id, session, agent, ts, features, score,
		prompt_digest, shown_key, shown_text, label_rule) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.MemoryID, in.Session, in.Agent, in.TS, in.Features, in.Score, in.PromptDigest, in.ShownKey, in.ShownText, in.LabelRule)
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

// ResolveScoreOutcome closes an outcome once. label nil means nothing to learn.
// It returns 0 when the row was already resolved.
func (t *Tx) ResolveScoreOutcome(id int64, label *int, reason string, scale float64, ts int64) (int64, error) {
	var v any
	if label != nil {
		v = *label
	}
	res, err := t.tx.Exec(`UPDATE score_outcomes SET label = ?, reason = ?, scale = ?, resolved = ?
		WHERE id = ? AND resolved IS NULL`, v, reason, scale, ts, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const outcomeSelect = `SELECT o.id, o.memory_id, o.session, o.agent, o.ts, o.features, o.score,
	o.prompt_digest, o.shown_key, o.shown_text, o.reason, o.label_rule, o.label, o.scale, COALESCE(s.ended, 0)
	FROM score_outcomes o LEFT JOIN sessions s ON s.id = o.session`

// OpenScoreGroups returns unresolved injections of ended sessions, grouped by
// (session, prompt_digest). At most maxGroups groups are returned, the oldest
// first, each with all of its rows.
func (s *Store) OpenScoreGroups(maxGroups int) ([][]ScoreOutcome, error) {
	if maxGroups <= 0 {
		maxGroups = 500
	}
	q := `WITH g AS (SELECT o.session, o.prompt_digest FROM score_outcomes o JOIN sessions s ON s.id = o.session
		WHERE o.resolved IS NULL AND s.ended IS NOT NULL
		GROUP BY o.session, o.prompt_digest ORDER BY MIN(o.id) LIMIT ?)
		` + outcomeSelect + ` JOIN g ON g.session = o.session AND g.prompt_digest = o.prompt_digest
		WHERE o.resolved IS NULL AND s.ended IS NOT NULL ORDER BY o.id`
	return s.queryGroups(q, maxGroups)
}

// LabeledScoreOutcomes returns the last maxGroups groups of rows labeled by
// labelRule with a reason that carries a signal, oldest first.
func (s *Store) LabeledScoreOutcomes(labelRule string, maxGroups int) ([][]ScoreOutcome, error) {
	if maxGroups <= 0 {
		maxGroups = 500
	}
	const labeled = `o.resolved IS NOT NULL AND o.label_rule = ?
		AND o.reason IN ('full_command', 'forgotten', 'corrected', 'failed', 'sibling')`
	q := `WITH g AS (SELECT o.session, o.prompt_digest FROM score_outcomes o WHERE ` + labeled + `
		GROUP BY o.session, o.prompt_digest ORDER BY MIN(o.id) DESC LIMIT ?)
		` + outcomeSelect + ` JOIN g ON g.session = o.session AND g.prompt_digest = o.prompt_digest
		WHERE ` + labeled + ` ORDER BY o.id`
	return s.queryGroups(q, labelRule, maxGroups, labelRule)
}

// RecentScoreGroups returns the maxGroups groups whose newest row is latest,
// oldest first, each with all of its rows.
func (s *Store) RecentScoreGroups(maxGroups int) ([][]ScoreOutcome, error) {
	q := `WITH g AS (SELECT o.session, o.prompt_digest FROM score_outcomes o
		GROUP BY o.session, o.prompt_digest ORDER BY MAX(o.id) DESC LIMIT ?)
		` + outcomeSelect + ` JOIN g ON g.session = o.session AND g.prompt_digest = o.prompt_digest ORDER BY o.id`
	return s.queryGroups(q, maxGroups)
}

// queryGroups runs a query of outcomeSelect rows ordered by id and groups them.
func (s *Store) queryGroups(q string, args ...any) ([][]ScoreOutcome, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type key struct{ session, digest string }
	index := map[key]int{}
	var out [][]ScoreOutcome
	for rows.Next() {
		o, err := scanOutcome(rows)
		if err != nil {
			return nil, err
		}
		k := key{o.Session, o.PromptDigest}
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], o)
	}
	return out, rows.Err()
}

// scanOutcome reads one outcomeSelect row.
func scanOutcome(rows *sql.Rows) (ScoreOutcome, error) {
	var o ScoreOutcome
	var label sql.NullInt64
	if err := rows.Scan(&o.ID, &o.MemoryID, &o.Session, &o.Agent, &o.TS, &o.Features, &o.Score,
		&o.PromptDigest, &o.ShownKey, &o.ShownText, &o.Reason, &o.LabelRule, &label, &o.Scale, &o.Ended); err != nil {
		return o, err
	}
	if label.Valid {
		v := int(label.Int64)
		o.Label = &v
	}
	return o, nil
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
