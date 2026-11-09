package store

import (
	"context"

	"github.com/a6n-ai/hive/model"
)

// Backend is the storage seam: every *Store method used outside this package.
// Tx stays concrete until a second backend exists.
type Backend interface {
	Close() error
	Path() string
	SchemaVersion() (int, error)
	Write(ctx context.Context, fn func(*Tx) error) error
	HitLock() bool
	GetMemory(id string) (*model.Memory, error)
	Counts() (events, memories int, err error)
	ListMemories(f ListFilter) ([]model.Memory, error)
	SearchFTS(terms []string, f ListFilter, limit int) ([]model.Memory, []float64, error)
	Evidence(memoryID string) ([]model.Event, error)
	QueryEvents(session, project string, kinds []model.EventKind, since, until int64, limit int) ([]model.Event, error)
	SessionEvents(session string) ([]model.Event, error)
	Spool(e *model.Event) error
	GetMeta(k string) (string, bool, error)
	EventsAfter(session string, afterID int64, limit int) ([]model.Event, error)
	CountEventsAfter(session string, afterID int64) (int, error)
	SessionCursor(session string) (int64, error)
	MemoriesCitingEvents(eventIDs []int64) ([]model.Memory, error)
	RunStatus(runID string) (string, error)
	AuditRows(runID string) ([]AuditRow, error)
	IdleSessions(agent string, after, before int64) ([]string, error)
	SessionAttempted(session string) (int64, error)
	ChangesSince(ms int64) ([]model.Memory, []string, int64, error)
}

var _ Backend = (*Store)(nil)
