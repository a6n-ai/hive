package consolidate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
)

// RunReport is the outcome of one consolidation attempt.
type RunReport struct {
	RunID    string
	Status   string // ok, partial, failed, skipped (too few new events), or locked (another run holds the lock)
	Valid    []Op   // the ops that apply; a dry run applies none
	Rejected []Rejected
	Error    string
}

// Run consolidates the session's events since its last consolidation through
// runner. The model call runs outside any transaction. A dry run changes nothing.
func Run(ctx context.Context, st *store.Store, cfg config.Config, scrub func(string) string, runner Runner, session string, dryRun bool) (RunReport, error) {
	release, ok, err := Lock(filepath.Dir(st.Path()))
	if err != nil {
		return RunReport{}, err
	}
	if !ok {
		return RunReport{Status: "locked"}, nil
	}
	defer release()
	cursor, err := st.SessionCursor(session)
	if err != nil {
		return RunReport{}, err
	}
	n, err := st.CountEventsAfter(session, cursor)
	if err != nil {
		return RunReport{}, err
	}
	if n < cfg.Consolidate.MinEvents {
		return RunReport{Status: "skipped"}, nil
	}
	in, err := BuildInput(st, session, cursor, cfg.Consolidate.InputBudget, scrub)
	if err != nil {
		return RunReport{}, err
	}
	in.RunID = newRunID()
	started := time.Now()
	raw, err := runner.Run(ctx, Prompt(in))
	rec := store.RunRecord{ID: in.RunID, Session: session, Agent: in.Agent, Model: cfg.Consolidate.Model[in.Agent],
		Started: started.UnixMilli(), Ops: raw}
	var rep RunReport
	rep.RunID = in.RunID
	rec.Status, rep.Valid, rep.Rejected, rec.Error = judge(raw, err, in, scrub)
	rep.Status, rep.Error = rec.Status, rec.Error
	if dryRun {
		return rep, nil
	}
	rec.Finished = time.Now().UnixMilli()
	return rep, commit(ctx, st, in, rec, rep.Valid)
}

// judge turns a runner result into a run status, the ops that may apply, and the rejections.
func judge(raw string, runErr error, in Input, scrub func(string) string) (status string, valid []Op, rejected []Rejected, errText string) {
	if runErr != nil {
		return "failed", nil, nil, runErr.Error()
	}
	ops, err := ParseOps(raw)
	if err != nil {
		return "failed", nil, nil, err.Error()
	}
	valid, rejected = Validate(ops, in, scrub)
	if len(rejected) == 0 {
		return "ok", valid, nil, ""
	}
	reasons := make([]string, 0, len(rejected))
	for _, r := range rejected {
		reasons = append(reasons, r.Reason)
	}
	return "partial", valid, rejected, strings.Join(reasons, "; ")
}

// commit applies the valid ops, moves the session's cursor, and records the run,
// in one transaction. A failed run records only itself.
func commit(ctx context.Context, st *store.Store, in Input, rec store.RunRecord, valid []Op) error {
	now := time.Now().UnixMilli()
	return st.Write(ctx, func(tx *store.Tx) error {
		if rec.Status != "failed" {
			if err := applyTx(tx, rec.ID, valid, now); err != nil {
				return err
			}
			if err := tx.SetSessionCursor(in.Session, in.MaxEventID, now); err != nil {
				return err
			}
		}
		return tx.InsertRun(rec)
	})
}

func newRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "run_" + hex.EncodeToString(b)
}
