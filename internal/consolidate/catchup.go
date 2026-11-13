package consolidate

import (
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
)

const (
	idleAfter     = time.Hour          // silence before a session counts as ended
	maxIdleAge    = 7 * 24 * time.Hour // older sessions are left alone
	retryAfter    = 24 * time.Hour     // a failed attempt is not retried sooner
	maxCatchUpRun = 3                  // sessions queued per catch-up
)

// Due returns up to maxCatchUpRun of the agent's idle sessions, newest first,
// that have at least MinEvents unconsolidated events and no attempt in the last day.
func Due(st *store.Store, cfg config.Config, agent string, now time.Time) ([]string, error) {
	idle, err := st.IdleSessions(agent, now.Add(-maxIdleAge).UnixMilli(), now.Add(-idleAfter).UnixMilli())
	if err != nil {
		return nil, err
	}
	var due []string
	for _, id := range idle {
		if len(due) == maxCatchUpRun {
			break
		}
		ok, err := Pending(st, cfg, id, now)
		if err != nil {
			return nil, err
		}
		if ok {
			due = append(due, id)
		}
	}
	return due, nil
}

// Pending reports whether a session has enough unconsolidated events and no attempt in the last day.
func Pending(st *store.Store, cfg config.Config, id string, now time.Time) (bool, error) {
	attempted, err := st.SessionAttempted(id)
	if err != nil {
		return false, err
	}
	if attempted > 0 && now.Sub(time.UnixMilli(attempted)) < retryAfter {
		return false, nil
	}
	cursor, err := st.SessionCursor(id)
	if err != nil {
		return false, err
	}
	n, err := st.CountEventsAfter(id, cursor)
	return n >= cfg.Consolidate.MinEvents, err
}
