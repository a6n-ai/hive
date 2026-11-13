package recall

import (
	"context"
	"strings"
	"testing"

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
	seedShown(t, st, "s1", "p1",
		shownLine{key: "npm test", text: "run the recall tests", f: Features{Key: 0.5}},
		shownLine{key: "deploy", text: "deploy with make", f: Features{Command: 1}})
	seedSession(t, st, "s1", promptEvent("s1", "p1"), commandEvent("s1", "c1", "npm test ./internal/recall", 0))

	res, err := Train(context.Background(), st, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reasons=%v open=%d refit=%v weights=%+v", res.Reasons, res.Open, res.Refit, res.Weights)
	if len(res.Reasons) != 2 || res.Reasons["full_command"] != 1 || res.Reasons["sibling"] != 1 {
		t.Fatalf("reasons = %v, want full_command=1 and sibling=1", res.Reasons)
	}
	if !res.Refit || res.Weights.Text != 1 || res.Weights.Hits != 1 || res.Weights.Misses != 0 {
		t.Fatalf("weights = %+v, want a refit with one hit and no miss", res.Weights)
	}
}
