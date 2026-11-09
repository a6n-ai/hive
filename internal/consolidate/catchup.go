package consolidate

import (
	"time"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
)

// idleAfter is how long a session must be silent before catch-up consolidates it.
const idleAfter = time.Hour

// Due returns the agent's idle sessions that have at least MinEvents events not
// yet consolidated. It catches sessions whose session end never ran.
func Due(st *store.Store, cfg config.Config, agent string, now time.Time) ([]string, error) {
	idle, err := st.IdleSessions(agent, now.Add(-idleAfter).UnixMilli())
	if err != nil {
		return nil, err
	}
	var due []string
	for _, id := range idle {
		cursor, err := st.SessionCursor(id)
		if err != nil {
			return nil, err
		}
		n, err := st.CountEventsAfter(id, cursor)
		if err != nil {
			return nil, err
		}
		if n >= cfg.Consolidate.MinEvents {
			due = append(due, id)
		}
	}
	return due, nil
}
