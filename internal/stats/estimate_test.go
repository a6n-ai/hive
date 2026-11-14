package stats

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

var evSeq int

func (e *env) event(t *testing.T, session string, ts int64, kind model.EventKind) int64 {
	t.Helper()
	evSeq++
	var id int64
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		ev := model.Event{TS: ts, Agent: "claude-code", Session: session, Project: "p", Kind: kind, Digest: fmt.Sprint("d", evSeq)}
		if _, err := tx.InsertEvent(&ev); err != nil {
			return err
		}
		id = ev.ID
		return tx.TouchSession(&ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) memory(t *testing.T, key string, evIDs ...int64) string {
	t.Helper()
	var id string
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		m := &model.Memory{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "p", Key: key, Text: key,
			Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1}
		var first int64
		if len(evIDs) > 0 {
			first = evIDs[0]
		}
		got, err := tx.UpsertMemory(m, nil, first)
		if err != nil {
			return err
		}
		id = got.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evIDs[min(1, len(evIDs)):] {
		if _, err := e.db.Exec(`INSERT OR IGNORE INTO memory_evidence(memory_id, event_id) VALUES (?, ?)`, id, ev); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (e *env) hit(t *testing.T, mem, session, reason string, ts int64) {
	t.Helper()
	e.event(t, session, 1, model.SessionStart)
	if _, err := e.db.Exec(`INSERT INTO score_outcomes(memory_id, session, ts, features, score, label, reason) VALUES (?, ?, ?, '', 0, 1, ?)`,
		mem, session, ts, reason); err != nil {
		t.Fatal(err)
	}
}

func (e *env) usage(t *testing.T, session string, turns ...store.UsageTurn) {
	t.Helper()
	for i := range turns {
		turns[i].Session = session
		turns[i].MessageID = fmt.Sprint(session, i, turns[i].TS)
	}
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.UpsertUsageTurns(turns)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

var wide = store.Window{From: 0, To: 1 << 40}

func credit(t *testing.T, e *env) ([]MemoryCredit, int) {
	t.Helper()
	got, skipped, err := Credit(e.st, wide, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].MemoryID != got[j].MemoryID {
			return got[i].MemoryID < got[j].MemoryID
		}
		return got[i].Source < got[j].Source
	})
	return got, skipped
}

func TestCreditEarliestSession(t *testing.T) {
	e := newEnv(t)
	e.event(t, "s1", 1000, model.Prompt)
	e1 := e.event(t, "s1", 2000, model.PostTool)
	e.event(t, "s2", 5000, model.Prompt)
	e2 := e.event(t, "s2", 6000, model.PostTool)
	m := e.memory(t, "k", e2, e1)
	e.usage(t, "s1", store.UsageTurn{TS: 1500, Input: 100, Output: 50})
	e.usage(t, "s2", store.UsageTurn{TS: 5500, Input: 7000})
	e.hit(t, m, "s3", "agent", 9000)
	got, skipped := credit(t, e)
	if skipped != 0 || len(got) != 1 {
		t.Fatalf("got %+v skipped=%d", got, skipped)
	}
	if want := (MemoryCredit{MemoryID: m, Source: "agent", Tokens: 150, ActiveMS: 1000}); got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
}

func TestCreditExcludesCacheRead(t *testing.T) {
	e := newEnv(t)
	e.event(t, "s", 1000, model.Prompt)
	ev := e.event(t, "s", 3000, model.PostTool)
	m := e.memory(t, "k", ev)
	e.usage(t, "s",
		store.UsageTurn{TS: 1500, Input: 100, Output: 50, CacheRead: 9999, CacheCreation: 10},
		store.UsageTurn{TS: 3000, Input: 1},
		store.UsageTurn{TS: 3001, Input: 500})
	e.hit(t, m, "h", "review", 9000)
	got, _ := credit(t, e)
	if len(got) != 1 || got[0].Tokens != 161 || got[0].ActiveMS != 2000 {
		t.Fatalf("got %+v, want tokens 161 and 2000ms", got)
	}
}

func TestActiveMSIdleGap(t *testing.T) {
	min := int64(time.Minute / time.Millisecond)
	evs := []model.Event{{TS: 0}, {TS: min}, {TS: 21 * min}}
	if got := ActiveMS(evs, 5*time.Minute); got != 60000 {
		t.Fatalf("got %d, want 60000", got)
	}
	if got := ActiveMS(nil, time.Minute); got != 0 {
		t.Fatalf("empty = %d", got)
	}
}

func TestCreditSplitsSharedWindow(t *testing.T) {
	e := newEnv(t)
	e.event(t, "s", 1000, model.Prompt)
	ev := e.event(t, "s", 3000, model.PostTool)
	a, b := e.memory(t, "a", ev), e.memory(t, "b", ev)
	e.usage(t, "s", store.UsageTurn{TS: 2000, Input: 101})
	e.hit(t, a, "h", "agent", 9000)
	e.hit(t, b, "h", "agent", 9000)
	got, _ := credit(t, e)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, c := range got {
		if c.Tokens != 50 || c.ActiveMS != 1000 {
			t.Fatalf("credit %+v, want tokens 50 and 1000ms", c)
		}
	}
}

func TestCreditOncePerSession(t *testing.T) {
	e := newEnv(t)
	e.event(t, "s", 1000, model.Prompt)
	ev := e.event(t, "s", 2000, model.PostTool)
	m := e.memory(t, "k", ev)
	e.usage(t, "s", store.UsageTurn{TS: 1500, Input: 10})
	e.hit(t, m, "h1", "agent", 9000)
	e.hit(t, m, "h1", "agent", 9100)
	got, _ := credit(t, e)
	if len(got) != 1 {
		t.Fatalf("same hit-session: got %+v, want 1 credit", got)
	}
	e.hit(t, m, "h2", "agent", 9200)
	got, _ = credit(t, e)
	if len(got) != 2 {
		t.Fatalf("two hit-sessions: got %+v, want 2 credits", got)
	}
}

func TestCreditRangeBySource(t *testing.T) {
	e := newEnv(t)
	e.event(t, "s", 1000, model.Prompt)
	ev := e.event(t, "s", 2000, model.PostTool)
	e.usage(t, "s", store.UsageTurn{TS: 1500, Input: 10})
	want := map[string]string{}
	for reason, source := range map[string]string{"full_command": "rule", "agent": "agent", "review": "review"} {
		m := e.memory(t, reason, ev)
		e.hit(t, m, "h", reason, 9000)
		want[m] = source
	}
	got, _ := credit(t, e)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, c := range got {
		if c.Source != want[c.MemoryID] {
			t.Fatalf("credit %+v, want source %s", c, want[c.MemoryID])
		}
	}
}

func TestCreditSkipsNoEvidence(t *testing.T) {
	e := newEnv(t)
	m := e.memory(t, "orphan")
	e.hit(t, m, "h", "agent", 9000)
	e.hit(t, m, "h2", "agent", 9100)
	e.event(t, "s", 1000, model.Prompt)
	ev := e.event(t, "s", 2000, model.PostTool)
	nousage := e.memory(t, "nousage", ev)
	e.hit(t, nousage, "h", "agent", 9000)
	got, skipped := credit(t, e)
	if len(got) != 0 || skipped != 2 {
		t.Fatalf("got %+v skipped=%d, want none and 2", got, skipped)
	}
}
