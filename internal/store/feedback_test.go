package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
)

func seedPromptGroups(t *testing.T, st *Store, session string, digests ...string) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(digests))
	for _, d := range digests {
		ids = append(ids, insertOutcome(t, st, OutcomeInput{MemoryID: "m", Session: session, Agent: "codex", TS: 100,
			Features: "{}", Score: 0.5, PromptDigest: d, ShownKey: "k", ShownText: "t"}))
	}
	return ids
}

func rowIDs(rows []ScoreOutcome) []int64 {
	out := make([]int64, 0, len(rows))
	for _, o := range rows {
		out = append(out, o.ID)
	}
	return out
}

func TestUnaskedOutcomesWindow(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	ids := seedPromptGroups(t, st, "s1", "d1", "d2", "d3", "d4", "d5", "d6")
	seedPromptGroups(t, st, "s2", "x1")

	got, err := st.UnaskedOutcomes("s1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{ids[5], ids[4], ids[3], ids[2], ids[1]}; !slices.Equal(rowIDs(got), want) {
		t.Fatalf("ids = %v, want newest 5 groups %v", rowIDs(got), want)
	}

	if err := st.Write(ctx, func(tx *Tx) error { return tx.InsertAsk("a1", "s1", 500, []int64{ids[5]}) }); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *Tx) error {
		_, err := tx.ResolveScoreOutcome(ids[4], nil, "forgotten", 1, 600)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err = st.UnaskedOutcomes("s1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{ids[3], ids[2], ids[1]}; !slices.Equal(rowIDs(got), want) {
		t.Fatalf("ids = %v, want %v (asked d6 and resolved d5 excluded)", rowIDs(got), want)
	}
}

func TestAskRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	ids := seedPromptGroups(t, st, "s1", "d1", "d2")
	if err := st.Write(ctx, func(tx *Tx) error { return tx.InsertAsk("a1", "s1", 500, ids) }); err != nil {
		t.Fatal(err)
	}

	var a *Ask
	if err := st.Write(ctx, func(tx *Tx) error {
		var err error
		a, err = tx.GetAsk("a1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if a.Session != "s1" || a.TS != 500 || a.Answered || !slices.Equal(rowIDs(a.Rows), ids) {
		t.Fatalf("ask = %+v, want session s1, ts 500, unanswered, rows %v", a, ids)
	}

	if err := st.Write(ctx, func(tx *Tx) error { return tx.AnswerAsk("a1") }); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *Tx) error {
		var err error
		a, err = tx.GetAsk("a1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !a.Answered {
		t.Fatalf("ask = %+v, want answered", a)
	}

	err := st.Write(ctx, func(tx *Tx) error {
		_, err := tx.GetAsk("missing")
		return err
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetAsk(missing) err = %v, want sql.ErrNoRows", err)
	}
}

func TestTxLastAskTS(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	var got int64
	if err := st.Write(ctx, func(tx *Tx) error {
		if err := tx.InsertAsk("a1", "s1", 500, nil); err != nil {
			return err
		}
		var err error
		got, err = tx.LastAskTS("s1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got != 500 {
		t.Fatalf("Tx.LastAskTS = %d, want 500 (the ask inserted in the same transaction)", got)
	}
}

func TestLastAskTS(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	if ts, err := st.LastAskTS("s1"); err != nil || ts != 0 {
		t.Fatalf("LastAskTS before any ask = %d, %v; want 0", ts, err)
	}
	for _, a := range []struct {
		id string
		ts int64
	}{{"a1", 500}, {"a2", 700}} {
		if err := st.Write(ctx, func(tx *Tx) error { return tx.InsertAsk(a.id, "s1", a.ts, nil) }); err != nil {
			t.Fatal(err)
		}
	}
	if ts, err := st.LastAskTS("s1"); err != nil || ts != 700 {
		t.Fatalf("LastAskTS = %d, %v; want 700", ts, err)
	}
	if ts, err := st.LastAskTS("s2"); err != nil || ts != 0 {
		t.Fatalf("LastAskTS(other session) = %d, %v; want 0", ts, err)
	}
}
