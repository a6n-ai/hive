package retention_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/retention"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

type fixture struct {
	t    *testing.T
	st   *store.Store
	raw  *sql.DB // second connection, for stored_ts and direct reads
	seq  int
	conf config.RetentionCfg
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hive.db")
	st, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return &fixture{t: t, st: st, raw: raw, conf: config.Defaults().Retention}
}

// event inserts a prompt (or an empty event when text is "") with original time
// ts and stored time stored; it returns the event id.
func (f *fixture) event(ts, stored time.Time, text string) model.Event {
	f.t.Helper()
	f.seq++
	e := model.Event{TS: ts.UnixMilli(), Agent: "claude-code", Session: "s1", Project: "/p", Kind: model.Prompt,
		PromptText: text, Digest: fmt.Sprintf("d-%d", f.seq), Payload: map[string]any{}}
	err := f.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.InsertEvent(&e)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.raw.Exec(`UPDATE events SET stored_ts = ? WHERE id = ?`, stored.UnixMilli(), e.ID); err != nil {
		f.t.Fatal(err)
	}
	return e
}

func (f *fixture) apply(cfg config.RetentionCfg, dry bool) retention.Report {
	f.t.Helper()
	rep, err := retention.Apply(context.Background(), f.st, cfg, now, dry)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) count(q string) int {
	f.t.Helper()
	var n int
	if err := f.raw.QueryRow(q).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) promptText(id int64) (string, bool) {
	f.t.Helper()
	var s sql.NullString
	if err := f.raw.QueryRow(`SELECT prompt_text FROM events WHERE id = ?`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s.String, s.Valid
}

func TestKeepAllRemovesNothing(t *testing.T) {
	f := newFixture(t)
	f.event(now.Add(-400*day), now.Add(-400*day), "old prompt")
	cfg := f.conf
	cfg.Mode = "keep_all"
	rep := f.apply(cfg, false)
	if rep != (retention.Report{}) {
		t.Errorf("report = %+v, want zero", rep)
	}
	if n := f.count(`SELECT count(*) FROM events`); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

func TestStorageTimeClearsByStoredTS(t *testing.T) {
	f := newFixture(t)
	oldOriginal := f.event(now.Add(-400*day), now.Add(-1*day), "stored recently")
	oldStored := f.event(now.Add(-1*day), now.Add(-400*day), "stored long ago")
	cfg := f.conf
	cfg.Mode = "storage_time"
	rep := f.apply(cfg, false)
	if rep.PromptTextsCleared != 1 || rep.EventsDeleted != 0 {
		t.Errorf("report = %+v, want 1 cleared and 0 deleted", rep)
	}
	if _, ok := f.promptText(oldOriginal.ID); !ok {
		t.Error("prompt stored recently was cleared")
	}
	if _, ok := f.promptText(oldStored.ID); ok {
		t.Error("prompt stored long ago was not cleared")
	}
}

func TestRollingSamplesThenClears(t *testing.T) {
	f := newFixture(t)
	e := f.event(now.Add(-60*day), now.Add(-60*day), "fix the login test")
	rep := f.apply(f.conf, false)
	if rep.PromptTextsCleared != 1 || rep.ReplayAdded != 1 {
		t.Errorf("report = %+v, want 1 cleared and 1 replay added", rep)
	}
	if _, ok := f.promptText(e.ID); ok {
		t.Error("prompt text still present after rolling prune")
	}
	if n := f.count(`SELECT count(*) FROM replay_prompts WHERE prompt_text = 'fix the login test'`); n != 1 {
		t.Errorf("replay rows = %d, want 1", n)
	}
}

func TestReplayCapAndAge(t *testing.T) {
	t.Run("cap keeps newest", func(t *testing.T) {
		f := newFixture(t)
		for i := range 3 {
			f.event(now.Add(time.Duration(-60-i)*day), now.Add(-60*day), fmt.Sprintf("prompt %d", i))
		}
		cfg := f.conf
		cfg.ReplaySampleMax = 2
		rep := f.apply(cfg, false)
		if n := f.count(`SELECT count(*) FROM replay_prompts`); n != 2 || rep.ReplayDropped != 1 {
			t.Errorf("replay rows = %d dropped = %d, want 2 and 1", n, rep.ReplayDropped)
		}
		if n := f.count(`SELECT count(*) FROM replay_prompts WHERE prompt_text = 'prompt 2'`); n != 0 {
			t.Error("oldest prompt was kept over newer ones")
		}
	})
	t.Run("age drops old samples", func(t *testing.T) {
		f := newFixture(t)
		f.event(now.Add(-200*day), now.Add(-200*day), "ancient prompt")
		cfg := f.conf
		cfg.ReplayDays = 100
		rep := f.apply(cfg, false)
		if n := f.count(`SELECT count(*) FROM replay_prompts`); n != 0 || rep.ReplayDropped != 1 {
			t.Errorf("replay rows = %d dropped = %d, want 0 and 1", n, rep.ReplayDropped)
		}
	})
}

func TestEvidenceNeverDeleted(t *testing.T) {
	f := newFixture(t)
	kept := f.event(now.Add(-400*day), now.Add(-400*day), "")
	gone := f.event(now.Add(-400*day), now.Add(-400*day), "")
	mem := &model.Memory{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "/p", Key: "k1", Text: "t",
		Confidence: 0.5, Status: model.Active, FirstSeen: now.UnixMilli(), LastSeen: now.UnixMilli()}
	forgotten := &model.Memory{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "/p", Key: "k2", Text: "t",
		Confidence: 0.5, Status: model.Active, FirstSeen: now.UnixMilli(), LastSeen: now.UnixMilli()}
	err := f.st.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.UpsertMemory(mem, nil, kept.ID); err != nil {
			return err
		}
		m, err := tx.UpsertMemory(forgotten, nil, gone.ID)
		if err != nil {
			return err
		}
		return tx.SetStatus(m.ID, model.Forgotten)
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := f.apply(f.conf, false)
	if rep.EventsDeleted != 1 || rep.KeptAsEvidence != 1 {
		t.Errorf("report = %+v, want 1 deleted and 1 kept as evidence", rep)
	}
	if n := f.count(fmt.Sprintf(`SELECT count(*) FROM events WHERE id = %d`, kept.ID)); n != 1 {
		t.Error("evidence event for an active memory was deleted")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	e := f.event(now.Add(-400*day), now.Add(-400*day), "old prompt")
	rep := f.apply(f.conf, true)
	if rep.PromptTextsCleared != 1 || rep.EventsDeleted != 1 {
		t.Errorf("dry-run report = %+v, want 1 cleared and 1 deleted", rep)
	}
	if _, ok := f.promptText(e.ID); !ok {
		t.Error("dry run cleared prompt text")
	}
	if n := f.count(`SELECT count(*) FROM events`); n != 1 {
		t.Errorf("events = %d after dry run, want 1", n)
	}
	if _, ok, _ := f.st.GetMeta("last_prune"); ok {
		t.Error("dry run recorded last_prune")
	}
}

func TestSettingsChangePauses(t *testing.T) {
	f := newFixture(t)
	cfg := f.conf
	cfg.EventDays = 180
	f.apply(cfg, false)

	paused, err := retention.Paused(f.st, cfg)
	if err != nil || paused {
		t.Fatalf("paused = %v (%v) after apply with same settings, want false", paused, err)
	}

	changed := cfg
	changed.EventDays = 30
	paused, err = retention.Paused(f.st, changed)
	if err != nil || !paused {
		t.Fatalf("paused = %v (%v) after changing event_days, want true", paused, err)
	}
	due, err := retention.Due(f.st, now)
	if err != nil || due {
		t.Errorf("due = %v (%v) right after a prune, want false", due, err)
	}
	if due, _ := retention.Due(f.st, now.Add(25*time.Hour)); !due {
		t.Error("due = false 25 h after a prune, want true")
	}

	f.apply(changed, false)
	if paused, _ := retention.Paused(f.st, changed); paused {
		t.Error("still paused after apply with the new settings")
	}
}
