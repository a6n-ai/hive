package inscribe

import (
	"context"
	"time"

	"github.com/a6n-ai/inscribe/internal/recall"
)

// TrainScores is the idle pass. It labels injections from sessions that have
// ended and updates the score model. It does not call a model.
func (h *Inscribe) TrainScores(ctx context.Context, now time.Time) (recall.TrainResult, error) {
	return recall.Train(ctx, h.store, now)
}

// ScoreWeights returns the score model, or the defaults when none has been learned.
func (h *Inscribe) ScoreWeights() (recall.Weights, error) {
	return recall.LoadWeights(h.store)
}
