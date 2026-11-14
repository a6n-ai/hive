package stats

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/a6n-ai/inscribe/internal/importer"
	"github.com/a6n-ai/inscribe/internal/store"
)

// IngestUsage reads the transcripts of ended Claude Code sessions that have not
// been ingested, or had events since, and stores their token usage. A session
// that fails to read is left for the next run; the rest still go through, and
// the errors are joined.
func IngestUsage(st *store.Store, limit int, now time.Time) (int, error) {
	targets, err := st.SessionsToIngest("claude-code", limit)
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, t := range targets {
		if err := ingestOne(st, t, now); err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", t.Session, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

func ingestOne(st *store.Store, t store.IngestTarget, now time.Time) error {
	var turns []store.UsageTurn
	assistant, lacking := 0, 0
	var prevTS int64
	_, malformed, err := importer.ReadTranscript(t.Path, func(l importer.RawLine) error {
		if l.Type != "assistant" {
			return nil
		}
		assistant++
		if l.Message.ID == "" || l.Message.Usage == nil {
			lacking++
			return nil
		}
		ts := prevTS
		if p, perr := time.Parse(time.RFC3339Nano, l.Timestamp); perr == nil {
			ts = p.UnixMilli()
		}
		prevTS = ts
		u := l.Message.Usage
		turns = append(turns, store.UsageTurn{Session: t.Session, MessageID: l.Message.ID, Model: l.Message.Model, TS: ts,
			Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheCreation: u.CacheCreationInputTokens})
		return nil
	})
	status := "ok"
	switch {
	case errors.Is(err, os.ErrNotExist):
		status, turns = "missing", nil
	case err != nil:
		return err
	case (assistant+malformed) > 0 && (lacking+malformed)*2 > assistant+malformed:
		status = "unparsed"
	}
	return st.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.UpsertUsageTurns(turns); err != nil {
			return err
		}
		return tx.MarkIngested(t.Session, status, now.UnixMilli())
	})
}
