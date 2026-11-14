package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func hasColumn(t *testing.T, st *Store, table, col string) bool {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigration0009Fresh(t *testing.T) {
	st, _ := openTemp(t)
	cols := map[string][]string{
		"sessions":    {"transcript_path", "usage_ingested_at", "usage_status"},
		"injections":  {"ts", "session", "agent", "project", "hook", "tool", "line_count", "est_tokens", "memory_ids", "holdout", "prompt_digest"},
		"usage_turns": {"session", "message_id", "ts", "model", "input", "output", "cache_read", "cache_creation"},
	}
	for table, cs := range cols {
		for _, c := range cs {
			if !hasColumn(t, st, table, c) {
				t.Errorf("%s.%s missing", table, c)
			}
		}
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='score_outcomes_ts'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("score_outcomes_ts index n=%d err=%v", n, err)
	}
}

func TestMigration0009FromV8(t *testing.T) {
	path := openAtVersion(t, 8)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id, agent, project, started, last_event, event_count) VALUES ('s1', 'claude-code', 'p', 1, 2, 3)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	st, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open at v8: %v", err)
	}
	defer func() { _ = st.Close() }()
	var tp sql.NullString
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*), MAX(transcript_path) FROM sessions WHERE id = 's1'`).Scan(&n, &tp); err != nil {
		t.Fatal(err)
	}
	if n != 1 || tp.Valid {
		t.Fatalf("rows=%d transcript_path=%v, want 1 row and NULL", n, tp)
	}
}

func TestTouchSessionTranscriptPath(t *testing.T) {
	st, _ := openTemp(t)
	touch := func(sess, path string) {
		t.Helper()
		e := &model.Event{Session: sess, Agent: "claude-code", Project: "p", TS: 1, Kind: model.PostTool, TranscriptPath: path}
		if err := st.Write(context.Background(), func(tx *Tx) error { return tx.TouchSession(e) }); err != nil {
			t.Fatal(err)
		}
	}
	get := func(sess string) sql.NullString {
		t.Helper()
		var tp sql.NullString
		if err := st.db.QueryRow(`SELECT transcript_path FROM sessions WHERE id = ?`, sess).Scan(&tp); err != nil {
			t.Fatal(err)
		}
		return tp
	}
	touch("s1", "/a.jsonl")
	touch("s1", "")
	touch("s1", "/b.jsonl")
	if got := get("s1"); got.String != "/a.jsonl" {
		t.Fatalf("transcript_path = %v, want /a.jsonl", got)
	}
	touch("s2", "")
	if got := get("s2"); got.Valid {
		t.Fatalf("s2 transcript_path = %v, want NULL", got)
	}
}

func TestInsertInjectionRoundTrip(t *testing.T) {
	st, _ := openTemp(t)
	in := InjectionInput{TS: 5, Session: "s1", Agent: "claude-code", Project: "p", Hook: "PostToolUseFailure", Tool: "shell",
		LineCount: 2, EstTokens: 30, MemoryIDs: []string{"m_1", "m_2"}, Holdout: true, PromptDigest: "d"}
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.InsertInjection(in) }); err != nil {
		t.Fatal(err)
	}
	got, err := st.InjectionsIn(Window{From: 0, To: 10})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	if got[0].ID == 0 || !reflect.DeepEqual(got[0].InjectionInput, in) {
		t.Fatalf("got %+v, want %+v", got[0], in)
	}
}

func seedAnalytics(t testing.TB, st *Store, outcomes, injections int) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *Tx) error {
		for _, id := range []string{"s1", "s2"} {
			if err := tx.TouchSession(&model.Event{TS: 1, Agent: "claude-code", Session: id, Project: "p", Kind: model.PostTool}); err != nil {
				return err
			}
			if err := tx.TouchSession(&model.Event{TS: 900, Agent: "claude-code", Session: id, Project: "p", Kind: model.SessionEnd}); err != nil {
				return err
			}
		}
		for i := 0; i < outcomes; i++ {
			if _, err := tx.tx.Exec(`INSERT INTO score_outcomes(memory_id, session, ts, features, score, label, reason) VALUES ('m', 's1', 100, '{}', 0.5, ?, ?)`,
				[]any{1, 0, nil}[i%3], []string{"", "agent", ""}[i%3]); err != nil {
				return err
			}
		}
		for i := 0; i < injections; i++ {
			if err := tx.InsertInjection(InjectionInput{TS: 100, Session: "s1", Agent: "claude-code", Project: "p", Hook: "UserPromptSubmit", EstTokens: 1, MemoryIDs: []string{"m"}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionLineCounts(t *testing.T) {
	st, _ := openTemp(t)
	seedAnalytics(t, st, 3, 0)
	err := st.Write(context.Background(), func(tx *Tx) error {
		for _, e := range []model.Event{
			{TS: 150, Session: "s1", Kind: model.PostTool, Tool: "shell", Digest: "e1"},
			{TS: 250, Session: "s1", Kind: model.PostTool, Tool: "shell", Payload: map[string]any{"error": "boom"}, Digest: "e2"},
		} {
			e.Agent, e.Project = "claude-code", "p"
			if _, err := tx.InsertEvent(&e); err != nil {
				return err
			}
		}
		for _, in := range []InjectionInput{
			{TS: 100, EstTokens: 100, Hook: "UserPromptSubmit"},
			{TS: 100, EstTokens: 50, Hook: "UserPromptSubmit"},
			{TS: 140, EstTokens: 10, Hook: "PostToolUseFailure", Tool: "shell"},
			{TS: 200, EstTokens: 10, Hook: "PostToolUseFailure", Tool: "shell"},
		} {
			in.Session, in.Agent, in.Project = "s1", "claude-code", "p"
			if err := tx.InsertInjection(in); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.SessionLineCounts("p", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := SessionLineCounts{Sessions: 2, Hits: 1, Shown: 3, InjectedTokens: 170, NextTryFixes: 1, AgentAnswered: 1}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSessionLineCountsEmpty(t *testing.T) {
	st, _ := openTemp(t)
	got, err := st.SessionLineCounts("p", 0)
	if err != nil || got != (SessionLineCounts{}) {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func BenchmarkSessionLineCounts(b *testing.B) {
	st, err := Open(filepath.Join(b.TempDir(), "inscribe.db"), false)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	seedAnalytics(b, st, 10000, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.SessionLineCounts("p", 0); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Microseconds())/1000/float64(b.N), "ms/op")
}

func seedUsage(t *testing.T, st *Store, turns ...UsageTurn) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.UpsertUsageTurns(turns)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeleteOldAnalyticsKeepsEvidence(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	err := st.Write(ctx, func(tx *Tx) error {
		e := model.Event{TS: 1, Agent: "claude-code", Session: "keep", Project: "p", Kind: model.PostTool, Digest: "d"}
		if _, err := tx.InsertEvent(&e); err != nil {
			return err
		}
		mem := &model.Memory{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "p", Key: "k", Text: "t",
			Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1}
		if _, err := tx.UpsertMemory(mem, nil, e.ID); err != nil {
			return err
		}
		for _, ts := range []int64{5, 500} {
			if err := tx.InsertInjection(InjectionInput{TS: ts, Session: "keep", Agent: "claude-code", Project: "p", Hook: "UserPromptSubmit"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seedUsage(t, st,
		UsageTurn{Session: "keep", MessageID: "a", TS: 5, Input: 1},
		UsageTurn{Session: "other", MessageID: "b", TS: 5, Input: 1},
		UsageTurn{Session: "other", MessageID: "c", TS: 500, Input: 1})
	var inj, use int64
	err = st.Write(ctx, func(tx *Tx) error {
		var err error
		inj, use, err = tx.DeleteOldAnalytics(100, 10)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if inj != 1 || use != 1 {
		t.Fatalf("deleted injections=%d usage=%d, want 1 and 1", inj, use)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM usage_turns WHERE session = 'keep'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("evidence session usage rows = %d (%v), want 1", n, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM usage_turns`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("usage rows = %d (%v), want 2", n, err)
	}
}

func TestUsageInRangeHalfOpen(t *testing.T) {
	st, _ := openTemp(t)
	seedUsage(t, st,
		UsageTurn{Session: "s", MessageID: "a", TS: 10, Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4},
		UsageTurn{Session: "s", MessageID: "b", TS: 20, Input: 10},
		UsageTurn{Session: "x", MessageID: "c", TS: 15, Input: 100})
	got, err := st.UsageInRange("s", 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := (UsageSum{Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestUsageTotalsFiltersWindowAndProject(t *testing.T) {
	st, _ := openTemp(t)
	err := st.Write(context.Background(), func(tx *Tx) error {
		for id, p := range map[string]string{"s1": "p", "s2": "q"} {
			if err := tx.TouchSession(&model.Event{TS: 1, Agent: "claude-code", Session: id, Project: p, Kind: model.PostTool}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seedUsage(t, st,
		UsageTurn{Session: "s1", MessageID: "a", TS: 10, Input: 1},
		UsageTurn{Session: "s1", MessageID: "b", TS: 20, Input: 10},
		UsageTurn{Session: "s2", MessageID: "c", TS: 10, Input: 100})
	if got, err := st.UsageTotals(Window{From: 10, To: 20, Project: "p"}); err != nil || got.Input != 1 {
		t.Fatalf("project p: %+v %v, want input 1", got, err)
	}
	if got, err := st.UsageTotals(Window{From: 10, To: 20}); err != nil || got.Input != 101 {
		t.Fatalf("all projects: %+v %v, want input 101", got, err)
	}
}

func TestUsageStatusCounts(t *testing.T) {
	st, _ := openTemp(t)
	err := st.Write(context.Background(), func(tx *Tx) error {
		for _, s := range []struct {
			id, project, status string
			ended               int64
		}{
			{"a", "p", "missing", 10}, {"b", "p", "unparsed", 15}, {"c", "p", "ok", 15},
			{"d", "p", "missing", 20}, {"e", "q", "missing", 12},
		} {
			if err := tx.TouchSession(&model.Event{TS: 1, Agent: "claude-code", Session: s.id, Project: s.project, Kind: model.PostTool}); err != nil {
				return err
			}
			if _, err := tx.tx.Exec(`UPDATE sessions SET ended = ? WHERE id = ?`, s.ended, s.id); err != nil {
				return err
			}
			if err := tx.MarkIngested(s.id, s.status, 1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	missing, unparsed, err := st.UsageStatusCounts(Window{From: 10, To: 20, Project: "p"})
	if err != nil || missing != 1 || unparsed != 1 {
		t.Fatalf("got missing=%d unparsed=%d err=%v, want 1 and 1", missing, unparsed, err)
	}
	if missing, _, _ = st.UsageStatusCounts(Window{From: 10, To: 20}); missing != 2 {
		t.Fatalf("all projects missing = %d, want 2", missing)
	}
}

func TestSessionsToIngestSkipsMissingWithoutNewerEvent(t *testing.T) {
	st, _ := openTemp(t)
	err := st.Write(context.Background(), func(tx *Tx) error {
		if err := tx.TouchSession(&model.Event{TS: 5, Agent: "claude-code", Session: "s", Project: "p", Kind: model.PostTool}); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`UPDATE sessions SET ended = 5, transcript_path = '/t' WHERE id = 's'`); err != nil {
			return err
		}
		return tx.MarkIngested("s", "missing", 10)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.SessionsToIngest("claude-code", 10); err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v, want none", got, err)
	}
	err = st.Write(context.Background(), func(tx *Tx) error {
		if err := tx.TouchSession(&model.Event{TS: 50, Agent: "claude-code", Session: "s", Project: "p", Kind: model.PostTool}); err != nil {
			return err
		}
		_, err := tx.tx.Exec(`UPDATE sessions SET ended = last_event WHERE id = 's'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.SessionsToIngest("claude-code", 10); err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v, want 1 after a newer event", got, err)
	}
}

func seedOutcomes(t *testing.T, st *Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *Tx) error {
		for i, s := range []struct{ id, project string }{{"a", "p"}, {"b", "q"}} {
			if err := tx.TouchSession(&model.Event{TS: 1, Agent: "claude-code", Session: s.id, Project: s.project, Kind: model.PostTool, Digest: string(rune('x' + i))}); err != nil {
				return err
			}
		}
		for _, o := range []struct {
			mem, sess, reason string
			ts                int64
			label             any
		}{
			{"m1", "a", "agent", 10, 1}, {"m2", "a", "agent", 11, 0}, {"m3", "a", "full_command", 12, 1},
			{"m4", "a", "", 13, nil}, {"m5", "b", "review", 14, 1}, {"m6", "a", "agent", 20, 1},
		} {
			if _, err := tx.tx.Exec(`INSERT INTO score_outcomes(memory_id, session, ts, features, score, label, reason) VALUES (?, ?, ?, '', 0, ?, ?)`,
				o.mem, o.sess, o.ts, o.label, o.reason); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHitsIn(t *testing.T) {
	st, _ := openTemp(t)
	seedOutcomes(t, st)
	got, err := st.HitsIn(Window{From: 10, To: 20, Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Hit{{"m1", "a", "agent", 10}, {"m3", "a", "full_command", 12}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if all, _ := st.HitsIn(Window{From: 10, To: 20}); len(all) != 3 {
		t.Fatalf("all projects = %+v, want 3", all)
	}
}

func TestOutcomeCounts(t *testing.T) {
	st, _ := openTemp(t)
	seedOutcomes(t, st)
	got, err := st.OutcomeCounts(Window{From: 10, To: 20, Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	want := []OutcomeCount{{"", -1, 1}, {"agent", 0, 1}, {"agent", 1, 1}, {"full_command", 1, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEventsInInclusiveAndMemoryText(t *testing.T) {
	st, _ := openTemp(t)
	err := st.Write(context.Background(), func(tx *Tx) error {
		for i, ts := range []int64{5, 10, 20, 25} {
			if _, err := tx.InsertEvent(&model.Event{TS: ts, Agent: "claude-code", Session: "s", Project: "p", Kind: model.PostTool, Digest: string(rune('a' + i))}); err != nil {
				return err
			}
		}
		_, err := tx.InsertEvent(&model.Event{TS: 15, Agent: "claude-code", Session: "other", Project: "p", Kind: model.PostTool, Digest: "z"})
		if err != nil {
			return err
		}
		_, err = tx.UpsertMemory(&model.Memory{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "p", Key: "k", Text: "hello",
			Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1}, nil, 0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.EventsIn("s", 10, 20)
	if err != nil || len(evs) != 2 || evs[0].TS != 10 || evs[1].TS != 20 {
		t.Fatalf("got %+v err=%v, want ts 10 and 20", evs, err)
	}
	var id string
	if err := st.db.QueryRow(`SELECT id FROM memories`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if txt, err := st.MemoryText(id); err != nil || txt != "hello" {
		t.Fatalf("text %q err %v", txt, err)
	}
	if txt, err := st.MemoryText("gone"); err != nil || txt != "" {
		t.Fatalf("gone: %q %v", txt, err)
	}
}

func TestPromptsIn(t *testing.T) {
	st, _ := openTemp(t)
	err := st.Write(context.Background(), func(tx *Tx) error {
		for _, e := range []model.Event{
			{TS: 10, Session: "s", Project: "p", Kind: model.Prompt, Digest: "a"},
			{TS: 20, Session: "s", Project: "p", Kind: model.TurnEnd, Digest: "b"},
			{TS: 30, Session: "s", Project: "p", Kind: model.Prompt, Digest: "c"},
			{TS: 15, Session: "o", Project: "q", Kind: model.Prompt, Digest: "d"},
			{TS: 40, Session: "s", Project: "p", Kind: model.Prompt, Digest: "e"},
		} {
			e.Agent = "claude-code"
			if _, err := tx.InsertEvent(&e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.PromptsIn(Window{From: 0, To: 40, Project: "p"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v err=%v, want 2 prompts", got, err)
	}
	if got[0].TS != 10 || got[0].Digest != "a" || got[0].NextPromptTS != 30 || got[0].TurnEndTS != 20 {
		t.Fatalf("first %+v", got[0])
	}
	if got[1].TS != 30 || got[1].NextPromptTS != 40 || got[1].TurnEndTS != 0 {
		t.Fatalf("second %+v", got[1])
	}
	all, err := st.PromptsIn(Window{From: 0, To: 100})
	if err != nil || len(all) != 4 || all[3].NextPromptTS != 0 {
		t.Fatalf("all %+v err=%v", all, err)
	}
}
