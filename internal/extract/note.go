package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// rememberedTypes are the memory types `inscribe remember` may create.
var rememberedTypes = map[string]model.MemoryType{
	"note": model.Note, "preference": model.Preference, "decision": model.Decision,
}

type noteX struct{}

// NewNote turns `inscribe remember` events into memories.
func NewNote() Extractor { return noteX{} }

func (noteX) Kinds() []model.EventKind { return []model.EventKind{model.Remembered} }

func (noteX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	text := str(e.Payload, "text")
	if text == "" {
		return nil
	}
	t, ok := rememberedTypes[str(e.Payload, "type")]
	if !ok {
		t = model.Note
	}
	sum := sha256.Sum256([]byte(text))
	m := projectMemory(e, t, hex.EncodeToString(sum[:]), text, nil, model.Active)
	m.Confidence = 0.9
	if str(e.Payload, "scope") == "global" {
		m.ScopeKind, m.Scope = model.Global, ""
	}
	_, err := tx.UpsertMemory(m, nil, e.ID)
	return err
}
