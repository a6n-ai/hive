package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/a6n-ai/inscribe/model"
)

type Window struct {
	From    int64  `json:"from"`
	To      int64  `json:"to"`
	Project string `json:"project"`
}

type InjectionInput struct {
	TS                                  int64
	Session, Agent, Project, Hook, Tool string
	LineCount, EstTokens                int
	MemoryIDs                           []string
	Holdout                             bool
	PromptDigest                        string
}

type Injection struct {
	InjectionInput
	ID int64
}

type SessionLineCounts struct {
	Sessions, Hits, Shown, InjectedTokens, NextTryFixes, AgentAnswered, AgentHelpful int
}

// InsertInjection records one injection shown to an agent.
func (t *Tx) InsertInjection(in InjectionInput) error {
	ids, err := json.Marshal(append([]string{}, in.MemoryIDs...))
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(`INSERT INTO injections(ts, session, agent, project, hook, tool, line_count, est_tokens, memory_ids, holdout, prompt_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.TS, in.Session, in.Agent, in.Project, in.Hook, in.Tool, in.LineCount, in.EstTokens, string(ids), in.Holdout, in.PromptDigest)
	return err
}

// InjectionsIn returns injections in [w.From, w.To) oldest first; an empty Project means all.
func (s *Store) InjectionsIn(w Window) ([]Injection, error) {
	rows, err := s.db.Query(`SELECT id, ts, session, agent, project, hook, tool, line_count, est_tokens, memory_ids, holdout, prompt_digest
		FROM injections WHERE ts >= ? AND ts < ? AND (? = '' OR project = ?) ORDER BY ts, id`, w.From, w.To, w.Project, w.Project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Injection
	for rows.Next() {
		var in Injection
		var ids string
		if err := rows.Scan(&in.ID, &in.TS, &in.Session, &in.Agent, &in.Project, &in.Hook, &in.Tool, &in.LineCount, &in.EstTokens, &ids, &in.Holdout, &in.PromptDigest); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(ids), &in.MemoryIDs); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SessionLineCounts counts one project's finished sessions, outcomes and injections since from.
func (s *Store) SessionLineCounts(project string, from int64) (SessionLineCounts, error) {
	var c SessionLineCounts
	err := s.db.QueryRow(`WITH o AS (SELECT coalesce(sum(o.label = 1), 0) hits, count(*) shown,
			coalesce(sum(o.reason IN ('agent', 'agent_wrong')), 0) answered,
			coalesce(sum(o.label = 1 AND o.reason IN ('agent', 'agent_wrong')), 0) helpful
			FROM score_outcomes o JOIN sessions s ON s.id = o.session WHERE s.project = ?1 AND o.ts >= ?2)
		SELECT
		(SELECT count(*) FROM sessions WHERE project = ?1 AND ended IS NOT NULL AND ended >= ?2),
		o.hits, o.shown,
		(SELECT coalesce(sum(est_tokens), 0) FROM injections WHERE project = ?1 AND ts >= ?2),
		(SELECT count(*) FROM injections i WHERE i.project = ?1 AND i.ts >= ?2 AND i.hook = 'PostToolUseFailure' AND
			(SELECT json_extract(e.payload, '$.error') IS NULL FROM events e WHERE e.session = i.session AND e.kind = 'post_tool' AND e.tool = i.tool AND e.ts > i.ts ORDER BY e.ts LIMIT 1)),
		o.answered, o.helpful FROM o`,
		project, from).Scan(&c.Sessions, &c.Hits, &c.Shown, &c.InjectedTokens, &c.NextTryFixes, &c.AgentAnswered, &c.AgentHelpful)
	return c, err
}

type UsageTurn struct {
	Session, MessageID, Model               string
	TS                                      int64
	Input, Output, CacheRead, CacheCreation int
}

type UsageSum struct {
	Input         int `json:"input"`
	Output        int `json:"output"`
	CacheRead     int `json:"cache_read"`
	CacheCreation int `json:"cache_creation"`
}

// UpsertUsageTurns stores one row per (session, message id). A message that
// shows up again keeps the larger value of each token field, since a streamed
// message's counts only grow.
func (t *Tx) UpsertUsageTurns(turns []UsageTurn) (int, error) {
	for _, u := range turns {
		if _, err := t.tx.Exec(`INSERT INTO usage_turns(session, message_id, ts, model, input, output, cache_read, cache_creation)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session, message_id) DO UPDATE SET
			input = max(input, excluded.input), output = max(output, excluded.output),
			cache_read = max(cache_read, excluded.cache_read), cache_creation = max(cache_creation, excluded.cache_creation)`,
			u.Session, u.MessageID, u.TS, u.Model, u.Input, u.Output, u.CacheRead, u.CacheCreation); err != nil {
			return 0, err
		}
	}
	return len(turns), nil
}

// MarkIngested records that a session's usage was read, with the outcome status.
func (t *Tx) MarkIngested(session, status string, ts int64) error {
	_, err := t.tx.Exec(`UPDATE sessions SET usage_status = ?, usage_ingested_at = ? WHERE id = ?`, status, ts, session)
	return err
}

// UsageInRange sums one session's usage with ts in [from, to).
func (s *Store) UsageInRange(session string, from, to int64) (UsageSum, error) {
	var u UsageSum
	err := s.db.QueryRow(`SELECT coalesce(sum(input), 0), coalesce(sum(output), 0), coalesce(sum(cache_read), 0), coalesce(sum(cache_creation), 0)
		FROM usage_turns WHERE session = ? AND ts >= ? AND ts < ?`, session, from, to).
		Scan(&u.Input, &u.Output, &u.CacheRead, &u.CacheCreation)
	return u, err
}

// UsageTotals sums usage with ts in [w.From, w.To); an empty Project means all.
func (s *Store) UsageTotals(w Window) (UsageSum, error) {
	var u UsageSum
	err := s.db.QueryRow(`SELECT coalesce(sum(u.input), 0), coalesce(sum(u.output), 0), coalesce(sum(u.cache_read), 0), coalesce(sum(u.cache_creation), 0)
		FROM usage_turns u JOIN sessions s ON s.id = u.session
		WHERE u.ts >= ? AND u.ts < ? AND (? = '' OR s.project = ?)`, w.From, w.To, w.Project, w.Project).
		Scan(&u.Input, &u.Output, &u.CacheRead, &u.CacheCreation)
	return u, err
}

// UsageStatusCounts counts sessions ended in the window whose usage could not be read.
func (s *Store) UsageStatusCounts(w Window) (missing, unparsed int, err error) {
	err = s.db.QueryRow(`SELECT coalesce(sum(usage_status = 'missing'), 0), coalesce(sum(usage_status = 'unparsed'), 0)
		FROM sessions WHERE ended >= ? AND ended < ? AND (? = '' OR project = ?)`, w.From, w.To, w.Project, w.Project).
		Scan(&missing, &unparsed)
	return missing, unparsed, err
}

// DeleteOldAnalytics deletes up to limit injections and up to limit usage rows
// older than cutoff. Usage of a session that owns evidence for a live memory stays.
func (t *Tx) DeleteOldAnalytics(cutoff int64, limit int) (injections, usage int64, err error) {
	res, err := t.tx.Exec(`DELETE FROM injections WHERE id IN (SELECT id FROM injections WHERE ts < ? LIMIT ?)`, cutoff, limit)
	if err != nil {
		return 0, 0, err
	}
	if injections, err = res.RowsAffected(); err != nil {
		return 0, 0, err
	}
	res, err = t.tx.Exec(`DELETE FROM usage_turns WHERE rowid IN (SELECT rowid FROM usage_turns u WHERE u.ts < ? AND NOT EXISTS (
		SELECT 1 FROM memory_evidence me JOIN events e ON e.id = me.event_id JOIN memories m ON m.id = me.memory_id
		WHERE e.session = u.session AND m.status <> 'forgotten') LIMIT ?)`, cutoff, limit)
	if err != nil {
		return 0, 0, err
	}
	usage, err = res.RowsAffected()
	return injections, usage, err
}

// PromptIndex counts a session's prompt events before ts.
func (s *Store) PromptIndex(session string, before int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM events WHERE session = ? AND kind = 'prompt' AND ts < ?`, session, before).Scan(&n)
	return n, err
}

// EventsIn returns one session's events with from <= ts <= to, oldest first.
func (s *Store) EventsIn(session string, from, to int64) ([]model.Event, error) {
	rows, err := s.db.Query(eventSelect+` WHERE e.session = ? AND e.ts >= ? AND e.ts <= ? ORDER BY e.ts, e.id`, session, from, to)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

type Hit struct {
	MemoryID, Session, Reason string
	TS                        int64
}

// HitsIn returns outcomes labeled 1 with ts in [w.From, w.To); an empty Project means all.
func (s *Store) HitsIn(w Window) ([]Hit, error) {
	rows, err := s.db.Query(`SELECT o.memory_id, o.session, o.reason, o.ts
		FROM score_outcomes o JOIN sessions s ON s.id = o.session
		WHERE o.label = 1 AND o.ts >= ? AND o.ts < ? AND (? = '' OR s.project = ?)
		ORDER BY o.ts, o.id`, w.From, w.To, w.Project, w.Project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.MemoryID, &h.Session, &h.Reason, &h.TS); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type OutcomeCount struct {
	Reason string
	Label  int
	N      int
}

// OutcomeCounts counts outcomes in the window by reason and label; Label is -1 for unlabeled.
func (s *Store) OutcomeCounts(w Window) ([]OutcomeCount, error) {
	rows, err := s.db.Query(`SELECT o.reason, coalesce(o.label, -1), count(*)
		FROM score_outcomes o JOIN sessions s ON s.id = o.session
		WHERE o.ts >= ? AND o.ts < ? AND (? = '' OR s.project = ?)
		GROUP BY 1, 2 ORDER BY 1, 2`, w.From, w.To, w.Project, w.Project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutcomeCount
	for rows.Next() {
		var c OutcomeCount
		if err := rows.Scan(&c.Reason, &c.Label, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MemoryKeyText returns a memory's key and text, or "" for both when it is gone.
func (s *Store) MemoryKeyText(id string) (key, text string, err error) {
	err = s.db.QueryRow(`SELECT key, text FROM memories WHERE id = ?`, id).Scan(&key, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return key, text, err
}

type PromptRef struct {
	EventID, TS             int64
	Session, Digest         string
	NextPromptTS, TurnEndTS int64
}

// PromptsIn returns prompt events in [w.From, w.To) oldest first, each with the
// ts of the next prompt and next turn_end in its session (0 when absent).
func (s *Store) PromptsIn(w Window) ([]PromptRef, error) {
	rows, err := s.db.Query(`SELECT e.id, e.ts, e.session, e.digest,
		coalesce((SELECT min(n.ts) FROM events n WHERE n.session = e.session AND n.kind = 'prompt' AND n.ts > e.ts), 0),
		coalesce((SELECT min(n.ts) FROM events n WHERE n.session = e.session AND n.kind = 'turn_end' AND n.ts > e.ts), 0)
		FROM events e WHERE e.kind = 'prompt' AND e.ts >= ? AND e.ts < ? AND (? = '' OR e.project = ?) ORDER BY e.ts, e.id`,
		w.From, w.To, w.Project, w.Project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PromptRef
	for rows.Next() {
		var p PromptRef
		if err := rows.Scan(&p.EventID, &p.TS, &p.Session, &p.Digest, &p.NextPromptTS, &p.TurnEndTS); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AskCounts counts feedback asks made in the window and how many were answered.
func (s *Store) AskCounts(w Window) (asked, answered int, err error) {
	err = s.db.QueryRow(`SELECT count(*), coalesce(sum(a.answered), 0)
		FROM feedback_asks a JOIN sessions s ON s.id = a.session
		WHERE a.ts >= ? AND a.ts < ? AND (? = '' OR s.project = ?)`, w.From, w.To, w.Project, w.Project).Scan(&asked, &answered)
	return asked, answered, err
}

// MemoryCounts counts all memories by status and by type.
func (s *Store) MemoryCounts() (byStatus, byType map[string]int, err error) {
	count := func(col string) (map[string]int, error) {
		rows, err := s.db.Query(`SELECT ` + col + `, count(*) FROM memories GROUP BY 1`) //nolint:gosec // G202: col is one of two fixed column names
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		out := map[string]int{}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, rows.Err()
	}
	if byStatus, err = count("status"); err != nil {
		return nil, nil, err
	}
	byType, err = count("type")
	return byStatus, byType, err
}

type Penalty struct {
	MemoryID string  `json:"memory_id"`
	Penalty  float64 `json:"penalty"`
}

// Penalized returns up to limit memories with a penalty above 0, largest first.
// A non-empty project keeps the global and that project's memories.
func (s *Store) Penalized(limit int, project string) ([]Penalty, error) {
	cond, args := filterSQL("memories", ListFilter{Project: project})
	rows, err := s.db.Query(`SELECT id, penalty FROM memories WHERE penalty > 0`+cond+` ORDER BY penalty DESC, id LIMIT ?`, append(args, limit)...) //nolint:gosec // G202: cond comes from filterSQL and binds values as args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Penalty
	for rows.Next() {
		var p Penalty
		if err := rows.Scan(&p.MemoryID, &p.Penalty); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActiveMemoryIDs returns the ids of active memories. A non-empty project keeps
// the global and that project's memories.
func (s *Store) ActiveMemoryIDs(project string) ([]string, error) {
	cond, args := filterSQL("memories", ListFilter{Project: project})
	rows, err := s.db.Query(`SELECT id FROM memories WHERE status = 'active'`+cond+` ORDER BY id`, args...) //nolint:gosec // G202: cond comes from filterSQL and binds values as args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// StoreCounts holds the row counts shown under store footprint and corrections.
// Events and Trajectory cover the whole store; Corrections and Sessions the window.
type StoreCounts struct {
	Events, Trajectory, Corrections, Sessions int
}

func (s *Store) StoreCounts(w Window) (StoreCounts, error) {
	var c StoreCounts
	err := s.db.QueryRow(`SELECT (SELECT count(*) FROM events), (SELECT count(*) FROM trajectory_records),
		(SELECT count(*) FROM events WHERE kind = 'correction' AND ts >= ?1 AND ts < ?2 AND (?3 = '' OR project = ?3)),
		(SELECT count(*) FROM sessions WHERE last_event >= ?1 AND last_event < ?2 AND (?3 = '' OR project = ?3))`,
		w.From, w.To, w.Project).Scan(&c.Events, &c.Trajectory, &c.Corrections, &c.Sessions)
	return c, err
}

// NextTryCounts counts error-fix injections in the window and how many were
// followed by a successful call of the same tool.
func (s *Store) NextTryCounts(w Window) (fixes, shown int, err error) {
	err = s.db.QueryRow(`SELECT count(*), coalesce(sum((SELECT json_extract(e.payload, '$.error') IS NULL FROM events e
			WHERE e.session = i.session AND e.kind = 'post_tool' AND e.tool = i.tool AND e.ts > i.ts ORDER BY e.ts LIMIT 1)), 0)
		FROM injections i WHERE i.hook = 'PostToolUseFailure' AND i.ts >= ?1 AND i.ts < ?2 AND (?3 = '' OR i.project = ?3)`,
		w.From, w.To, w.Project).Scan(&shown, &fixes)
	return fixes, shown, err
}

// SessionsIn returns the ids of sessions with events in [w.From, w.To); an empty Project means all.
func (s *Store) SessionsIn(w Window) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM sessions WHERE last_event >= ? AND started < ? AND (? = '' OR project = ?) ORDER BY id`,
		w.From, w.To, w.Project, w.Project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
