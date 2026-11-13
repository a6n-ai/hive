package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func TestScoreShowOutcomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INSCRIBE_HOME", home)
	st, err := store.Open(filepath.Join(home, "inscribe.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	in := store.OutcomeInput{MemoryID: "m", Session: "s1", Agent: "codex", TS: 100, Features: "{}", Score: 0.5,
		PromptDigest: "abcdef0123456789", ShownKey: "body", ShownText: "t"}
	label := 1
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.TouchSession(&model.Event{TS: 900, Agent: "codex", Session: "s1", Project: "/p", Kind: model.SessionEnd}); err != nil {
			return err
		}
		if err := tx.InsertScoreOutcome(in); err != nil {
			return err
		}
		if _, err := tx.ResolveScoreOutcome(1, &label, "full_command", 1, 200); err != nil {
			return err
		}
		if err := tx.InsertScoreOutcome(in); err != nil {
			return err
		}
		_, err := tx.ResolveScoreOutcome(2, nil, "no_lift", 0, 200)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := scoreCmd([]string{"show", "--outcomes"}, &out); err != nil {
		t.Fatal(err)
	}
	want := "abcdef01\nfull_command 1 1.000 body\nno_lift - 0.000 body\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output = %q, want it to contain %q", out.String(), want)
	}
}
