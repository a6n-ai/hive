package inscribe

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a6n-ai/inscribe/internal/feedback"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

const (
	askMinRows = 4                // outcome rows needed before a save asks
	askEvery   = 10 * time.Minute // at most one ask per session in this window
	askGroups  = 5                // newest prompt groups an ask may cover
	maxHow     = 200              // runes of an agent's how, after scrubbing
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
	all, err := h.store.MemoriesByID(ids)
	if err != nil {
		return "", "", 0, err
	}
	var ms []model.Memory
	for _, m := range all {
		if m.Status == model.Active {
			ms = append(ms, m)
		}
	}
	if len(ms) == 0 {
		return "", "", 0, nil
	}
	askID = feedback.NewAskID()
	skipped := false
	err = h.store.Write(ctx, func(tx *store.Tx) error {
		last, err := tx.LastAskTS(session)
		if err != nil {
			return err
		}
		if now.UnixMilli()-last < askEvery.Milliseconds() {
			skipped = true
			return nil
		}
		return tx.InsertAsk(askID, session, now.UnixMilli(), outcomeIDs)
	})
	if err != nil || skipped {
		return "", "", 0, err
	}
	return askID, feedback.Render(askID, ms), len(ms), nil
}

// answerPlan is one checked answer, ready to write.
type answerPlan struct {
	id      string
	rows    []store.ScoreOutcome
	label   int
	scale   float64
	reason  string
	penalty float64 // zero unless the agent called the memory wrong
}

// Feedback records the agent's answers to ask, for the session's project or one
// inside it. Every item is checked before any row changes, so one bad item
// leaves the store as it was. A row already resolved keeps its label.
func (h *Inscribe) Feedback(ctx context.Context, project, askID string, items []feedback.Answer) (labeled, wrong int, err error) {
	if project == "" {
		return 0, 0, fmt.Errorf("feedback: ask %s needs a project", askID)
	}
	if len(items) == 0 {
		return 0, 0, fmt.Errorf("feedback: ask %s has no items", askID)
	}
	var ask *store.Ask
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		ask, err = tx.GetAsk(askID)
		return err
	}); err != nil {
		return 0, 0, err
	}
	sp, err := h.store.SessionProject(ask.Session)
	if err != nil {
		return 0, 0, err
	}
	if sp != project && !strings.HasPrefix(sp, project+string(filepath.Separator)) {
		return 0, 0, fmt.Errorf("feedback: ask %s is not in project %q", askID, project)
	}

	now := time.Now().UnixMilli()
	err = h.store.Write(ctx, func(tx *store.Tx) error {
		cur, err := tx.GetAsk(askID)
		if err != nil {
			return err
		}
		if cur.Answered {
			return fmt.Errorf("feedback: ask %s is already answered", askID)
		}
		plans, err := h.planAnswers(tx, cur, items)
		if err != nil {
			return err
		}
		for _, p := range plans {
			for _, r := range p.rows {
				label := p.label
				n, err := tx.ResolveScoreOutcome(r.ID, &label, p.reason, p.scale, now)
				if err != nil {
					return err
				}
				labeled += int(n)
			}
			if p.penalty > 0 {
				if err := tx.AddPenalty(p.id, p.penalty); err != nil {
					return err
				}
				wrong++
			}
		}
		return tx.AnswerAsk(askID)
	})
	h.afterWrite()
	if err != nil {
		return 0, 0, err
	}
	if h.Debug() {
		appendLog(h.home, fmt.Sprintf("feedback %s: labeled %d rows, %d wrong", askID, labeled, wrong))
	}
	return labeled, wrong, nil
}

// planAnswers checks items against ask and returns what each one will write.
func (h *Inscribe) planAnswers(tx *store.Tx, ask *store.Ask, items []feedback.Answer) ([]answerPlan, error) {
	byMemory := map[string][]store.ScoreOutcome{}
	for _, r := range ask.Rows {
		byMemory[r.MemoryID] = append(byMemory[r.MemoryID], r)
	}
	seen := map[string]bool{}
	plans := make([]answerPlan, 0, len(items))
	for _, it := range items {
		if seen[it.ID] {
			return nil, fmt.Errorf("feedback: memory %s is answered twice", it.ID)
		}
		seen[it.ID] = true
		rows, ok := byMemory[it.ID]
		if !ok {
			return nil, fmt.Errorf("feedback: memory %s is not in ask %s", it.ID, ask.ID)
		}
		m, err := tx.GetMemory(it.ID)
		if err != nil {
			return nil, err
		}
		if m == nil || m.Status != model.Active {
			return nil, fmt.Errorf("feedback: memory %s is not active", it.ID)
		}
		it.How = h.Scrub(it.How)
		if utf8.RuneCountInString(it.How) > maxHow {
			return nil, fmt.Errorf("feedback: how for memory %s is over %d characters", it.ID, maxHow)
		}
		label, scale, reason, wrong, err := feedback.Label(it, feedback.AgentScale, "agent")
		if err != nil {
			return nil, err
		}
		p := answerPlan{id: it.ID, rows: rows, label: label, scale: scale, reason: reason}
		if wrong {
			p.penalty = feedback.Penalty(*m)
		}
		plans = append(plans, p)
	}
	return plans, nil
}
