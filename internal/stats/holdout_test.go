package stats

import (
	"context"
	"fmt"
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func TestDraw(t *testing.T) {
	first, second := Draw("s", "d", 1, 0.3), Draw("s", "d", 1, 0.3)
	if first != second {
		t.Fatal("Draw is not deterministic")
	}
	holdouts := 0
	for i := range 10000 {
		if Draw("s", "d", i, 0) {
			t.Fatal("share 0 drew a holdout")
		}
		if Draw(fmt.Sprintf("s%d", i), "d", i, 0.05) {
			holdouts++
		}
	}
	if holdouts < 400 || holdouts > 600 {
		t.Fatalf("share 0.05 gave %d holdouts of 10000, want 400-600", holdouts)
	}
	var yes, no int
	for i := range 100 {
		if Draw(fmt.Sprintf("s%d", i), "d", 0, 0.5) {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("one digest over 100 sessions: %d held out, %d not", yes, no)
	}
}

func wbSeed(t *testing.T, e *env, n int, holdout bool, tokens func(i int) int, line string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		for i := range n {
			sess := fmt.Sprintf("%v-%d-%d", holdout, i, evSeq)
			ts := int64(1000 + i)
			dg := sess + "-p"
			for _, ev := range []model.Event{
				{TS: ts, Session: sess, Kind: model.Prompt, Digest: dg},
				{TS: ts + 10, Session: sess, Kind: model.TurnEnd, Digest: sess + "-t"},
			} {
				ev.Agent, ev.Project = "claude-code", "p"
				if _, err := tx.InsertEvent(&ev); err != nil {
					return err
				}
			}
			if _, err := tx.UpsertUsageTurns([]store.UsageTurn{{Session: sess, MessageID: "m", TS: ts + 5, Output: tokens(i)}}); err != nil {
				return err
			}
			if err := tx.InsertInjection(store.InjectionInput{TS: ts, Session: sess, Agent: "claude-code", Project: "p", Hook: "UserPromptSubmit",
				LineCount: 1, MemoryIDs: []string{line}, Holdout: holdout, PromptDigest: dg}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evSeq++
}

func alt(a, b int) func(int) int {
	return func(i int) int {
		if i%2 == 0 {
			return a
		}
		return b
	}
}

func compareSeeded(t *testing.T, holdoutAt, normalAt func(int) int, minSample int, expected float64) HoldoutResult {
	t.Helper()
	e := newEnv(t)
	wbSeed(t, e, 40, true, holdoutAt, "m")
	wbSeed(t, e, 40, false, normalAt, "m")
	res, err := Compare(e.st, store.Window{From: 0, To: 1 << 40}, minSample, expected)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCompareCollecting(t *testing.T) {
	e := newEnv(t)
	wbSeed(t, e, 37, true, alt(1, 2), "m")
	wbSeed(t, e, 410, false, alt(1, 2), "m")
	res, err := Compare(e.st, wide, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "collecting" || res.Verdict != "" || res.Holdout.N != 37 || res.Normal.N != 410 {
		t.Fatalf("got %+v", res)
	}
}

func TestCompareConsistent(t *testing.T) {
	res := compareSeeded(t, alt(1300, 1500), alt(1000, 1200), 20, 300)
	if res.State != "compared" || res.Diff != 300 || res.Verdict != "consistent" || res.Lo > 300 || res.Hi < 300 || res.Lo <= 0 {
		t.Fatalf("got %+v", res)
	}
	if res.Holdout.MeanTokens != 1400 || res.Normal.MeanTokens != 1100 || res.Expected != 300 {
		t.Fatalf("group stats %+v", res)
	}
}

func TestCompareContradicted(t *testing.T) {
	if res := compareSeeded(t, alt(1300, 1500), alt(1000, 1200), 20, 100); res.Verdict != "contradicted" {
		t.Fatalf("below: %+v", res)
	}
	if res := compareSeeded(t, alt(1300, 1500), alt(1000, 1200), 20, 900); res.Verdict != "contradicted" {
		t.Fatalf("above: %+v", res)
	}
}

func TestCompareInconclusive(t *testing.T) {
	res := compareSeeded(t, alt(1000, 1200), alt(1000, 1200), 20, 1)
	if res.Lo > 0 || res.Hi < 0 || res.Verdict != "inconclusive" {
		t.Fatalf("got %+v", res)
	}
}

func TestCompareHiddenEstimate(t *testing.T) {
	if res := compareSeeded(t, alt(1300, 1500), alt(1000, 1200), 20, 0); res.State != "compared" || res.Verdict != "inconclusive" {
		t.Fatalf("got %+v", res)
	}
}

func TestCompareRepeatable(t *testing.T) {
	e := newEnv(t)
	wbSeed(t, e, 40, true, alt(1300, 1700), "m")
	wbSeed(t, e, 40, false, alt(1000, 1200), "m")
	a, _ := Compare(e.st, wide, 20, 300)
	b, _ := Compare(e.st, wide, 20, 300)
	if a.Lo != b.Lo || a.Hi != b.Hi || a.Lo == a.Hi {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestCompareMeasures(t *testing.T) {
	e := newEnv(t)
	memID := e.memory(t, "go test ./pkg", e.event(t, "x", 1, model.SessionStart))
	wbSeed(t, e, 2, true, alt(10, 10), memID)
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		for _, ev := range []model.Event{
			{TS: 1003, Session: fmt.Sprintf("true-0-%d", evSeq-1), Kind: model.PostTool, Tool: "shell", Payload: map[string]any{"command": "go test pkg"}, Digest: "x1"},
			{TS: 1004, Session: fmt.Sprintf("true-0-%d", evSeq-1), Kind: model.PostTool, Tool: "shell", Payload: map[string]any{"command": "ls", "error": "boom"}, Digest: "x2"},
			{TS: 1006, Session: fmt.Sprintf("true-0-%d", evSeq-1), Kind: model.Correction, Digest: "x3"},
		} {
			ev.Agent, ev.Project = "claude-code", "p"
			if _, err := tx.InsertEvent(&ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wbSeed(t, e, 2, false, alt(10, 10), memID)
	res, err := Compare(e.st, wide, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := res.Holdout
	if h.N != 2 || h.FailedPerPrompt != 0.5 || h.CorrectionsPerPrompt != 0.5 || h.WouldBeHitRate != 0.5 || res.Normal.WouldBeHitRate != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestWouldBeHit(t *testing.T) {
	cases := []struct {
		cmds      []string
		key, line string
		want      bool
	}{
		{[]string{"go test login"}, "k", "run go test login now", true},
		{[]string{"GO TEST login"}, "k", "go test login", true},
		{[]string{"ls", "go build"}, "k", "go test login", false},
		{[]string{"go test login extra"}, "k", "go test login", false},
		{[]string{"go login"}, "login", "go", true},
		{[]string{"a an of"}, "k", "a an of", false},
		{[]string{""}, "k", "go test", false},
		{nil, "k", "go test", false},
	}
	for _, c := range cases {
		if got := WouldBeHit(c.cmds, c.key, c.line); got != c.want {
			t.Errorf("WouldBeHit(%v, %q, %q) = %v, want %v", c.cmds, c.key, c.line, got, c.want)
		}
	}
}
