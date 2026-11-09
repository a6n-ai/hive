// Package extract turns captured events into typed memories with plain code.
package extract

import (
	"context"
	"slices"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// Extractor reacts to events of the kinds it names, inside the capture
// transaction.
type Extractor interface {
	Kinds() []model.EventKind
	Extract(ctx context.Context, e *model.Event, tx *store.Tx, cfg config.Config) error
}

type Registry struct {
	extractors []Extractor
}

func (r *Registry) Register(x Extractor) { r.extractors = append(r.extractors, x) }

// Run calls every extractor registered for e.Kind, stopping at the first error.
func (r *Registry) Run(ctx context.Context, e *model.Event, tx *store.Tx, cfg config.Config) error {
	for _, x := range r.extractors {
		if !slices.Contains(x.Kinds(), e.Kind) {
			continue
		}
		if err := x.Extract(ctx, e, tx, cfg); err != nil {
			return err
		}
	}
	return nil
}
