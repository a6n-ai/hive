package store

import (
	"database/sql"
	"errors"

	"github.com/a6n-ai/inscribe/model"
)

// RunRecord is one consolidation run's row in consolidation_runs.
type RunRecord struct {
	ID, Session, Agent, Model string
	Started, Finished         int64
	Status, Error, Ops        string
}

// AuditRow is one memory change made by a consolidation run. Before is empty
// when the run created the memory.
type AuditRow struct {
	ID                  int64
	RunID, Op, MemoryID string
	Before, After       string
	TS                  int64
}

// EventsAfter returns up to limit of a session's events with id above afterID,
// oldest first. When more exist, the newest are kept.
func (s *Store) EventsAfter(session string, afterID int64, limit int) ([]model.Event, error) {
	rows, err := s.db.Query(`SELECT * FROM (`+eventSelect+` WHERE e.session = ? AND e.id > ? ORDER BY e.id DESC LIMIT ?) ORDER BY id`,
		session, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// CountEventsAfter returns how many of a session's events have id above afterID.
func (s *Store) CountEventsAfter(session string, afterID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM events WHERE session = ? AND id > ?`, session, afterID).Scan(&n)
	return n, err
}

// SessionCursor returns the id of the session's last consolidated event, or 0.
func (s *Store) SessionCursor(session string) (int64, error) {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT last_consolidated_event_id FROM sessions WHERE id = ?`, session).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v.Int64, err
}

// MemoriesCitingEvents returns the memories that cite any of the given events.
func (s *Store) MemoriesCitingEvents(eventIDs []int64) ([]model.Memory, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	rows, err := s.db.Query(memorySelect+` WHERE id IN (SELECT memory_id FROM memory_evidence WHERE event_id IN (`+ //nolint:gosec // G202: only "?" placeholders are concatenated
		placeholders(len(eventIDs))+`))`, args...)
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

// RunStatus returns a consolidation run's status, or "" when no such run exists.
func (s *Store) RunStatus(runID string) (string, error) {
	var st sql.NullString
	err := s.db.QueryRow(`SELECT status FROM consolidation_runs WHERE id = ?`, runID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return st.String, err
}

// AuditRows returns a run's audit rows, in the order they were written.
func (s *Store) AuditRows(runID string) ([]AuditRow, error) {
	rows, err := s.db.Query(`SELECT id, run_id, op, memory_id, before, after, ts FROM audit WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditRow
	for rows.Next() {
		var a AuditRow
		var before, after sql.NullString
		if err := rows.Scan(&a.ID, &a.RunID, &a.Op, &a.MemoryID, &before, &after, &a.TS); err != nil {
			return nil, err
		}
		a.Before, a.After = before.String, after.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetSessionCursor records that a session's events up to eventID are consolidated.
func (t *Tx) SetSessionCursor(session string, eventID, ts int64) error {
	_, err := t.tx.Exec(`UPDATE sessions SET last_consolidated_event_id = ?, consolidated_at = ? WHERE id = ?`, eventID, ts, session)
	return err
}

// InsertRun records a consolidation run.
func (t *Tx) InsertRun(r RunRecord) error {
	_, err := t.tx.Exec(`INSERT INTO consolidation_runs(id, session, agent, model, started, finished, status, error, ops)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Session, r.Agent, r.Model, r.Started, r.Finished, r.Status, nullable(r.Error), nullable(r.Ops))
	return err
}

// InsertAudit records one memory change made by a run.
func (t *Tx) InsertAudit(a AuditRow) error {
	_, err := t.tx.Exec(`INSERT INTO audit(run_id, op, memory_id, before, after, ts) VALUES (?, ?, ?, ?, ?, ?)`,
		a.RunID, a.Op, a.MemoryID, nullable(a.Before), nullable(a.After), a.TS)
	return err
}

// GetMemory returns a memory by id inside the transaction, or nil when absent.
func (t *Tx) GetMemory(id string) (*model.Memory, error) {
	return absentIsNil(scanMemory(t.tx.QueryRow(memorySelect+` WHERE id = ?`, id)))
}

// MemoryByKey returns the memory for a (type, scope_kind, scope, key), or nil when absent.
func (t *Tx) MemoryByKey(typ model.MemoryType, sk model.ScopeKind, scope, key string) (*model.Memory, error) {
	return absentIsNil(scanMemory(t.tx.QueryRow(memorySelect+` WHERE type=? AND scope_kind=? AND scope=? AND key=?`,
		string(typ), string(sk), scope, key)))
}

func absentIsNil(m *model.Memory, err error) (*model.Memory, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// PutMemory writes every mutable field of m back to its row.
func (t *Tx) PutMemory(m *model.Memory) error {
	body, err := marshal(m.Body)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(`UPDATE memories SET type=?, scope_kind=?, scope=?, key=?, body=?, text=?, terms=?, count=?,
		confidence=?, status=?, superseded_by=?, last_seen=?, last_used=? WHERE id=?`,
		string(m.Type), string(m.ScopeKind), m.Scope, m.Key, body, m.Text, termsOf(m.Text), m.Count,
		m.Confidence, string(m.Status), nullable(m.SupersededBy), m.LastSeen, nullInt(m.LastUsed), m.ID)
	return err
}

// EvidenceIDs returns the event ids cited by a memory, ascending.
func (t *Tx) EvidenceIDs(memoryID string) ([]int64, error) {
	rows, err := t.tx.Query(`SELECT event_id FROM memory_evidence WHERE memory_id = ? ORDER BY event_id`, memoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetEvidence replaces a memory's evidence with exactly ids.
func (t *Tx) SetEvidence(memoryID string, ids []int64) error {
	if _, err := t.tx.Exec(`DELETE FROM memory_evidence WHERE memory_id = ?`, memoryID); err != nil {
		return err
	}
	for _, id := range ids {
		if err := t.AddEvidence(memoryID, id); err != nil {
			return err
		}
	}
	return nil
}

// MoveEvidence reassigns every evidence link of from to to.
func (t *Tx) MoveEvidence(from, to string) error {
	if _, err := t.tx.Exec(`UPDATE OR IGNORE memory_evidence SET memory_id = ? WHERE memory_id = ?`, to, from); err != nil {
		return err
	}
	_, err := t.tx.Exec(`DELETE FROM memory_evidence WHERE memory_id = ?`, from)
	return err
}
