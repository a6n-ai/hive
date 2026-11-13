package store

import (
	"context"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

var signalReasons = []string{"full_command", "forgotten", "corrected", "failed", "sibling"}

func endSession(t *testing.T, st *Store, id string) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *Tx) error {
		return tx.TouchSession(&model.Event{TS: 900, Agent: "codex", Session: id, Project: "/p", Kind: model.SessionEnd})
	}); err != nil {
		t.Fatal(err)
	}
}

func insertOutcome(t *testing.T, st *Store, in OutcomeInput) int64 {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *Tx) error { return tx.InsertScoreOutcome(in) }); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.db.QueryRow(`SELECT max(id) FROM score_outcomes`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOpenScoreGroupsKeepsGroupsWhole(t *testing.T) {
	st, _ := openTemp(t)
	endSession(t, st, "s1")
	for _, d := range []string{"d1", "d2", "d1", "d2", "d1", "d2"} {
		insertOutcome(t, st, OutcomeInput{MemoryID: "m", Session: "s1", Agent: "codex", TS: 100, Features: "{}", Score: 0.5,
			PromptDigest: d, ShownKey: "k", ShownText: "t"})
	}
	groups, err := st.OpenScoreGroups(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("groups = %d, want 1 group of 3 rows: %+v", len(groups), groups)
	}
	for _, o := range groups[0] {
		if o.PromptDigest != "d1" || o.Ended == 0 {
			t.Fatalf("row = %+v, want digest d1 with ended set", o)
		}
	}
}

func TestResolveScoreOutcomeOnce(t *testing.T) {
	st, _ := openTemp(t)
	id := insertOutcome(t, st, OutcomeInput{MemoryID: "m", Session: "s1", Agent: "codex", TS: 100, Features: "{}", Score: 0.5})
	label := 1
	var first, second int64
	if err := st.Write(context.Background(), func(tx *Tx) error {
		var err error
		first, err = tx.ResolveScoreOutcome(id, &label, "full_command", 1, 200)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), func(tx *Tx) error {
		var err error
		second, err = tx.ResolveScoreOutcome(id, &label, "full_command", 1, 300)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("rows affected first=%d second=%d, want 1 0", first, second)
	}
}

func TestLabeledScoreOutcomesFiltersRule(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	rows := []struct {
		in     OutcomeInput
		reason string
		open   bool
	}{
		{OutcomeInput{MemoryID: "a", Session: "s1", TS: 1, PromptDigest: "d1", LabelRule: "L1"}, "full_command", false},
		{OutcomeInput{MemoryID: "b", Session: "s1", TS: 2, PromptDigest: "d2", LabelRule: ""}, "closed_unlabeled", false},
		{OutcomeInput{MemoryID: "c", Session: "s1", TS: 3, PromptDigest: "d3", LabelRule: "L1"}, "no_lift", false},
		{OutcomeInput{MemoryID: "d", Session: "s1", TS: 4, PromptDigest: "d1", LabelRule: "L1"}, "sibling", false},
		{OutcomeInput{MemoryID: "e", Session: "s1", TS: 5, PromptDigest: "d4", LabelRule: "L1"}, "", true},
	}
	var ids []int64
	for _, r := range rows {
		ids = append(ids, insertOutcome(t, st, r.in))
	}
	for i, r := range rows {
		if r.open {
			continue
		}
		label := 1
		if err := st.Write(ctx, func(tx *Tx) error {
			_, err := tx.ResolveScoreOutcome(ids[i], &label, r.reason, 1, 500)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := st.LabeledScoreOutcomes("L1", signalReasons, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 2 || groups[0][0].ID != ids[0] || groups[0][1].ID != ids[3] {
		t.Fatalf("groups = %+v, want one group of rows a and d", groups)
	}
}

func TestRecentScoreGroupsByLatestRow(t *testing.T) {
	st, _ := openTemp(t)
	endSession(t, st, "s1")
	for _, d := range []string{"d1", "d2", "d3", "d1"} {
		insertOutcome(t, st, OutcomeInput{MemoryID: "m", Session: "s1", Agent: "codex", TS: 100, Features: "{}", Score: 0.5,
			PromptDigest: d, ShownKey: "k", ShownText: "t"})
	}
	groups, err := st.RecentScoreGroups(2)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, g := range groups {
		got[g[0].PromptDigest] = len(g)
	}
	if len(groups) != 2 || got["d1"] != 2 || got["d3"] != 1 {
		t.Fatalf("groups = %v, want d1 with 2 rows and d3 with 1", got)
	}
}

func TestOpenScoreGroupsSkipsUnendedSessions(t *testing.T) {
	st, _ := openTemp(t)
	endSession(t, st, "ended")
	if err := st.Write(context.Background(), func(tx *Tx) error {
		return tx.TouchSession(&model.Event{TS: 800, Agent: "codex", Session: "live", Project: "/p", Kind: model.Prompt})
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"live", "ended"} {
		insertOutcome(t, st, OutcomeInput{MemoryID: "m", Session: s, Agent: "codex", TS: 100, Features: "{}", PromptDigest: "d"})
	}
	groups, err := st.OpenScoreGroups(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0][0].Session != "ended" {
		t.Fatalf("groups = %+v, want only the ended session", groups)
	}
}

func TestLabeledScoreOutcomesKeepsLastGroupsAscending(t *testing.T) {
	st, _ := openTemp(t)
	var ids []int64
	for _, d := range []string{"d1", "d2", "d3"} {
		ids = append(ids, insertOutcome(t, st, OutcomeInput{MemoryID: "m", Session: "s1", TS: 1, PromptDigest: d, LabelRule: "L1"}))
	}
	label := 1
	if err := st.Write(context.Background(), func(tx *Tx) error {
		for _, id := range ids {
			if _, err := tx.ResolveScoreOutcome(id, &label, "full_command", 1, 500); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	groups, err := st.LabeledScoreOutcomes("L1", signalReasons, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0][0].PromptDigest != "d2" || groups[1][0].PromptDigest != "d3" {
		t.Fatalf("groups = %+v, want d2 then d3", groups)
	}
}
