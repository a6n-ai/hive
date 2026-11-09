package extract

import (
	"context"
	"slices"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// preferenceMinSessions is how many distinct sessions must repeat a
// correction before code alone promotes it (consolidation can promote sooner).
const preferenceMinSessions = 2

type preferenceX struct{}

// NewPreference turns repeated corrections into user-wide preferences.
func NewPreference() Extractor { return preferenceX{} }

func (preferenceX) Kinds() []model.EventKind { return []model.EventKind{model.Correction} }

func (preferenceX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	rule := str(e.Payload, "rule")
	if rule == "" {
		return nil
	}
	m := &model.Memory{Type: model.Preference, ScopeKind: model.Global, Key: rule, Text: rule,
		Body: map[string]any{"sessions": []any{e.Session}}, Confidence: 0.5, Status: model.Candidate,
		FirstSeen: e.TS, LastSeen: e.TS}
	m, err := tx.UpsertMemory(m, mergeSessions, e.ID)
	if err != nil {
		return err
	}
	if sessions, _ := m.Body["sessions"].([]any); m.Status == model.Candidate && len(sessions) >= preferenceMinSessions {
		return tx.SetStatus(m.ID, model.Active)
	}
	return nil
}

// mergeSessions unions the distinct session ids that stated the preference.
func mergeSessions(prev, next map[string]any) map[string]any {
	out := store.ShallowMerge(prev, next)
	merged, _ := prev["sessions"].([]any)
	merged = slices.Clone(merged)
	add, _ := next["sessions"].([]any)
	for _, s := range add {
		if !slices.Contains(merged, s) {
			merged = append(merged, s)
		}
	}
	out["sessions"] = merged
	return out
}
