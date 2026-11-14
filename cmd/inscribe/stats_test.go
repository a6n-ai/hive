package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/stats"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func TestStatsIngestCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INSCRIBE_HOME", home)
	tp := filepath.Join(home, "t.jsonl")
	if err := os.WriteFile(tp, []byte(`{"type":"assistant","timestamp":"2026-11-14T10:00:00Z","message":{"id":"m","model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	ev := inscribe.Event{Agent: "claude-code", Session: "s1", Project: "p", Kind: model.SessionEnd, TS: 1, TranscriptPath: tp}
	if err := h.Capture(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()

	out, errOut, code := runCLI(t, home, "", nil, "stats", "ingest", "--catch-up")
	if code != 0 || out != "ingested 1 session(s)\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func goldenReport() stats.Report {
	return stats.Report{
		Window: store.Window{From: 0, To: 7 * 24 * 3600 * 1000, Project: "p"},
		Measured: stats.Measured{
			InjectedByHook:     map[string]int{"SessionStart": 1200, "UserPromptSubmit": 3400, "PostToolUseFailure": 210, "feedback_ask": 380},
			SessionStartTokens: 1200, SessionStartLines: 42,
			Rates: map[string]stats.Rate{
				"rule":   {Misses: 28, MissReasons: map[string]int{"forgotten": 28}},
				"agent":  {Hits: 4, Misses: 5, MissReasons: map[string]int{"agent": 3, "agent_wrong": 2}},
				"review": {Hits: 11, Misses: 45, MissReasons: map[string]int{"review": 34, "review_wrong": 11}},
			},
			Unlabeled: 17, Shown: 82, Wrong: 13, Rated: 65,
			Penalized: []stats.Penalty{{MemoryID: "m_ab12", Penalty: 0.2}, {MemoryID: "m_cd34", Penalty: 0.05}},
			Asks:      1, AsksAnswered: 1, AgentRows: 11,
			MemoriesUsed: 31, MemoriesActive: 148, Unused30d: make([]string, 60),
			TopInjected: []stats.Count{{MemoryID: "m_ab12", N: 9}, {MemoryID: "m_cd34", N: 7}},
			ByStatus:    map[string]int{"active": 140, "candidate": 5, "forgotten": 3},
			DBBytes:     14_200_000, Events: 1306, Trajectory: 1616,
			NextTryFixes: 3, NextTryShown: 5, Corrections: 1, Sessions: 5,
			Usage:    store.UsageSum{Input: 12300, Output: 48200, CacheRead: 2_100_000, CacheCreation: 310400},
			ActiveMS: (3*60 + 12) * 60000, UsageMissing: 1,
		},
		Estimate: stats.Estimate{Hidden: true, LabeledHits: 15},
		Holdout:  stats.HoldoutResult{State: "off"},
	}
}

func TestStatsGolden(t *testing.T) {
	got := stats.Text(goldenReport(), false)
	want, err := os.ReadFile("testdata/stats_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestStatsJSONKeys(t *testing.T) {
	home := t.TempDir()
	out := runOK(t, home, "", "stats", "--json")
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(top) != 4 {
		t.Fatalf("keys %v", top)
	}
	for _, k := range []string{"window", "measured", "estimate", "holdout"} {
		if _, ok := top[k]; !ok {
			t.Fatalf("missing %s in %s", k, out)
		}
	}
}

func TestStatsJSONEmptySlices(t *testing.T) {
	out := runOK(t, t.TempDir(), "", "stats", "--json")
	for _, field := range []string{`"penalized":`, `"unused_30d":`, `"top_injected":`, `"top":`} {
		if !strings.Contains(out, field+"[]") && !strings.Contains(out, field+" []") {
			t.Fatalf("%s is not []:\n%s", field, out)
		}
	}
}

func TestStatsEmptyStore(t *testing.T) {
	out, errOut, code := runCLI(t, t.TempDir(), "", nil, "stats", "--all", "--days", "30")
	if code != 0 || !strings.Contains(out, "not enough data (0 labeled hits, need 30)") || !strings.Contains(out, "\nHoldout\n  off\n") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestStatsExplain(t *testing.T) {
	home := t.TempDir()
	if strings.Contains(runOK(t, home, "", "stats"), "Overstates") {
		t.Fatal("bias bullets without --explain")
	}
	out := runOK(t, home, "", "stats", "--explain")
	for _, want := range []string{"Overstates", "Understates", "Self-report", "Cost is low", "Skips", "Shared windows", "not independent"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
