package store

import (
	"encoding/json"
)

type Window struct {
	From, To int64
	Project  string
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
	Input, Output, CacheRead, CacheCreation int
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
