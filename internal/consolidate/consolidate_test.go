package consolidate_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/hive/agent/claudecode"
	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/consolidate"
	"github.com/a6n-ai/hive/internal/scrub"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

const session = "s1"

type fakeRunner struct {
	out     string
	err     error
	prompts []string
}

func (f *fakeRunner) Run(_ context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	return f.out, f.err
}

type env struct {
	t     *testing.T
	st    *store.Store
	home  string
	cfg   config.Config
	scrub func(string) string
	seq   int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "hive.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sc, err := scrub.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Consolidate.MinEvents = 1
	return &env{t: t, st: st, home: home, cfg: cfg, scrub: func(s string) string { out, _ := sc.Scrub(s); return out }}
}

// events inserts n prompts into session and returns their ids.
func (e *env) events(n int) []int64 {
	e.t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		e.seq++
		ev := model.Event{TS: time.Now().UnixMilli() + int64(e.seq), Agent: "claude-code", Session: session, Project: "/p",
			Kind: model.Prompt, PromptText: fmt.Sprintf("prompt %d about deploys", e.seq), Digest: fmt.Sprintf("ev-%d", e.seq)}
		err := e.st.Write(context.Background(), func(tx *store.Tx) error {
			if _, err := tx.InsertEvent(&ev); err != nil {
				return err
			}
			return tx.TouchSession(&ev)
		})
		if err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, ev.ID)
	}
	return ids
}

// memory inserts a fix memory with key, citing evidence, and returns its id.
func (e *env) memory(key string, evidence int64) string {
	e.t.Helper()
	var id string
	err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		m, err := tx.UpsertMemory(&model.Memory{Type: model.Fix, ScopeKind: model.ProjectScope, Scope: "/p", Key: key,
			Text: "fix " + key, Confidence: 0.5, Status: model.Active, FirstSeen: 1, LastSeen: 1}, nil, evidence)
		if err != nil {
			return err
		}
		id = m.ID
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) get(id string) *model.Memory {
	e.t.Helper()
	m, err := e.st.GetMemory(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) run(r consolidate.Runner, dry bool) consolidate.RunReport {
	e.t.Helper()
	rep, err := consolidate.Run(context.Background(), e.st, e.cfg, e.scrub, r, session, dry)
	if err != nil {
		e.t.Fatal(err)
	}
	return rep
}

// fixture reads testdata/ops/name, replacing each token with its value.
func fixture(t *testing.T, name string, repl ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ops", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer(repl...).Replace(string(b))
}

// abc seeds three events and memories A, B (evidence e1, e2) and C (evidence e3).
func abc(e *env) (ids []int64, a, b, c string) {
	ids = e.events(3)
	a = e.memory("a", ids[0])
	b = e.memory("b", ids[1])
	c = e.memory("c", ids[2])
	return ids, a, b, c
}

func TestValidOpsApplyWithAudit(t *testing.T) {
	e := newEnv(t)
	ids, a, b, c := abc(e)
	r := &fakeRunner{out: fixture(t, "valid.json", "MEM_A", a, "MEM_B", b, "MEM_C", c, "EV_3", strconv.FormatInt(ids[2], 10))}
	rep := e.run(r, false)
	if rep.Status != "ok" || len(rep.Valid) != 3 {
		t.Fatalf("status = %s valid = %d rejected = %v", rep.Status, len(rep.Valid), rep.Rejected)
	}
	if got := e.get(b); got.Status != model.Superseded || got.SupersededBy != a {
		t.Errorf("dropped memory status = %s superseded_by = %s", got.Status, got.SupersededBy)
	}
	if got := e.get(a); got.Count != 2 {
		t.Errorf("keep count = %d, want 2", got.Count)
	}
	if got := e.get(c); got.Status != model.Forgotten {
		t.Errorf("rejected memory status = %s", got.Status)
	}
	added, err := e.st.ListMemories(store.ListFilter{Key: "prefer-small-commits"})
	if err != nil || len(added) != 1 || added[0].Status != model.Active {
		t.Fatalf("added memory = %+v (%v)", added, err)
	}
	audit, err := e.st.AuditRows(rep.RunID)
	if err != nil || len(audit) != 4 {
		t.Errorf("audit rows = %d (%v), want 4", len(audit), err)
	}
	cursor, _ := e.st.SessionCursor(session)
	if cursor != ids[2] {
		t.Errorf("cursor = %d, want %d", cursor, ids[2])
	}
}

func TestHostileOpsDropped(t *testing.T) {
	e := newEnv(t)
	ids, a, b, c := abc(e)
	secret := "deploy with password=" + "hunter9two"
	out := fixture(t, "hostile.json", "MEM_A", a, "MEM_B", b, "MEM_C", c, "EV_1", strconv.FormatInt(ids[0], 10),
		"EV_OUT", "999999", "SECRET_TEXT", secret)
	ops, err := consolidate.ParseOps(out)
	if err != nil {
		t.Fatal(err)
	}
	valid, rejected := consolidate.Validate(ops, consolidate.Input{Events: inputEvents(ids), Memories: memories(e, a, b, c)}, e.scrub)
	if len(valid) != 2 || len(rejected) != 6 {
		t.Fatalf("valid = %d rejected = %d, want 2 and 6: %+v", len(valid), len(rejected), rejected)
	}
	for _, v := range valid {
		if strings.Contains(v.Text, "hunter9two") {
			t.Errorf("secret kept in valid op text %q", v.Text)
		}
	}
}

func TestTooManyOpsRejected(t *testing.T) {
	e := newEnv(t)
	ids, _, _, _ := abc(e)
	var ops []string
	for i := 0; i < 51; i++ {
		ops = append(ops, fmt.Sprintf(`{"op":"add","type":"decision","scope_kind":"global","scope":"","key":"k%d","text":"t","evidence":[%d]}`, i, ids[0]))
	}
	parsed, err := consolidate.ParseOps(`{"ops":[` + strings.Join(ops, ",") + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	valid, rejected := consolidate.Validate(parsed, consolidate.Input{Events: inputEvents(ids)}, e.scrub)
	if len(valid) != 50 || len(rejected) != 1 {
		t.Errorf("valid = %d rejected = %d, want 50 and 1", len(valid), len(rejected))
	}
}

func TestParseOpsFenced(t *testing.T) {
	for name, in := range map[string]string{
		"bare":   `{"ops":[{"op":"stale","id":"m_1"}]}`,
		"fenced": "```json\n{\"ops\":[{\"op\":\"stale\",\"id\":\"m_1\"}]}\n```",
	} {
		ops, err := consolidate.ParseOps(in)
		if err != nil || len(ops) != 1 || ops[0].Op != "stale" {
			t.Errorf("%s: ops = %+v, err = %v", name, ops, err)
		}
	}
	if _, err := consolidate.ParseOps("I would merge these two."); err == nil {
		t.Error("prose parsed as ops")
	}
}

func TestNonJSONRecordedFailed(t *testing.T) {
	e := newEnv(t)
	_, a, _, _ := abc(e)
	before := e.get(a).Count
	rep := e.run(&fakeRunner{out: "I would merge these two."}, false)
	if rep.Status != "failed" || len(rep.Valid) != 0 {
		t.Fatalf("status = %s valid = %d", rep.Status, len(rep.Valid))
	}
	if got := e.get(a); got.Count != before || got.Status != model.Active {
		t.Errorf("memory changed by a failed run: %+v", got)
	}
	if status, _ := e.st.RunStatus(rep.RunID); status != "failed" {
		t.Errorf("run row status = %q, want failed", status)
	}
	if cursor, _ := e.st.SessionCursor(session); cursor != 0 {
		t.Errorf("cursor moved to %d by a failed run", cursor)
	}
}

func TestUndoRestoresExactly(t *testing.T) {
	e := newEnv(t)
	ids, a, b, c := abc(e)
	before := map[string]*model.Memory{a: e.get(a), b: e.get(b), c: e.get(c)}
	rep := e.run(&fakeRunner{out: fixture(t, "valid.json", "MEM_A", a, "MEM_B", b, "MEM_C", c, "EV_3", strconv.FormatInt(ids[2], 10))}, false)
	undo, err := consolidate.Undo(context.Background(), e.st, rep.RunID)
	if err != nil || undo.Restored != 4 || len(undo.Skipped) != 0 {
		t.Fatalf("undo = %+v (%v), want 4 restored and none skipped", undo, err)
	}
	for id, want := range before {
		got := e.get(id)
		if got.Status != want.Status || got.Count != want.Count || got.SupersededBy != want.SupersededBy || got.Text != want.Text {
			t.Errorf("memory %s = %+v, want %+v", id, got, want)
		}
	}
	if left, _ := e.st.ListMemories(store.ListFilter{Key: "prefer-small-commits"}); len(left) != 0 {
		t.Error("undo left the added memory")
	}
	if ev, _ := e.st.Evidence(b); len(ev) != 1 || ev[0].ID != ids[1] {
		t.Errorf("evidence of B after undo = %+v, want event %d", ev, ids[1])
	}
}

func TestUndoRefusesChangedMemory(t *testing.T) {
	e := newEnv(t)
	ids, a, b, c := abc(e)
	rep := e.run(&fakeRunner{out: fixture(t, "valid.json", "MEM_A", a, "MEM_B", b, "MEM_C", c, "EV_3", strconv.FormatInt(ids[2], 10))}, false)
	if err := e.st.Write(context.Background(), func(tx *store.Tx) error { return tx.SetStatus(c, model.Stale) }); err != nil {
		t.Fatal(err)
	}
	undo, err := consolidate.Undo(context.Background(), e.st, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(undo.Skipped) != 1 || undo.Skipped[0] != c {
		t.Errorf("skipped = %v, want [%s]", undo.Skipped, c)
	}
	if got := e.get(c); got.Status != model.Stale {
		t.Errorf("changed memory status = %s, want stale (undo must not clobber it)", got.Status)
	}
}

func TestSecondRunCoversOnlyNewEvents(t *testing.T) {
	e := newEnv(t)
	first := e.events(3)
	e.memory("a", first[0])
	rep := e.run(&fakeRunner{out: `{"ops":[]}`}, false)
	if rep.Status != "ok" {
		t.Fatalf("first run status = %s", rep.Status)
	}
	second := e.events(2)
	in, err := consolidate.BuildInput(e.st, session, first[2], 8000, e.scrub)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Events) != 2 || in.Events[0].ID != second[0] || in.Events[1].ID != second[1] {
		t.Errorf("events after cursor = %+v, want ids %v", in.Events, second)
	}
}

func TestLockHeldSkips(t *testing.T) {
	e := newEnv(t)
	e.events(2)
	release, ok, err := consolidate.Lock(e.home)
	if err != nil || !ok {
		t.Fatalf("lock = %v, %v", ok, err)
	}
	defer release()
	r := &fakeRunner{out: `{"ops":[]}`}
	if rep := e.run(r, false); rep.Status != "locked" || len(r.prompts) != 0 {
		t.Errorf("status = %s prompts = %d, want locked and no model call", rep.Status, len(r.prompts))
	}
}

func TestBelowMinEventsSkips(t *testing.T) {
	e := newEnv(t)
	e.events(3)
	e.cfg.Consolidate.MinEvents = 20
	r := &fakeRunner{out: `{"ops":[]}`}
	if rep := e.run(r, false); rep.Status != "skipped" || len(r.prompts) != 0 {
		t.Errorf("status = %s prompts = %d, want skipped and no model call", rep.Status, len(r.prompts))
	}
}

func TestHeadlessRunnerFakeScript(t *testing.T) {
	spec := claudecode.New().Headless("haiku")
	spec.Command = []string{"./testdata/fake-agent.sh"}
	out, err := consolidate.Headless{Spec: spec, Timeout: 10 * time.Second}.Run(context.Background(), "prompt")
	if err != nil || out != `{"ops":[]}` {
		t.Errorf("out = %q, err = %v", out, err)
	}
}

func TestHeadlessOutputCap(t *testing.T) {
	spec := claudecode.New().Headless("haiku")
	spec.Command = []string{"sh", "-c", "head -c 1100000 /dev/zero"}
	_, err := consolidate.Headless{Spec: spec, Timeout: 10 * time.Second}.Run(context.Background(), "prompt")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want output-too-large", err)
	}
}

func inputEvents(ids []int64) []consolidate.InputEvent {
	var out []consolidate.InputEvent
	for _, id := range ids {
		out = append(out, consolidate.InputEvent{ID: id})
	}
	return out
}

func memories(e *env, ids ...string) []model.Memory {
	var out []model.Memory
	for _, id := range ids {
		out = append(out, *e.get(id))
	}
	return out
}
