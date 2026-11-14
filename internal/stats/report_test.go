package stats

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func (e *env) outcome(t *testing.T, mem, session, reason string, label sql.NullInt64) {
	t.Helper()
	e.event(t, session, 1, model.SessionStart)
	if _, err := e.db.Exec(`INSERT INTO score_outcomes(memory_id, session, ts, features, score, label, reason) VALUES (?, ?, 5000, '', 0, ?, ?)`,
		mem, session, label, reason); err != nil {
		t.Fatal(err)
	}
}

func lbl(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

var reportCfg = config.StatsCfg{SessionLine: true, HoldoutMinSample: 100, IdleMinutes: 5}

func build(t *testing.T, e *env, cfg config.StatsCfg) Report {
	t.Helper()
	r, err := Build(e.st, store.Window{From: 0, To: 1 << 40, Project: "p"}, cfg, time.UnixMilli(10000))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReportSourceSplit(t *testing.T) {
	e := newEnv(t)
	e.outcome(t, "m1", "s1", "full_command", lbl(1))
	e.outcome(t, "m1", "s2", "forgotten", lbl(0))
	e.outcome(t, "m1", "s3", "forgotten", lbl(0))
	e.outcome(t, "m2", "s4", "agent", lbl(1))
	e.outcome(t, "m2", "s5", "agent_wrong", lbl(0))
	e.outcome(t, "m3", "s6", "review", lbl(1))
	e.outcome(t, "m3", "s7", "review", lbl(1))
	e.outcome(t, "m3", "s8", "closed_unlabeled", sql.NullInt64{})
	m := build(t, e, reportCfg).Measured
	if got := m.Rates["rule"]; got.Hits != 1 || got.Misses != 2 || got.MissReasons["forgotten"] != 2 {
		t.Fatalf("rule %+v", got)
	}
	if got := m.Rates["agent"]; got.Hits != 1 || got.Misses != 1 || got.MissReasons["agent_wrong"] != 1 {
		t.Fatalf("agent %+v", got)
	}
	if got := m.Rates["review"]; got.Hits != 2 || got.Misses != 0 {
		t.Fatalf("review %+v", got)
	}
	if m.Unlabeled != 1 || m.Shown != 8 || m.Wrong != 1 || m.Rated != 4 || m.AgentRows != 2 {
		t.Fatalf("unlabeled=%d shown=%d wrong=%d rated=%d agentRows=%d", m.Unlabeled, m.Shown, m.Wrong, m.Rated, m.AgentRows)
	}
}

func TestReportHiddenEstimate(t *testing.T) {
	e := newEnv(t)
	for i := range 15 {
		e.outcome(t, fmt.Sprint("m", i), fmt.Sprint("s", i), "agent", lbl(1))
	}
	r := build(t, e, reportCfg)
	if !r.Estimate.Hidden || r.Estimate.LabeledHits != 15 || r.Estimate.Top != nil {
		t.Fatalf("%+v", r.Estimate)
	}
	if txt := Text(r, false); !strings.Contains(txt, "not enough data (15 labeled hits, need 30)") {
		t.Fatalf("text:\n%s", txt)
	}
}

func seedConsistent(t *testing.T, e *env) {
	t.Helper()
	e.event(t, "disc", 100, model.Prompt)
	ev := e.event(t, "disc", 200, model.PostTool)
	e.usage(t, "disc", store.UsageTurn{TS: 150, Output: 4000})
	for i := range 30 {
		m := e.memory(t, fmt.Sprint("k", i), ev)
		e.outcome(t, m, fmt.Sprint("hs", i), "full_command", lbl(1))
	}
	wbSeed(t, e, 40, true, alt(1000, 1200), "m")
	wbSeed(t, e, 40, false, alt(900, 1100), "m")
}

func TestReportNoNetUnlessConsistent(t *testing.T) {
	e := newEnv(t)
	seedConsistent(t, e)
	cfg := reportCfg
	cfg.Holdout, cfg.HoldoutMinSample = 0.05, 100
	r := build(t, e, cfg)
	if r.Holdout.State != "collecting" || r.Estimate.Net != nil || strings.Contains(Text(r, false), "net ") {
		t.Fatalf("holdout %+v net %v", r.Holdout, r.Estimate.Net)
	}
	cfg.HoldoutMinSample = 40
	r = build(t, e, cfg)
	if r.Holdout.Verdict != "consistent" || r.Estimate.Net == nil {
		t.Fatalf("holdout %+v net %v hidden=%v hi=%d", r.Holdout, r.Estimate.Net, r.Estimate.Hidden, r.Estimate.HiTokens)
	}
	if want := r.Estimate.HiTokens - r.Estimate.InjectedTokens; *r.Estimate.Net != want {
		t.Fatalf("net %d want %d", *r.Estimate.Net, want)
	}
	if !strings.Contains(Text(r, false), "net ") {
		t.Fatal("net line missing")
	}
	cfg.Holdout = 0
	if r = build(t, e, cfg); r.Holdout.State == "off" {
		t.Fatal("holdout rows exist, state must not be off")
	}
}

func TestReportBuildValues(t *testing.T) {
	e := newEnv(t)
	t.Setenv("INSCRIBE_HOME", e.dir)
	e.event(t, "s1", 1000, model.SessionStart)
	e.event(t, "s1", 2000, model.Correction)
	if _, err := e.db.Exec(`INSERT INTO feedback_asks(id, session, ts, answered) VALUES ('a', 's1', 3000, 1), ('b', 's1', 3500, 0)`); err != nil {
		t.Fatal(err)
	}
	mem := e.memory(t, "k")
	e.memory(t, "unused")
	if _, err := e.db.Exec(`UPDATE memories SET penalty = 0.2 WHERE id = ?`, mem); err != nil {
		t.Fatal(err)
	}
	err := e.st.Write(t.Context(), func(tx *store.Tx) error {
		for _, in := range []store.InjectionInput{
			{TS: 1500, Session: "s1", Agent: "claude-code", Project: "p", Hook: "SessionStart", LineCount: 3, EstTokens: 40, MemoryIDs: []string{mem}},
			{TS: 1600, Session: "s1", Agent: "claude-code", Project: "p", Hook: "UserPromptSubmit", LineCount: 1, EstTokens: 10, MemoryIDs: []string{mem}},
			{TS: 1700, Session: "s1", Agent: "claude-code", Project: "p", Hook: "feedback_ask", LineCount: 1, EstTokens: 5},
		} {
			if err := tx.InsertInjection(in); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := build(t, e, reportCfg)
	m := r.Measured
	if m.InjectedByHook["SessionStart"] != 40 || m.InjectedByHook["UserPromptSubmit"] != 10 || m.InjectedByHook["feedback_ask"] != 5 {
		t.Fatalf("by hook %v", m.InjectedByHook)
	}
	if m.SessionStartTokens != 40 || m.SessionStartLines != 3 || r.Estimate.InjectedTokens != 55 {
		t.Fatalf("ss %d/%d inj %d", m.SessionStartTokens, m.SessionStartLines, r.Estimate.InjectedTokens)
	}
	if m.Asks != 2 || m.AsksAnswered != 1 {
		t.Fatalf("asks %d/%d", m.AsksAnswered, m.Asks)
	}
	if m.MemoriesUsed != 1 || m.MemoriesActive != 2 || len(m.Unused30d) != 1 || len(m.TopInjected) != 1 || m.TopInjected[0].N != 2 {
		t.Fatalf("memories %+v", m)
	}
	if len(m.Penalized) != 1 || m.Penalized[0].Penalty != 0.2 || m.ByStatus["active"] != 2 || m.ByType["fix"] != 2 {
		t.Fatalf("penalized/by %+v", m)
	}
	if m.Corrections != 1 || m.Sessions != 1 || m.Events != 2 || m.DBBytes <= 0 {
		t.Fatalf("counts %+v", m)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "inscribe.db")); err != nil {
		t.Fatal(err)
	}
}

func TestReportEstimateShown(t *testing.T) {
	e := newEnv(t)
	seedConsistent(t, e)
	r := build(t, e, reportCfg)
	if r.Estimate.Hidden || r.Estimate.LoTokens != r.Estimate.BySource["rule"] || r.Estimate.HiTokens != 3990 || len(r.Estimate.Top) != 5 {
		t.Fatalf("%+v", r.Estimate)
	}
	txt := Text(r, false)
	if !strings.Contains(txt, "injected       ") || !strings.Contains(txt, "by source      rule ") {
		t.Fatalf("text:\n%s", txt)
	}
}
