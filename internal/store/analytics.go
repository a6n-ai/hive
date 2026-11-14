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
