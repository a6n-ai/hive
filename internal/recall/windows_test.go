package recall

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

func TestLongPromptQueriesEachStretch(t *testing.T) {
	st := newStore(t)
	login := model.Memory{Type: model.Fix, Key: "login-fix", Text: "login bug fixed in auth.ts"}
	deploy := model.Memory{Type: model.Note, Key: "deploy", Text: "deploy with make"}
	tabs := model.Memory{Type: model.Preference, Key: "tabs", Text: "use tabs not spaces", ScopeKind: model.Global, Scope: ""}
	for _, m := range []model.Memory{login, deploy, tabs} {
		put(t, st, m)
	}

	prompt := "fix the login bug in auth " + strings.Repeat("alpha bravo charlie delta ", 3) + "deploy with make"
	groups := text.Groups(prompt)
	windows := promptWindows(groups)
	if len(groups) <= shortPromptGroups || len(windows) < 2 {
		t.Fatalf("groups=%d windows=%d, want a long prompt split into windows", len(groups), len(windows))
	}

	loginMem := login
	loginMem.Confidence, loginMem.ScopeKind, loginMem.LastSeen = 0.9, model.ProjectScope, now.UnixMilli()
	full := promptScore(groups, loginMem, now, 30, nil)
	if full >= 0.35 {
		t.Fatalf("score against the whole prompt = %.3f, want it under 0.35 so the window split is what saves the match", full)
	}

	q := Query{Project: "/p", Text: prompt, Mode: Prompt}
	lines, err := Recall(context.Background(), st, nil, cfg(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("groups=%d windows=%d full-prompt score=%.3f", len(groups), len(windows), full)
	t.Logf("%-6s  %s", "score", "memory")
	for _, l := range lines {
		t.Logf("%6.3f  %s", l.Score, l.Text)
	}
	got := map[string]bool{}
	for _, l := range lines {
		got[l.Text] = true
		if strings.Contains(l.Text, "tabs") {
			t.Fatalf("injected %q", l.Text)
		}
	}
	if !got[login.Text] || !got[deploy.Text] {
		t.Fatalf("injected %v, want the login fix and the deploy note", lines)
	}

	src := IndexSource{Idx: indexOf(t, st), Store: st}
	indexed, err := Recall(context.Background(), st, src, cfg(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(linesOf(lines), linesOf(indexed)) {
		t.Fatalf("store %v\nindex %v", linesOf(lines), linesOf(indexed))
	}
}

func TestShortPromptStaysOneWindow(t *testing.T) {
	groups := text.Groups("fix the login bug in auth")
	if len(promptWindows(groups)) != 1 {
		t.Fatalf("windows = %d, want 1", len(promptWindows(groups)))
	}
}
