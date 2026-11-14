// Package retention bounds stored data: it clears prompt text, samples it for
// replay, and deletes old events that no active memory cites as evidence.
package retention

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
)

// Report counts what a prune changed, or would change in a dry run.
type Report struct {
	PromptTextsCleared, EventsDeleted, ReplayAdded, ReplayDropped, KeptAsEvidence, InjectionsDeleted, UsageTurnsDeleted int
}

const (
	batchSize   = 5000
	appliedKey  = "retention_applied"
	lastPrune   = "last_prune"
	pruneEvery  = 24 * time.Hour
	modeRolling = "rolling"
	modeKeepAll = "keep_all"
)

var errDryRun = errors.New("retention: dry run")

// Apply prunes st under cfg as of now, in batches of 5,000 rows per transaction.
// A dry run reports the counts and changes nothing.
func Apply(ctx context.Context, st *store.Store, cfg config.RetentionCfg, now time.Time, dryRun bool) (Report, error) {
	var rep Report
	if dryRun {
		err := st.Write(ctx, func(tx *store.Tx) error {
			if err := sweep(tx, cfg, now, &rep); err != nil {
				return err
			}
			return errDryRun
		})
		if errors.Is(err, errDryRun) {
			return rep, nil
		}
		return rep, err
	}
	for {
		var changed int
		err := st.Write(ctx, func(tx *store.Tx) error {
			var err error
			changed, err = pass(tx, cfg, now, &rep)
			return err
		})
		if err != nil {
			return rep, err
		}
		if changed == 0 {
			break
		}
	}
	return rep, st.Write(ctx, func(tx *store.Tx) error {
		if err := finish(tx, cfg, now, &rep); err != nil {
			return err
		}
		if err := tx.SetMeta(lastPrune, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
			return err
		}
		return tx.SetMeta(appliedKey, settingsJSON(cfg))
	})
}

// Due reports whether no prune ran in the last day. The settings check is
// Paused's job.
func Due(st *store.Store, now time.Time) (bool, error) {
	v, ok, err := st.GetMeta(lastPrune)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return true, nil
	}
	return now.Sub(time.UnixMilli(ms)) > pruneEvery, nil
}

// Paused reports whether cfg differs from the settings last applied. Pruning
// waits for `inscribe retention apply` to confirm. With no record there is no pause.
func Paused(st *store.Store, cfg config.RetentionCfg) (bool, error) {
	v, ok, err := st.GetMeta(appliedKey)
	if err != nil || !ok {
		return false, err
	}
	return v != settingsJSON(cfg), nil
}

// Auto applies retention when it is due and not paused. It reports whether it ran.
func Auto(ctx context.Context, st *store.Store, cfg config.RetentionCfg, now time.Time) (bool, error) {
	due, err := Due(st, now)
	if err != nil || !due {
		return false, err
	}
	paused, err := Paused(st, cfg)
	if err != nil || paused {
		return false, err
	}
	_, err = Apply(ctx, st, cfg, now, false)
	return err == nil, err
}

// sweep runs every pass inside one transaction, for a dry run.
func sweep(tx *store.Tx, cfg config.RetentionCfg, now time.Time, rep *Report) error {
	for {
		n, err := pass(tx, cfg, now, rep)
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	return finish(tx, cfg, now, rep)
}

// pass runs one batch of each step and returns how many rows it changed.
func pass(tx *store.Tx, cfg config.RetentionCfg, now time.Time, rep *Report) (int, error) {
	if cfg.Mode == modeKeepAll {
		return 0, nil
	}
	cleared, err := clearPrompts(tx, cfg, now, rep)
	if err != nil || cfg.Mode != modeRolling {
		return cleared, err
	}
	dropped, err := trimReplay(tx, cfg, now, rep)
	if err != nil {
		return 0, err
	}
	deleted, err := deleteOld(tx, cfg, now, rep)
	return cleared + dropped + deleted, err
}

// clearPrompts copies expiring prompt text to replay (rolling, when sampling is
// on) and then clears it.
func clearPrompts(tx *store.Tx, cfg config.RetentionCfg, now time.Time, rep *Report) (int, error) {
	cutoff := now.Add(-days(cfg.PromptTextDays)).UnixMilli()
	rolling := cfg.Mode == modeRolling
	evs, err := tx.ExpiringPrompts(cutoff, rolling && cfg.PromptClock == "original", batchSize)
	if err != nil {
		return 0, err
	}
	if rolling && cfg.ReplaySampleMax > 0 {
		added, err := tx.CopyToReplay(evs)
		if err != nil {
			return 0, err
		}
		rep.ReplayAdded += added
	}
	cleared, err := tx.ClearPromptText(evs)
	rep.PromptTextsCleared += cleared
	return cleared, err
}

// trimReplay keeps the newest replay_sample_max samples and drops samples older
// than replay_days.
func trimReplay(tx *store.Tx, cfg config.RetentionCfg, now time.Time, rep *Report) (int, error) {
	n, err := tx.TrimReplay(cfg.ReplaySampleMax)
	if err != nil {
		return 0, err
	}
	dropped := int(n)
	if cfg.ReplayDays > 0 {
		m, err := tx.DropReplayBefore(now.Add(-days(cfg.ReplayDays)).UnixMilli())
		if err != nil {
			return 0, err
		}
		dropped += int(m)
	}
	rep.ReplayDropped += dropped
	return dropped, nil
}

func deleteOld(tx *store.Tx, cfg config.RetentionCfg, now time.Time, rep *Report) (int, error) {
	cutoff := now.Add(-days(cfg.EventDays)).UnixMilli()
	n, err := tx.DeleteOldEvents(cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	inj, use, err := tx.DeleteOldAnalytics(cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	rep.EventsDeleted += int(n)
	rep.InjectionsDeleted += int(inj)
	rep.UsageTurnsDeleted += int(use)
	return int(n + inj + use), nil
}

// finish drops evidence links to deleted events and counts the old events kept as evidence.
func finish(tx *store.Tx, cfg config.RetentionCfg, now time.Time, rep *Report) error {
	if cfg.Mode != modeRolling {
		return nil
	}
	if err := tx.DropOrphanEvidence(); err != nil {
		return err
	}
	n, err := tx.CountEventsBefore(now.Add(-days(cfg.EventDays)).UnixMilli())
	rep.KeptAsEvidence = n
	return err
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func settingsJSON(cfg config.RetentionCfg) string {
	b, _ := json.Marshal(cfg) // a struct of scalars always marshals
	return string(b)
}
