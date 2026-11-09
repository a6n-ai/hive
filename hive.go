// Package hive is Hive's public API: capture events, recall memories, and
// manage them. Agents reach it through the CLI or by importing this package.
package hive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/a6n-ai/hive/internal/capture"
	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/extract"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/internal/scrub"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

type (
	Event      = model.Event
	Memory     = model.Memory
	MemoryType = model.MemoryType
	ScopeKind  = model.ScopeKind
	Line       = recall.Line
	Query      = recall.Query
	ListFilter = store.ListFilter
)

// Options selects the database and config. Empty Home means config.Home().
type Options struct {
	Home, Caller, Project string
	ReadOnly              bool
}

// EventFilter selects events; zero values mean no filter.
type EventFilter struct {
	Session, Project string
	Kinds            []model.EventKind
	Since, Until     int64
	Limit            int
}

type Hive struct {
	store    *store.Store
	pipeline *capture.Pipeline
	cfg      config.Config
}

// Open opens the Hive database under o.Home with the config for o.Project.
func Open(o Options) (*Hive, error) {
	home := o.Home
	if home == "" {
		home = config.Home()
	}
	cfg, _, err := config.Load(home, o.Project)
	if err != nil {
		return nil, err
	}
	sc, err := scrub.New(cfg.Scrub.Patterns)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(home, "hive.db"), o.ReadOnly)
	if err != nil {
		return nil, err
	}
	reg := &extract.Registry{}
	for _, x := range extract.Defaults(o.Caller == "scout") {
		reg.Register(x)
	}
	return &Hive{store: st, pipeline: &capture.Pipeline{Store: st, Scrub: sc, Extract: reg, Cfg: cfg}, cfg: cfg}, nil
}

func (h *Hive) Close() error { return h.store.Close() }

// Capture stores e and what it implies. An event without a timestamp gets now.
func (h *Hive) Capture(ctx context.Context, e Event) error {
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	return h.pipeline.Capture(ctx, e)
}

func (h *Hive) Recall(ctx context.Context, q Query) ([]Line, error) {
	return recall.Recall(ctx, h.store, h.cfg.Recall, q, time.Now())
}

// Remember stores text as a memory and returns its ID.
func (h *Hive) Remember(ctx context.Context, text string, t MemoryType, scope ScopeKind, project string) (string, error) {
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
		return "", errors.New("hive: remembered text was not stored (database busy or event excluded)")
	}
	return ms[0].ID, nil
}

// Forget marks a memory forgotten, or deletes it outright when purge is set.
func (h *Hive) Forget(ctx context.Context, id string, purge bool) error {
	return h.store.Write(ctx, func(tx *store.Tx) error {
		if purge {
			return tx.DeleteMemory(id)
		}
		return tx.SetStatus(id, model.Forgotten)
	})
}

// Why returns the events that evidence a memory, oldest first.
func (h *Hive) Why(_ context.Context, id string) ([]Event, error) {
	return h.store.Evidence(id)
}

func (h *Hive) List(_ context.Context, f ListFilter) ([]Memory, error) {
	return h.store.ListMemories(f)
}

func (h *Hive) RegisterExtractor(x extract.Extractor) { h.pipeline.Extract.Register(x) }

func (h *Hive) QueryEvents(_ context.Context, f EventFilter) ([]Event, error) {
	return h.store.QueryEvents(f.Session, f.Project, f.Kinds, f.Since, f.Until, f.Limit)
}
