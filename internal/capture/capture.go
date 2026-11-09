// Package capture is Hive's write path: scrub, apply the storage policy,
// derive events, store, and run extractors, in one transaction.
package capture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/extract"
	"github.com/a6n-ai/hive/internal/scrub"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

type Pipeline struct {
	Store   *store.Store
	Scrub   *scrub.Scrubber
	Extract *extract.Registry
	Cfg     config.Config
}

// Digest identifies an event for idempotent inserts.
func Digest(e *model.Event) string {
	payload, _ := json.Marshal(e.Payload) // map keys marshal in sorted order
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%s|%s", e.Agent, e.Session, e.Kind, e.TS, payload, e.PromptText)))
	return hex.EncodeToString(h[:])
}

// Derive returns the events implied by e. prev is the session's previous
// event (nil if none).
func Derive(e *model.Event, prev *model.Event) []model.Event {
	child := func(kind model.EventKind, payload map[string]any) model.Event {
		return model.Event{TS: e.TS, Agent: e.Agent, Session: e.Session, Project: e.Project, Kind: kind, Tool: e.Tool,
			Payload: payload, Digest: hashString(e.Digest + "|" + string(kind))}
	}
	switch e.Kind {
	case model.PostTool:
		switch e.Tool {
		case "skill":
			if s, ok := e.Payload["skill"].(string); ok && s != "" {
				return []model.Event{child(model.SkillLoaded, map[string]any{"skill": s})}
			}
		case "shell":
			cmd, ok := e.Payload["command"].(string)
			if !ok || cmd == "" {
				return nil
			}
			p := map[string]any{"command": cmd, "exit_code": e.Payload["exit_code"]}
			if errText, ok := e.Payload["error"]; ok {
				p["error"] = errText
			}
			return []model.Event{child(model.CommandResult, p)}
		}
	case model.Prompt:
		if prev == nil || prev.Kind != model.TurnEnd {
			return nil
		}
		if rule := correctionRule(e.PromptText); rule != "" {
			return []model.Event{child(model.Correction, map[string]any{"rule": rule, "text": e.PromptText})}
		}
	}
	return nil
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Prepare scrubs e, applies the storage policy, and sets its digest.
func (p *Pipeline) Prepare(e *model.Event) {
	ApplyPolicy(e) // truncate first, so scrubbing never scans more than the store keeps
	if e.Payload != nil {
		e.Payload, _ = p.Scrub.ScrubAny(e.Payload).(map[string]any)
	}
	e.PromptText, _ = p.Scrub.Scrub(e.PromptText)
	if e.Digest == "" {
		e.Digest = Digest(e)
	}
}

// Capture stores e and everything it implies. When the database is locked the
// event is spooled for the next writer and Capture returns nil.
func (p *Pipeline) Capture(ctx context.Context, e model.Event) error {
	excluded := normalizeExcludes(p.Cfg.Exclude.Paths)
	if underAny(e.Project, excluded) {
		return nil
	}
	p.Prepare(&e)
	err := p.Store.Write(ctx, func(tx *store.Tx) error {
		drained, err := tx.DrainSpoolEvents()
		if err != nil {
			return err
		}
		for i := range drained {
			if underAny(drained[i].Project, excluded) {
				continue
			}
			if err := tx.Savepoint(func() error { return p.process(ctx, tx, &drained[i]) }); err != nil {
				if err := quarantine(p.Store.Path(), &drained[i]); err != nil {
					return err
				}
			}
		}
		inserted, err := tx.InsertEvent(&e)
		if err != nil || !inserted {
			return err
		}
		return p.process(ctx, tx, &e)
	})
	if err != nil && !errors.Is(err, store.ErrSchemaNewer) && !errors.Is(err, store.ErrReadOnly) {
		// Spool on any failure so a bad drain never drops the current event.
		return p.Store.Spool(&e)
	}
	return err
}

// quarantine keeps an event whose processing always fails in spool/bad, so it
// cannot fail every later write.
func quarantine(dbPath string, e *model.Event) error {
	dir := filepath.Join(filepath.Dir(dbPath), "spool", "bad")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	name := filepath.Join(dir, fmt.Sprintf("poison-%d.jsonl", time.Now().UnixNano()))
	return os.WriteFile(name, append(line, '\n'), 0o600)
}

// process runs on an event that is already inserted.
func (p *Pipeline) process(ctx context.Context, tx *store.Tx, e *model.Event) error {
	prev, err := tx.LastEvent(e.Session, e.ID)
	if err != nil {
		return err
	}
	if err := tx.TouchSession(e); err != nil {
		return err
	}
	for _, d := range Derive(e, prev) {
		inserted, err := tx.InsertEvent(&d)
		if err != nil {
			return err
		}
		if inserted {
			if err := p.Extract.Run(ctx, &d, tx, p.Cfg); err != nil {
				return err
			}
		}
	}
	return p.Extract.Run(ctx, e, tx, p.Cfg)
}
