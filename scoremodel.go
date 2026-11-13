package inscribe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/a6n-ai/inscribe/internal/consolidate"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
)

// TrainScores is the idle pass. It labels injections from sessions that have
// ended and updates the score model. It does not call a model. Sessions that
// consolidation is about to review keep their open groups for an hour.
func (h *Inscribe) TrainScores(ctx context.Context, now time.Time) (recall.TrainResult, error) {
	var hold func(string) bool
	if h.cfg.Consolidate.Auto {
		due, err := consolidate.Due(h.store, h.cfg, "claude-code", now)
		if err != nil {
			return recall.TrainResult{}, err
		}
		held := make(map[string]bool, len(due))
		for _, id := range due {
			held[id] = true
		}
		hold = func(session string) bool { return held[session] }
	}
	return recall.Train(ctx, h.store, now, hold)
}

// ScoreWeights returns the score model, or the defaults when none has been learned.
func (h *Inscribe) ScoreWeights() (recall.Weights, error) {
	return recall.LoadWeights(h.store)
}

// InstallID returns the random id that tags this install's share snapshots, creating it on first use.
func (h *Inscribe) InstallID(ctx context.Context) (string, error) {
	var id string
	err := h.store.Write(ctx, func(tx *store.Tx) error {
		v, ok, err := tx.GetMeta("install_id")
		if err != nil || ok {
			id = v
			return err
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		id = hex.EncodeToString(b)
		return tx.SetMeta("install_id", id)
	})
	return id, err
}

// RecentScoreOutcomes returns the latest maxGroups groups of score outcomes, oldest first.
func (h *Inscribe) RecentScoreOutcomes(maxGroups int) ([][]store.ScoreOutcome, error) {
	return h.store.RecentScoreGroups(maxGroups)
}
