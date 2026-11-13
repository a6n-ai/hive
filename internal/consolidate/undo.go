package consolidate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// UndoReport counts what an undo restored and names the memories it left alone.
type UndoReport struct {
	Restored int
	Skipped  []string
}

// Undo reverses a run from its audit rows, newest first. A memory changed since
// the run is skipped, not overwritten. Running undo twice skips what it already reversed.
func Undo(ctx context.Context, st *store.Store, runID string) (UndoReport, error) {
	status, err := st.RunStatus(runID)
	if err != nil {
		return UndoReport{}, err
	}
	if status == "" {
		return UndoReport{}, fmt.Errorf("consolidate: no run %q", runID)
	}
	session, err := st.RunSession(runID)
	if err != nil {
		return UndoReport{}, err
	}
	rows, err := st.AuditRows(runID)
	if err != nil {
		return UndoReport{}, err
	}
	var rep UndoReport
	err = st.Write(ctx, func(tx *store.Tx) error {
		rep = UndoReport{}
		for i := len(rows) - 1; i >= 0; i-- {
			if err := undoRow(tx, session, rows[i], &rep); err != nil {
				return err
			}
		}
		return nil
	})
	return rep, err
}

func undoRow(tx *store.Tx, session string, r store.AuditRow, rep *UndoReport) error {
	cur, err := tx.GetMemory(r.MemoryID)
	if err != nil {
		return err
	}
	if cur == nil {
		rep.Skipped = append(rep.Skipped, r.MemoryID)
		return nil
	}
	now, err := snapshotOf(tx, cur)
	if err != nil {
		return err
	}
	var after snapshot
	if err := json.Unmarshal([]byte(r.After), &after); err != nil {
		return err
	}
	if after.Penalty == nil {
		now.Penalty = nil // written before penalties were snapshotted
	}
	b, err := json.Marshal(now)
	if err != nil {
		return err
	}
	if string(b) != r.After {
		rep.Skipped = append(rep.Skipped, r.MemoryID)
		return nil
	}
	if r.Before == "" {
		if err := tx.DeleteMemory(cur.ID); err != nil {
			return err
		}
	} else if err := restore(tx, cur, r.Before); err != nil {
		return err
	}
	if r.Op == "rate" {
		if err := tx.UnresolveSessionMemoryRows(session, r.MemoryID, []string{"review", "review_wrong"}); err != nil {
			return err
		}
	}
	rep.Restored++
	return nil
}

// restore writes a snapshot's state back onto m; last_seen and last_used stay as they are.
// A snapshot without a penalty leaves m's penalty as it is.
func restore(tx *store.Tx, m *model.Memory, before string) error {
	var s snapshot
	if err := json.Unmarshal([]byte(before), &s); err != nil {
		return err
	}
	m.Type, m.ScopeKind, m.Scope, m.Key = s.Type, s.ScopeKind, s.Scope, s.Key
	m.Text, m.Body, m.Count, m.Confidence = s.Text, s.Body, s.Count, s.Confidence
	m.Status, m.SupersededBy = s.Status, s.SupersededBy
	if s.Penalty != nil {
		m.Penalty = *s.Penalty
	}
	if err := tx.PutMemory(m); err != nil {
		return err
	}
	return tx.SetEvidence(m.ID, s.Evidence)
}
