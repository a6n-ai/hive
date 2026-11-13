package recall

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

var now = time.UnixMilli(1_800_000_000_000)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "inscribe.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func put(t *testing.T, st *store.Store, m model.Memory) *model.Memory {
	t.Helper()
	if m.ScopeKind == "" {
		m.ScopeKind, m.Scope = model.ProjectScope, "/p"
	}
	if m.Status == "" {
		m.Status = model.Active
	}
	if m.Confidence == 0 {
		m.Confidence = 0.9
	}
	if m.LastSeen == 0 {
		m.FirstSeen, m.LastSeen = now.UnixMilli(), now.UnixMilli()
	}
	if m.Body == nil {
		m.Body = map[string]any{}
	}
	var out *model.Memory
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		var err error
		out, err = tx.UpsertMemory(&m, nil, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func cfg() config.RecallCfg { return config.Defaults().Recall }

func tokens(lines []Line) int {
	n := 0
	for _, l := range lines {
		n += estimateTokens(renderLine(l))
	}
	return n
}

func TestSessionStartRespectsBudget(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 50; i++ {
		put(t, st, model.Memory{Type: model.Command, Key: fmt.Sprint(i), Text: fmt.Sprintf("tests: run suite number %02d now", i)})
	}
	lines, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Mode: SessionStart}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || tokens(lines) > 400 {
		t.Fatalf("lines=%d tokens=%d, want >0 and <=400", len(lines), tokens(lines))
	}
	for i := 1; i < len(lines); i++ {
		if lines[i].Score > lines[i-1].Score {
			t.Fatal("lines not sorted by score")
		}
	}
}

func TestPromptRanksSimilarMemories(t *testing.T) {
	st := newStore(t)
	memories := []model.Memory{
		{Type: model.Fix, Key: "login-fix", Text: "login bug fixed in auth.ts"},
		{Type: model.Note, Key: "login-note", Text: "login failure in auth handler"},
		{Type: model.Command, Key: "test", Text: "tests: go test ./..."},
		{Type: model.Note, Key: "deploy", Text: "deploy with make"},
		{Type: model.Preference, Key: "tabs", Text: "use tabs not spaces", ScopeKind: model.Global, Scope: ""},
	}
	for _, m := range memories {
		put(t, st, m)
	}
	const prompt = "fix the login bug in auth"
	lines, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: prompt, Mode: Prompt}, now)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("prompt: %q", prompt)
	t.Logf("%-6s  %-44s  %s", "score", "memory", "injected")
	injected := map[string]bool{}
	for _, l := range lines {
		injected[l.Text] = true
	}
	groups := text.Groups(prompt)
	type row struct {
		text  string
		score float64
	}
	var ranked []row
	for _, m := range memories {
		m.Confidence = 0.9
		m.LastSeen = now.UnixMilli()
		if m.ScopeKind == "" {
			m.ScopeKind = model.ProjectScope
		}
		ranked = append(ranked, row{m.Text, promptScore(groups, m, now, 30, nil)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].text < ranked[j].text
	})
	for _, r := range ranked {
		mark := "no"
		if injected[r.text] {
			mark = "yes"
		}
		t.Logf("%6.3f  %-44s  %s", r.score, r.text, mark)
	}

	if len(lines) != 2 {
		t.Fatalf("injected %d lines, want the two login memories: %+v", len(lines), lines)
	}
	if !strings.Contains(lines[0].Text, "login bug fixed") || !strings.Contains(lines[1].Text, "login failure") {
		t.Fatalf("rank = %q then %q", lines[0].Text, lines[1].Text)
	}
	if lines[0].Score <= lines[1].Score {
		t.Fatalf("scores not descending: %+v", lines)
	}
}

func TestPromptInjectsTheMatchingMemory(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Command, Key: "test", Text: "tests: go test ./..."})
	put(t, st, model.Memory{Type: model.Note, Key: "deploy", Text: "deploy with make"})
	put(t, st, model.Memory{Type: model.Fix, Key: "login", Text: "login bug fixed in auth.ts"})
	put(t, st, model.Memory{Type: model.Preference, Key: "tabs", Text: "use tabs not spaces", ScopeKind: model.Global, Scope: ""})

	login, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "fix the login bug", Mode: Prompt}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(login) != 1 || !strings.Contains(login[0].Text, "login bug") {
		t.Fatalf("login prompt injected %+v, want only the login fix", login)
	}

	tests, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "run the tests", Mode: Prompt}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 1 || !strings.Contains(tests[0].Text, "go test") {
		t.Fatalf("test prompt injected %+v, want the go test command", tests)
	}

	tabs, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "please use tabs", Mode: Prompt}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 1 || !strings.Contains(tabs[0].Text, "tabs") {
		t.Fatalf("tabs prompt injected %+v, want the preference", tabs)
	}
}

func TestPromptSilentBelowThreshold(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Command, Key: "k", Text: "tests: go test ./..."})
	lines, _ := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "what time is it", Mode: Prompt}, now)
	if len(lines) != 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestPromptMaxThreeLines(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 10; i++ {
		put(t, st, model.Memory{Type: model.Note, Key: fmt.Sprint(i), Text: fmt.Sprintf("deploy step %d", i)})
	}
	lines, _ := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "how do we deploy", Mode: Prompt}, now)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
}

func TestCandidatesAndForgottenNeverReturned(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make", Status: model.Candidate})
	put(t, st, model.Memory{Type: model.Note, Key: "b", Text: "deploy with ship", Status: model.Forgotten})
	for _, q := range []Query{{Project: "/p", Mode: SessionStart}, {Project: "/p", Text: "deploy", Mode: Prompt}} {
		if lines, _ := Recall(context.Background(), st, nil, cfg(), q, now); len(lines) != 0 {
			t.Fatalf("mode %d returned %+v", q.Mode, lines)
		}
	}
}

func TestPostToolErrorBySignature(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Fix, Key: "sigS", Text: "TypeError … → edited auth.ts → tests green", Confidence: 0.5})
	put(t, st, model.Memory{Type: model.Fix, Key: "other", Text: "something else"})
	lines, _ := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", ErrorSignature: "sigS", Mode: PostToolError}, now)
	if len(lines) != 1 || !strings.Contains(lines[0].Text, "auth.ts") {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestRecallUpdatesLastUsed(t *testing.T) {
	st := newStore(t)
	m := put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make deploy"})
	if _, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt}, now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetMemory(m.ID)
	if got.LastUsed != now.UnixMilli() {
		t.Fatalf("last_used = %d, want %d", got.LastUsed, now.UnixMilli())
	}
}

func TestCJKTermFound(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "部署命令: make deploy"})
	lines, _ := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "部署", Mode: Prompt}, now)
	if len(lines) != 1 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestInjectFalseReturnsNothing(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make deploy"})
	c := cfg()
	c.Inject = false
	if lines, _ := Recall(context.Background(), st, nil, c, Query{Project: "/p", Mode: SessionStart}, now); len(lines) != 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestOldMemoriesRankLower(t *testing.T) {
	st := newStore(t)
	old := now.Add(-90 * 24 * time.Hour).UnixMilli()
	put(t, st, model.Memory{Type: model.Note, Key: "old", Text: "deploy old way", FirstSeen: old, LastSeen: old})
	put(t, st, model.Memory{Type: model.Note, Key: "new", Text: "deploy new way"})
	lines, _ := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Mode: SessionStart}, now)
	if len(lines) < 1 || !strings.Contains(lines[0].Text, "new way") {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestRender(t *testing.T) {
	got := Render([]Line{{Type: model.Command, Text: "tests: go test ./..."}, {Type: model.Note, Text: "x"}})
	if got != "[command] tests: go test ./...\n[note] x" {
		t.Fatalf("Render = %q", got)
	}
}

func TestRecallSkipsMarkUsedAfterLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "inscribe.db")
	st, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make deploy"})
	holder, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- holder.Write(ctx, func(*store.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	if err := st.Write(ctx, func(*store.Tx) error { return nil }); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("Write err = %v, want ErrLocked", err)
	}
	start := time.Now()
	lines, err := Recall(ctx, st, nil, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt, Session: "s1"}, now)
	elapsed := time.Since(start)
	close(release)
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil || len(lines) != 1 {
		t.Fatalf("lines = %+v, err = %v", lines, err)
	}
	if elapsed >= 40*time.Millisecond {
		t.Fatalf("Recall took %v after the lock was hit, want < 40ms", elapsed)
	}
	if st.DroppedOutcomes() != 1 {
		t.Fatalf("DroppedOutcomes = %d, want 1", st.DroppedOutcomes())
	}
}

func TestCJKUnspacedRecall(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "部署命令: make deploy"})
	if lines, _ := Recall(ctx, st, nil, cfg(), Query{Project: "/p", Text: "如何部署", Mode: Prompt}, now); len(lines) != 1 {
		t.Fatalf("部署 inside 如何部署: lines = %+v, want 1", lines)
	}
	st2 := newStore(t)
	put(t, st2, model.Memory{Type: model.Note, Key: "b", Text: "命令行工具"})
	if lines, _ := Recall(ctx, st2, nil, cfg(), Query{Project: "/p", Text: "命令", Mode: Prompt}, now); len(lines) != 1 {
		t.Fatalf("命令 inside 命令行工具: lines = %+v, want 1", lines)
	}
}

func TestMarkUsedWritesShownLine(t *testing.T) {
	st := newStore(t)
	m := put(t, st, model.Memory{Type: model.Note, Key: "k1", Text: "deploy with make deploy"})
	lines, err := Recall(context.Background(), st, nil, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt,
		Session: "s1", Agent: "claude-code", PromptDigest: "d1"}, now)
	if err != nil || len(lines) != 1 {
		t.Fatalf("lines = %+v, err = %v", lines, err)
	}
	if lines[0].Key != m.Key {
		t.Fatalf("Line.Key = %q, want %q", lines[0].Key, m.Key)
	}
	db, err := sql.Open("sqlite", st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var digest, key, shown, rule string
	if err := db.QueryRow(`SELECT prompt_digest, shown_key, shown_text, label_rule FROM score_outcomes WHERE memory_id = ?`, m.ID).
		Scan(&digest, &key, &shown, &rule); err != nil {
		t.Fatal(err)
	}
	if digest != "d1" || key != m.Key || shown != lines[0].Text || rule != "L1" {
		t.Fatalf("outcome = (%q, %q, %q, %q)", digest, key, shown, rule)
	}
}

func TestMarkUsedSwallowsLockHitMidWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "inscribe.db")
	st, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	holder, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- holder.Write(ctx, func(*store.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	hits := []hit{{Line: Line{MemoryID: "m1", Key: "k", Text: "t"}}}
	err = markUsed(ctx, st, hits, Query{Mode: Prompt, Session: "s1"}, now)
	close(release)
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("markUsed err = %v, want nil", err)
	}
	if !st.HitLock() || st.DroppedOutcomes() != 1 {
		t.Fatalf("HitLock=%v DroppedOutcomes=%d, want true and 1", st.HitLock(), st.DroppedOutcomes())
	}
}
