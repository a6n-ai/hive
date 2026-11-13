package store

import (
	"context"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func touch(t *testing.T, st *Store, id string, ts int64, kind model.EventKind) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *Tx) error {
		return tx.TouchSession(&model.Event{TS: ts, Agent: "claude-code", Session: id, Project: "/p", Kind: kind})
	}); err != nil {
		t.Fatal(err)
	}
}

func sessionEnded(t *testing.T, st *Store, id string) int64 {
	t.Helper()
	var ended *int64
	if err := st.db.QueryRow(`SELECT ended FROM sessions WHERE id = ?`, id).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	if ended == nil {
		return 0
	}
	return *ended
}

func TestCloseSessionsEndsAtLastEvent(t *testing.T) {
	st, _ := openTemp(t)
	touch(t, st, "a", 100, model.Prompt)
	touch(t, st, "a", 200, model.PostTool)
	touch(t, st, "b", 300, model.Prompt)
	touch(t, st, "done", 400, model.SessionEnd)
	open, err := st.OpenSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].ID != "b" || open[1].ID != "a" || open[1].LastEvent != 200 || open[1].Agent != "claude-code" {
		t.Fatalf("open = %+v, want b then a (last 200), not the ended one", open)
	}
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.CloseSessions([]string{"a", "done"}) }); err != nil {
		t.Fatal(err)
	}
	if got := sessionEnded(t, st, "a"); got != 200 {
		t.Fatalf("a ended = %d, want its last event 200", got)
	}
	if got := sessionEnded(t, st, "done"); got != 400 {
		t.Fatalf("an already ended session moved to %d", got)
	}
	if got := sessionEnded(t, st, "b"); got != 0 {
		t.Fatalf("b ended = %d, want still open", got)
	}
}

func TestNewEventReopensSession(t *testing.T) {
	st, _ := openTemp(t)
	touch(t, st, "a", 100, model.Prompt)
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.CloseSessions([]string{"a"}) }); err != nil {
		t.Fatal(err)
	}
	touch(t, st, "a", 500, model.Prompt)
	if got := sessionEnded(t, st, "a"); got != 0 {
		t.Fatalf("resumed session still ended at %d", got)
	}
	touch(t, st, "a", 600, model.SessionEnd)
	if got := sessionEnded(t, st, "a"); got != 600 {
		t.Fatalf("ended = %d, want 600", got)
	}
}

func TestIdleSessionsIncludesEndedRecentSessions(t *testing.T) {
	st, _ := openTemp(t)
	touch(t, st, "old", 100, model.Prompt)
	touch(t, st, "closed", 900, model.Prompt)
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.CloseSessions([]string{"closed"}) }); err != nil {
		t.Fatal(err)
	}
	touch(t, st, "busy", 950, model.Prompt)
	ids, err := st.IdleSessions("claude-code", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "closed" || ids[1] != "old" {
		t.Fatalf("idle = %v, want closed (ended though recent) then old", ids)
	}
}
