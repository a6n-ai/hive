package consolidate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// addConfidence is the confidence of a memory the agent adds.
const addConfidence = 0.7

// snapshot is a memory's changeable state, as the audit log stores it. Undo
// compares a current snapshot with the one a run left behind.
type snapshot struct {
	Type         model.MemoryType `json:"type"`
	ScopeKind    model.ScopeKind  `json:"scope_kind"`
	Scope        string           `json:"scope"`
	Key          string           `json:"key"`
	Text         string           `json:"text"`
	Body         map[string]any   `json:"body"`
	Count        int              `json:"count"`
	Confidence   float64          `json:"confidence"`
	Status       model.Status     `json:"status"`
	SupersededBy string           `json:"superseded_by"`
	Evidence     []int64          `json:"evidence"`
}

// snapshotJSON returns the memory's snapshot as JSON; equal memories give equal strings.
func snapshotJSON(tx *store.Tx, m *model.Memory) (string, error) {
	ev, err := tx.EvidenceIDs(m.ID)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(snapshot{Type: m.Type, ScopeKind: m.ScopeKind, Scope: m.Scope, Key: m.Key, Text: m.Text,
		Body: m.Body, Count: m.Count, Confidence: m.Confidence, Status: m.Status, SupersededBy: m.SupersededBy, Evidence: ev})
	return string(b), err
}

// Apply makes the valid ops' changes in one transaction, with one audit row per
// changed memory. It returns the ops skipped because the state changed since review.
func Apply(ctx context.Context, st *store.Store, runID string, valid []Op) ([]Rejected, error) {
	var skipped []Rejected
	err := st.Write(ctx, func(tx *store.Tx) error {
		var err error
		skipped, err = applyTx(tx, runID, valid, time.Now().UnixMilli())
		return err
	})
	return skipped, err
}

func applyTx(tx *store.Tx, runID string, ops []Op, now int64) ([]Rejected, error) {
	a := applier{tx: tx, runID: runID, now: now}
	var skipped []Rejected
	for _, op := range ops {
		reason, err := a.apply(op)
		if err != nil {
			return skipped, fmt.Errorf("consolidate: %s: %w", op.Op, err)
		}
		if reason != "" {
			skipped = append(skipped, Rejected{Op: op, Reason: reason})
		}
	}
	return skipped, nil
}

type applier struct {
	tx    *store.Tx
	runID string
	now   int64
}

// apply makes one op's change. A non-empty reason with a nil error means the op
// was skipped: the memory changed since the review.
func (a applier) apply(op Op) (string, error) {
	reason, err := a.recheck(op)
	if reason != "" || err != nil {
		return reason, err
	}
	return "", a.dispatch(op)
}

// recheck re-reads, inside the transaction, what the review relied on.
func (a applier) recheck(op Op) (string, error) {
	switch op.Op {
	case "promote":
		m, err := a.tx.GetMemory(op.ID)
		switch {
		case err != nil:
			return "", err
		case m == nil:
			return "memory no longer exists", nil
		case m.Status != model.Candidate:
			return "memory is no longer a candidate", nil
		}
	case "add":
		existing, err := a.tx.MemoryByKey(model.MemoryType(op.Type), model.ScopeKind(op.ScopeKind), op.Scope, op.Key)
		if err != nil {
			return "", err
		}
		if existing != nil && existing.ID != op.ID {
			return "key was created after the review", nil
		}
	}
	return "", nil
}

func (a applier) dispatch(op Op) error {
	switch op.Op {
	case "merge":
		return a.merge(op)
	case "promote":
		return a.promote(op)
	case "reject":
		return a.setStatus(op.Op, op.ID, model.Forgotten)
	case "stale":
		return a.setStatus(op.Op, op.ID, model.Stale)
	case "supersede":
		return a.supersede(op)
	case "add":
		return a.add(op)
	}
	return fmt.Errorf("unknown op %q", op.Op)
}

// merge moves the dropped memories' evidence and counts to keep, and marks them superseded.
func (a applier) merge(op Op) error {
	keep, err := a.get(op.Keep)
	if err != nil {
		return err
	}
	keepBefore, err := snapshotJSON(a.tx, keep)
	if err != nil {
		return err
	}
	for _, id := range op.Drop {
		d, err := a.get(id)
		if err != nil {
			return err
		}
		before, err := snapshotJSON(a.tx, d)
		if err != nil {
			return err
		}
		if err := a.tx.MoveEvidence(d.ID, keep.ID); err != nil {
			return err
		}
		keep.Count += d.Count
		d.Status, d.SupersededBy = model.Superseded, keep.ID
		if err := a.write("merge", d, before); err != nil {
			return err
		}
	}
	return a.write("merge", keep, keepBefore)
}

func (a applier) promote(op Op) error {
	m, err := a.get(op.ID)
	if err != nil {
		return err
	}
	before, err := snapshotJSON(a.tx, m)
	if err != nil {
		return err
	}
	m.Status = model.Active
	if op.ScopeKind != "" {
		m.ScopeKind = model.ScopeKind(op.ScopeKind)
	}
	if op.Scope != "" {
		m.Scope = op.Scope
	}
	return a.write("promote", m, before)
}

func (a applier) setStatus(opName, id string, s model.Status) error {
	m, err := a.get(id)
	if err != nil {
		return err
	}
	before, err := snapshotJSON(a.tx, m)
	if err != nil {
		return err
	}
	m.Status = s
	return a.write(opName, m, before)
}

func (a applier) supersede(op Op) error {
	m, err := a.get(op.Old)
	if err != nil {
		return err
	}
	before, err := snapshotJSON(a.tx, m)
	if err != nil {
		return err
	}
	m.Status, m.SupersededBy = model.Superseded, op.New
	return a.write("supersede", m, before)
}

// add upserts the memory and cites its evidence. An existing memory is updated, not replaced.
func (a applier) add(op Op) error {
	existing, err := a.tx.MemoryByKey(model.MemoryType(op.Type), model.ScopeKind(op.ScopeKind), op.Scope, op.Key)
	if err != nil {
		return err
	}
	var before string
	if existing != nil {
		if before, err = snapshotJSON(a.tx, existing); err != nil {
			return err
		}
	}
	m, err := a.tx.UpsertMemory(&model.Memory{Type: model.MemoryType(op.Type), ScopeKind: model.ScopeKind(op.ScopeKind),
		Scope: op.Scope, Key: op.Key, Body: op.Body, Text: op.Text, Confidence: addConfidence,
		Status: model.Active, FirstSeen: a.now, LastSeen: a.now}, nil, 0)
	if err != nil {
		return err
	}
	for _, ev := range op.Evidence {
		if err := a.tx.AddEvidence(m.ID, ev); err != nil {
			return err
		}
	}
	return a.write("add", m, before)
}

func (a applier) get(id string) (*model.Memory, error) {
	m, err := a.tx.GetMemory(id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("memory %q not found", id)
	}
	return m, nil
}

// write saves m and records the audit row: before (empty when the run created m) and the state now.
func (a applier) write(op string, m *model.Memory, before string) error {
	if err := a.tx.PutMemory(m); err != nil {
		return err
	}
	// Re-read so "after" is in the same form undo reads back from the row.
	saved, err := a.get(m.ID)
	if err != nil {
		return err
	}
	after, err := snapshotJSON(a.tx, saved)
	if err != nil {
		return err
	}
	return a.tx.InsertAudit(store.AuditRow{RunID: a.runID, Op: op, MemoryID: m.ID, Before: before, After: after, TS: a.now})
}
