package recall

import (
	"context"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

func TestPromptScoresStoredColumns(t *testing.T) {
	st := newStore(t)
	memories := []model.Memory{
		{Type: model.Command, Key: "npm run lint", Text: "check style before commit", Body: map[string]any{"class": "lint"}},
		{Type: model.Note, Key: "handler", Text: "handler lives here", ScopeKind: model.File, Scope: "/p/auth.ts"},
		{Type: model.Preference, Key: "eof", Text: "prefers an empty line at the end of a file", Body: map[string]any{"agent": "codex"}},
		{Type: model.Fix, Key: "ship-binary", Text: "ship the binary with make"},
		{Type: model.Note, Key: "deploy", Text: "deploy with make"},
	}
	for _, m := range memories {
		put(t, st, m)
	}

	cases := []struct {
		prompt string
		agent  string
		want   string
		skip   string
	}{
		{prompt: "npm run lint", want: "check style before commit", skip: "deploy with make"},
		{prompt: "auth.ts", want: "handler lives here", skip: "deploy with make"},
		{prompt: "codex", want: "empty line", skip: "deploy with make"},
		{prompt: "fix", skip: "ship the binary"},
	}
	w := DefaultWeights()
	t.Logf("%-16s %-6s %-5s %-5s %-5s %-5s %-5s %-5s %-7s %s", "prompt", "score", "text", "key", "body", "type", "scope", "agent", "command", "memory")
	for _, m := range memories {
		filled := m
		if filled.ScopeKind == "" {
			filled.ScopeKind = model.ProjectScope
		}
		filled.Confidence = 0.9
		filled.LastSeen = now.UnixMilli()
		for _, c := range cases {
			groups := text.Groups(c.prompt)
			facets := chunkPrompt(c.prompt, c.agent)
			f := fieldFeatures(groups, facets, filled)
			score := blend(f, w) * filled.Confidence * scopeWeight(filled, nil)
			t.Logf("%-16s %6.3f %5.2f %5.2f %5.2f %5.2f %5.2f %5.2f %7.2f %s",
				c.prompt, score, f.Text, f.Key, f.Body, f.Type, f.Scope, f.Agent, f.Command, filled.Text)
		}
	}

	for _, c := range cases {
		lines, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: c.prompt, Agent: c.agent, Mode: Prompt}, now)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range lines {
			got = append(got, l.Text)
			if c.skip != "" && strings.Contains(l.Text, c.skip) {
				t.Fatalf("prompt %q injected %q", c.prompt, l.Text)
			}
		}
		t.Logf("prompt %q injected: %s", c.prompt, strings.Join(got, " | "))
		if c.want != "" {
			found := false
			for _, l := range lines {
				if strings.Contains(l.Text, c.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("prompt %q injected %v, want %q", c.prompt, got, c.want)
			}
		} else if len(lines) != 0 {
			t.Fatalf("prompt %q injected %v, want nothing", c.prompt, got)
		}
	}
}

func TestRecallRecordsTheInjection(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make deploy"})
	if _, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt, Session: "s1", Agent: "codex"}, now); err != nil {
		t.Fatal(err)
	}
	n, err := st.ScoreOutcomeCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("outcomes = %d, want 1", n)
	}
	if _, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt}, now); err != nil {
		t.Fatal(err)
	}
	n, err = st.ScoreOutcomeCount()
	if err != nil || n != 1 {
		t.Fatalf("outcomes = %d, %v; a prompt without a session must not record another", n, err)
	}
}

func TestTrainLearnsFromWhatTheAgentDid(t *testing.T) {
	st := newStore(t)
	start := DefaultWeights()
	start.Key = 0.4
	if err := SaveWeights(context.Background(), st, start); err != nil {
		t.Fatal(err)
	}
	worked := put(t, st, model.Memory{Type: model.Command, Key: "npm run lint", Text: "check style before commit"})
	failed := put(t, st, model.Memory{Type: model.Command, Key: "go test ./...", Text: "check style before commit"})
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		good, err := (Features{Key: 0.5}).marshal()
		if err != nil {
			return err
		}
		bad, err := (Features{Command: 1}).marshal()
		if err != nil {
			return err
		}
		if err := tx.InsertScoreOutcome(worked.ID, "good", "codex", 100, good, 0.2); err != nil {
			return err
		}
		if err := tx.InsertScoreOutcome(failed.ID, "bad", "codex", 100, bad, 0.9); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(&model.Event{TS: 200, Agent: "codex", Session: "good", Project: "/p", Kind: model.PostTool, Digest: "good-tool",
			Payload: map[string]any{"command": "npm run lint", "exit_code": 0}}); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(&model.Event{TS: 200, Agent: "codex", Session: "bad", Project: "/p", Kind: model.PostTool, Digest: "bad-tool",
			Payload: map[string]any{"command": "go test ./...", "exit_code": 1}}); err != nil {
			return err
		}
		if err := tx.TouchSession(&model.Event{TS: 300, Agent: "codex", Session: "good", Project: "/p", Kind: model.SessionEnd}); err != nil {
			return err
		}
		return tx.TouchSession(&model.Event{TS: 300, Agent: "codex", Session: "bad", Project: "/p", Kind: model.SessionEnd})
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("labeled=%d hits=%d misses=%d quiet=%d", res.Labeled, res.Hits, res.Misses, res.Quiet)
	t.Logf("key %.3f -> %.3f, command %.3f -> %.3f", start.Key, res.Weights.Key, start.Command, res.Weights.Command)
	if res.Hits != 1 || res.Misses != 1 {
		t.Fatalf("result = %+v, want one hit and one miss", res)
	}
	if res.Weights.Key <= start.Key {
		t.Fatalf("key weight did not rise: %v", res.Weights.Key)
	}
	if res.Weights.Command >= start.Command {
		t.Fatalf("command weight did not fall: %v", res.Weights.Command)
	}
	again, err := Train(context.Background(), st, now)
	if err != nil {
		t.Fatal(err)
	}
	if again.Labeled != 0 {
		t.Fatalf("second pass labeled %d, want 0", again.Labeled)
	}
}
