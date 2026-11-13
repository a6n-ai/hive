// Package inscribe is Inscribe's public API: capture events, recall memories, and
// manage them. Agents reach it through the CLI or by importing this package.
package inscribe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/a6n-ai/inscribe/index"
	"github.com/a6n-ai/inscribe/internal/capture"
	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/consolidate"
	"github.com/a6n-ai/inscribe/internal/extract"
	"github.com/a6n-ai/inscribe/internal/importer"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/retention"
	"github.com/a6n-ai/inscribe/internal/scrub"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

type (
	Event         = model.Event
	Memory        = model.Memory
	MemoryType    = model.MemoryType
	ScopeKind     = model.ScopeKind
	Line          = recall.Line
	Query         = recall.Query
	ListFilter    = store.ListFilter
	RecentSession = store.RecentSession
)

// Options selects the database and config. Empty Home means config.Home().
type Options struct {
	Home, Caller, Project string
	ReadOnly              bool
	// Index serves prompt recall from the in-memory index, kept in sync with the store.
	Index bool
	// Writer marks the gateway process that batches capture; it implies Index.
	Writer bool
}

// EventFilter selects events; zero values mean no filter.
type EventFilter struct {
	Session, Project string
	Kinds            []model.EventKind
	Since, Until     int64
	Limit            int
}

type Inscribe struct {
	store    *store.Store
	pipeline *capture.Pipeline
	cfg      config.Config
	index    *index.Index
	stamp    index.Stamp
	home     string
	readOnly bool
	// mu serializes index sync and snapshot save, which share stamp.
	mu sync.Mutex
}

// Open opens the Inscribe database under o.Home with the config for o.Project.
func Open(o Options) (*Inscribe, error) {
	home := o.Home
	if home == "" {
		home = config.Home()
	}
	cfg, warns, err := config.Load(home, o.Project)
	if err != nil {
		return nil, err
	}
	logWarnings(home, warns)
	sc, err := scrub.New(cfg.Scrub.Patterns)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(home, "inscribe.db"), o.ReadOnly)
	if err != nil {
		return nil, err
	}
	reg := &extract.Registry{}
	for _, x := range extract.Defaults(o.Caller == "scout") {
		reg.Register(x)
	}
	h := &Inscribe{store: st, pipeline: &capture.Pipeline{Store: st, Scrub: sc, Extract: reg, Cfg: cfg}, cfg: cfg, home: home, readOnly: o.ReadOnly}
	if o.Index || o.Writer {
		if err := h.openIndex(); err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	return h, nil
}

// logWarnings appends config warnings to inscribe.log under home, where a user
// looks for them; a broken config never stops capture.
func logWarnings(home string, warns []string) {
	if len(warns) == 0 || os.MkdirAll(home, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "inscribe.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the log path is under the user's INSCRIBE_HOME
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	for _, w := range warns {
		fmt.Fprintf(f, "%s config: %s\n", time.Now().Format(time.RFC3339), w)
	}
}

func (h *Inscribe) Close() error {
	return errors.Join(h.closeIndex(), h.store.Close())
}

// Scrub returns s with secrets redacted, as the stored events and memories are.
func (h *Inscribe) Scrub(s string) string {
	out, _ := h.pipeline.Scrub.Scrub(s)
	return out
}

// Capture stores e and what it implies. An event without a timestamp gets now.
func (h *Inscribe) Capture(ctx context.Context, e Event) error {
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	err := h.pipeline.Capture(ctx, e)
	h.afterWrite()
	return err
}

// Prepare scrubs e and sets its digest and timestamp in place, as Capture will store them.
// Calling it before Capture lets the caller key recall by the stored digest.
func (h *Inscribe) Prepare(e *Event) {
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	h.pipeline.Prepare(e)
}

// DroppedOutcomes counts score outcome rows not written because the store was locked.
func (h *Inscribe) DroppedOutcomes() int64 { return h.store.DroppedOutcomes() }

// RecentSessions returns the latest sessions other than exclude, in any project.
func (h *Inscribe) RecentSessions(exclude string, limit int) ([]RecentSession, error) {
	return h.store.RecentSessions(exclude, limit)
}

func (h *Inscribe) Recall(ctx context.Context, q Query) ([]Line, error) {
	if h.index != nil {
		if err := h.syncIndex(); err != nil {
			return nil, err
		}
	}
	return recall.Recall(ctx, h.store, h.source(), h.cfg.Recall, q, time.Now())
}

// Remember stores text as a memory and returns its ID.
func (h *Inscribe) Remember(ctx context.Context, text string, t MemoryType, scope ScopeKind, project string) (string, error) {
	err := h.Capture(ctx, Event{Agent: "cli", Session: "cli", Project: project, Kind: model.Remembered,
		Payload: map[string]any{"text": text, "type": string(t), "scope": string(scope)}})
	if err != nil {
		return "", err
	}
	// The extractor keys the memory by the scrubbed text, so look it up the same way.
	scrubbed, _ := h.pipeline.Scrub.Scrub(text)
	sum := sha256.Sum256([]byte(scrubbed))
	ms, err := h.store.ListMemories(store.ListFilter{Types: []model.MemoryType{t}, Key: hex.EncodeToString(sum[:]), Project: project, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return "", errors.New("inscribe: remembered text was not stored (database busy or event excluded)")
	}
	return ms[0].ID, nil
}

// Forget marks a memory forgotten, or deletes it outright when purge is set.
func (h *Inscribe) Forget(ctx context.Context, id string, purge bool) error {
	err := h.store.Write(ctx, func(tx *store.Tx) error {
		if purge {
			return tx.DeleteMemory(id)
		}
		return tx.SetStatus(id, model.Forgotten)
	})
	h.afterWrite()
	// A purged memory's terms must not stay on disk until Close.
	if purge && err == nil && h.index != nil {
		return h.saveIndex()
	}
	return err
}

// Why returns the events that evidence a memory, oldest first.
func (h *Inscribe) Why(_ context.Context, id string) ([]Event, error) {
	return h.store.Evidence(id)
}

func (h *Inscribe) List(_ context.Context, f ListFilter) ([]Memory, error) {
	return h.store.ListMemories(f)
}

func (h *Inscribe) RegisterExtractor(x extract.Extractor) { h.pipeline.Extract.Register(x) }

func (h *Inscribe) QueryEvents(_ context.Context, f EventFilter) ([]Event, error) {
	return h.store.QueryEvents(f.Session, f.Project, f.Kinds, f.Since, f.Until, f.Limit)
}

// Retention prunes stored data under the [retention] settings. A dry run reports
// the counts and changes nothing.
func (h *Inscribe) Retention(ctx context.Context, dryRun bool) (retention.Report, error) {
	rep, err := retention.Apply(ctx, h.store, h.cfg.Retention, time.Now(), dryRun)
	h.afterWrite()
	return rep, err
}

// AutoRetention prunes when a prune is due and the settings are not paused. It
// reports whether it ran.
func (h *Inscribe) AutoRetention(ctx context.Context) (bool, error) {
	ran, err := retention.Auto(ctx, h.store, h.cfg.Retention, time.Now())
	h.afterWrite()
	return ran, err
}

// Consolidate reviews a session's new events through runner. A dry run returns
// the proposed ops without applying them.
func (h *Inscribe) Consolidate(ctx context.Context, session string, runner consolidate.Runner, dryRun bool) (consolidate.RunReport, error) {
	rep, err := consolidate.Run(ctx, h.store, h.cfg, h.Scrub, runner, session, dryRun)
	h.afterWrite()
	return rep, err
}

// Consolidation returns the consolidation settings in effect.
func (h *Inscribe) Consolidation() config.ConsolidateCfg { return h.cfg.Consolidate }

// Debug reports whether config.toml or INSCRIBE_DEBUG=1 turned on diagnostics.
func (h *Inscribe) Debug() bool { return h.cfg.Debug }

// SavedBy returns the memories that the event of session at ts, and the events derived from it, saved or updated.
func (h *Inscribe) SavedBy(session string, ts int64) ([]Memory, error) {
	return h.store.SavedBy(session, ts)
}

// DueConsolidations lists the agent's idle sessions that have enough new events to review.
func (h *Inscribe) DueConsolidations(agent string, now time.Time) ([]string, error) {
	return consolidate.Due(h.store, h.cfg, agent, now)
}

// Undo reverses a consolidation run, leaving alone any memory changed since.
func (h *Inscribe) Undo(ctx context.Context, runID string) (consolidate.UndoReport, error) {
	rep, err := consolidate.Undo(ctx, h.store, runID)
	h.afterWrite()
	return rep, err
}

// ImportReport is an import's summary plus what it added to the store.
type ImportReport struct {
	importer.Summary
	NewEvents, NewMemories, Redactions int
}

// ImportClaudeCode backfills events and memories from Claude Code transcripts.
func (h *Inscribe) ImportClaudeCode(ctx context.Context, o importer.Options) (ImportReport, error) {
	evBefore, memBefore, err := h.store.Counts()
	if err != nil {
		return ImportReport{}, err
	}
	redBefore := h.pipeline.Redactions.Load()
	sum, err := importer.ClaudeCode(ctx, o, h.Capture)
	if err != nil {
		return ImportReport{Summary: sum}, err
	}
	evAfter, memAfter, err := h.store.Counts()
	if err != nil {
		return ImportReport{Summary: sum}, err
	}
	return ImportReport{Summary: sum, NewEvents: evAfter - evBefore, NewMemories: memAfter - memBefore,
		Redactions: int(h.pipeline.Redactions.Load() - redBefore)}, nil
}

// ImportClaudeMem imports decisions and fixes from a claude-mem database. With
// DryRun it reads and counts only.
func (h *Inscribe) ImportClaudeMem(ctx context.Context, o importer.ClaudeMemOptions) (importer.ClaudeMemReport, error) {
	evs, rep, err := importer.ReadClaudeMem(ctx, o)
	if err != nil || o.DryRun {
		return rep, err
	}
	for _, e := range evs {
		if err := h.Capture(ctx, e); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// CaptureBatch stores evs in one transaction, as Capture does for each. An
// event without a timestamp gets now.
func (h *Inscribe) CaptureBatch(ctx context.Context, evs []Event) error {
	batch := slices.Clone(evs)
	for i := range batch {
		if batch[i].TS == 0 {
			batch[i].TS = time.Now().UnixMilli()
		}
	}
	err := h.pipeline.CaptureBatch(ctx, batch)
	h.afterWrite()
	return err
}
