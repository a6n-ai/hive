package extract

import (
	"context"
	"sort"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

const (
	cochangeMinTurns = 3
	cochangeMaxFiles = 8 // larger turns are refactors, not real coupling
)

type cochangeX struct{}

// NewCochange remembers files that keep being edited in the same turn.
func NewCochange() Extractor { return cochangeX{} }

func (cochangeX) Kinds() []model.EventKind { return []model.EventKind{model.TurnEnd} }

func (cochangeX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	prior, err := tx.RecentEvents(e.Session, e.ID, lookback)
	if err != nil {
		return err
	}
	files := turnEdits(prior, e.Project)
	if len(files) < 2 || len(files) > cochangeMaxFiles {
		return nil
	}
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			key := files[i] + "|" + files[j]
			text := files[i] + " changes with " + files[j]
			m, err := tx.UpsertMemory(projectMemory(e, model.Cochange, key, text, nil, model.Candidate), nil, e.ID)
			if err != nil {
				return err
			}
			if m.Status == model.Candidate && m.Count >= cochangeMinTurns {
				if err := tx.SetStatus(m.ID, model.Active); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// turnEdits returns the sorted, distinct project-relative paths edited since
// the previous turn end. prior is newest first.
func turnEdits(prior []model.Event, project string) []string {
	seen := map[string]bool{}
	for i := range prior {
		if prior[i].Kind == model.TurnEnd {
			break
		}
		if p, ok := isEdit(&prior[i]); ok {
			seen[relPath(project, p)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
