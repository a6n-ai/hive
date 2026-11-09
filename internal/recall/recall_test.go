package recall

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

var now = time.UnixMilli(1_800_000_000_000)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hive.db"), false)
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
	lines, err := Recall(context.Background(), st, cfg(), Query{Project: "/p", Mode: SessionStart}, now)
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

func TestPromptSilentBelowThreshold(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Command, Key: "k", Text: "tests: go test ./..."})
	lines, _ := Recall(context.Background(), st, cfg(), Query{Project: "/p", Text: "what time is it", Mode: Prompt}, now)
	if len(lines) != 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestPromptMaxThreeLines(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 10; i++ {
		put(t, st, model.Memory{Type: model.Note, Key: fmt.Sprint(i), Text: fmt.Sprintf("deploy step %d", i)})
	}
	lines, _ := Recall(context.Background(), st, cfg(), Query{Project: "/p", Text: "how do we deploy", Mode: Prompt}, now)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
}

func TestCandidatesAndForgottenNeverReturned(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make", Status: model.Candidate})
	put(t, st, model.Memory{Type: model.Note, Key: "b", Text: "deploy with ship", Status: model.Forgotten})
	for _, q := range []Query{{Project: "/p", Mode: SessionStart}, {Project: "/p", Text: "deploy", Mode: Prompt}} {
		if lines, _ := Recall(context.Background(), st, cfg(), q, now); len(lines) != 0 {
			t.Fatalf("mode %d returned %+v", q.Mode, lines)
		}
	}
}

func TestPostToolErrorBySignature(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Fix, Key: "sigS", Text: "TypeError … → edited auth.ts → tests green", Confidence: 0.5})
	put(t, st, model.Memory{Type: model.Fix, Key: "other", Text: "something else"})
	lines, _ := Recall(context.Background(), st, cfg(), Query{Project: "/p", ErrorSignature: "sigS", Mode: PostToolError}, now)
	if len(lines) != 1 || !strings.Contains(lines[0].Text, "auth.ts") {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestRecallUpdatesLastUsed(t *testing.T) {
	st := newStore(t)
	m := put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make deploy"})
	if _, err := Recall(context.Background(), st, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt}, now); err != nil {
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
	lines, _ := Recall(context.Background(), st, cfg(), Query{Project: "/p", Text: "部署", Mode: Prompt}, now)
	if len(lines) != 1 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestInjectFalseReturnsNothing(t *testing.T) {
	st := newStore(t)
	put(t, st, model.Memory{Type: model.Note, Key: "a", Text: "deploy with make deploy"})
	c := cfg()
	c.Inject = false
	if lines, _ := Recall(context.Background(), st, c, Query{Project: "/p", Mode: SessionStart}, now); len(lines) != 0 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestOldMemoriesRankLower(t *testing.T) {
	st := newStore(t)
	old := now.Add(-90 * 24 * time.Hour).UnixMilli()
	put(t, st, model.Memory{Type: model.Note, Key: "old", Text: "deploy old way", FirstSeen: old, LastSeen: old})
	put(t, st, model.Memory{Type: model.Note, Key: "new", Text: "deploy new way"})
	lines, _ := Recall(context.Background(), st, cfg(), Query{Project: "/p", Mode: SessionStart}, now)
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
	path := filepath.Join(t.TempDir(), "hive.db")
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
	lines, err := Recall(ctx, st, cfg(), Query{Project: "/p", Text: "deploy", Mode: Prompt}, now)
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
}
