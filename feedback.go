package inscribe

import (
	"context"
	"time"

	"github.com/a6n-ai/inscribe/internal/feedback"
	"github.com/a6n-ai/inscribe/internal/store"
)

const (
	askMinRows = 4                // outcome rows needed before a save asks
	askEvery   = 10 * time.Minute // at most one ask per session in this window
	askGroups  = 5                // newest prompt groups an ask may cover
)

// MaybeAsk returns the feedback question to add after a save, or an empty askID
// when none is due. The ask covers every unasked outcome row of the window, so
// rows of one memory are asked about once.
func (h *Inscribe) MaybeAsk(ctx context.Context, session string, savedAny bool, now time.Time) (askID, text string, n int, err error) {
	if !savedAny {
		return "", "", 0, nil
	}
	last, err := h.store.LastAskTS(session)
	if err != nil {
		return "", "", 0, err
	}
	if now.UnixMilli()-last < askEvery.Milliseconds() {
		return "", "", 0, nil
	}
	rows, err := h.store.UnaskedOutcomes(session, askGroups)
	if err != nil {
		return "", "", 0, err
	}
	if len(rows) < askMinRows {
		return "", "", 0, nil
	}
	seen := map[string]bool{}
	var ids []string
	outcomeIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		outcomeIDs = append(outcomeIDs, r.ID)
		if !seen[r.MemoryID] {
			seen[r.MemoryID] = true
			ids = append(ids, r.MemoryID)
		}
	}
	ms, err := h.store.MemoriesByID(ids)
	if err != nil {
		return "", "", 0, err
	}
	if len(ms) == 0 {
		return "", "", 0, nil
	}
	askID = feedback.NewAskID()
	err = h.store.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertAsk(askID, session, now.UnixMilli(), outcomeIDs)
	})
	if err != nil {
		return "", "", 0, err
	}
	return askID, feedback.Render(askID, ms), len(ms), nil
}
