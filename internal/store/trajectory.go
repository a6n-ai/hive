package store

import (
	"strings"

	"github.com/a6n-ai/inscribe/internal/trajectory"
	"github.com/a6n-ai/inscribe/model"
)

// TrajectoryHit is one stored trajectory v1 record whose JSON matched a query.
type TrajectoryHit struct {
	EventID     int64
	Role, Agent string
	Record      string
}

// InsertTrajectory stores recs for e. A digest that is already stored is skipped,
// so a replay of the same event does not duplicate the transcript.
func (t *Tx) InsertTrajectory(e *model.Event, recs []trajectory.Record) error {
	for _, r := range recs {
		if _, err := t.tx.Exec(`INSERT INTO trajectory_records(
			event_id, session, project, agent, role, tool_call_id, ok, ts, record, digest)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(digest) DO NOTHING`,
			e.ID, e.Session, e.Project, e.Agent, r.Role, nullable(r.ToolCallID), bit(r.OK), nullInt(r.TS), string(r.JSON), r.Digest); err != nil {
			return err
		}
	}
	return nil
}

// TrajectoryJSON returns one session's trajectory v1 records in write order.
func (s *Store) TrajectoryJSON(session string) ([]string, error) {
	rows, err := s.db.Query(`SELECT record FROM trajectory_records WHERE session = ? ORDER BY id`, session)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SearchTrajectory returns recent records for project whose stored JSON
// contains any of terms. The JSON is the same object Normalize writes.
func (s *Store) SearchTrajectory(project string, terms []string, limit int) ([]TrajectoryHit, error) {
	if project == "" || limit <= 0 {
		return nil, nil
	}
	var likes []string
	args := []any{project}
	seen := map[string]bool{}
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		likes = append(likes, `record LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(term)+"%")
		if len(likes) == 12 {
			break
		}
	}
	if len(likes) == 0 {
		return nil, nil
	}
	// Terms are bound. The SQL only concatenates "?" placeholders.
	//nolint:gosec // G202: only "?" placeholders are concatenated; every value is bound
	q := `SELECT event_id, role, agent, record FROM trajectory_records
		WHERE project = ? AND (` + strings.Join(likes, " OR ") + `) ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TrajectoryHit
	for rows.Next() {
		var h TrajectoryHit
		if err := rows.Scan(&h.EventID, &h.Role, &h.Agent, &h.Record); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// EvidencePair links a memory to an event it cites.
type EvidencePair struct {
	MemoryID string
	EventID  int64
}

// EvidencePairs returns the memory/event links for the given events.
func (s *Store) EvidencePairs(eventIDs []int64) ([]EvidencePair, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	q := `SELECT memory_id, event_id FROM memory_evidence WHERE event_id IN (` + placeholders(len(eventIDs)) + `)` //nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []EvidencePair
	for rows.Next() {
		var p EvidencePair
		if err := rows.Scan(&p.MemoryID, &p.EventID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func bit(ok *bool) any {
	if ok == nil {
		return nil
	}
	if *ok {
		return 1
	}
	return 0
}
