package extract_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/capture"
	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/extract"
	"github.com/a6n-ai/inscribe/internal/scrub"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

type harness struct {
	t  *testing.T
	p  *capture.Pipeline
	ts int64
}

func newHarness(t *testing.T, cfg config.Config, extractors ...extract.Extractor) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "inscribe.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sc, _ := scrub.New(nil)
	reg := &extract.Registry{}
	for _, x := range extractors {
		reg.Register(x)
	}
	return &harness{t: t, p: &capture.Pipeline{Store: st, Scrub: sc, Extract: reg, Cfg: cfg}}
}

func (h *harness) emit(session, project string, kind model.EventKind, tool string, payload map[string]any, prompt string) {
	h.t.Helper()
	h.ts++
	if err := h.p.Capture(context.Background(), model.Event{TS: h.ts, Agent: "claude-code", Session: session, Project: project,
		Kind: kind, Tool: tool, Payload: payload, PromptText: prompt}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) shell(session, cmd string, exit int, errText string) {
	p := map[string]any{"command": cmd, "exit_code": exit}
	if errText != "" {
		p["error"] = errText
	}
	h.emit(session, "/p", model.PostTool, "shell", p, "")
}

func (h *harness) edit(session, path string) {
	h.emit(session, "/p", model.PostTool, "edit", map[string]any{"path": path}, "")
}

func (h *harness) list(types ...model.MemoryType) []model.Memory {
	h.t.Helper()
	ms, err := h.p.Store.ListMemories(store.ListFilter{Types: types, Statuses: []model.Status{model.Active}})
	if err != nil {
		h.t.Fatal(err)
	}
	return ms
}

func TestSignatureIgnoresLineNumbersAndPaths(t *testing.T) {
	a := extract.Signature("TypeError: cannot read properties of undefined (reading 'map') at /a/b.ts:12:5")
	b := extract.Signature("TypeError: cannot read properties of undefined (reading 'map') at /c/d.ts:98:1")
	c := extract.Signature("ReferenceError: x is not defined at /a/b.ts:3:1")
	if a != b {
		t.Fatalf("same error at other lines differs: %s vs %s", a, b)
	}
	if a == c {
		t.Fatal("different errors share a signature")
	}
}

func TestProjectFromGoMod(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewProject())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.emit("s1", dir, model.SessionStart, "", nil, "")
	ms := h.list(model.Project)
	if len(ms) != 1 || ms[0].Key != "language" || ms[0].Text != "Go module" || ms[0].Scope != dir {
		t.Fatalf("project memories = %+v", ms)
	}
}

func TestCommandClassifiedAndCounted(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewCommand())
	for i := 0; i < 3; i++ {
		h.shell("s1", "go test ./...", 0, "")
	}
	h.shell("s1", "go  test   ./...", 1, "FAIL")
	h.shell("s1", "ls -la", 0, "")
	ms := h.list(model.Command)
	if len(ms) != 1 {
		t.Fatalf("command memories = %+v", ms)
	}
	m := ms[0]
	if m.Key != "go test" || m.Body["class"] != "test" || m.Count != 3 || m.Text != "tests: go test ./..." {
		t.Fatalf("memory = %+v", m)
	}
}

func TestCommandVariantsShareOneMemory(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewCommand())
	h.shell("s1", "go test -count=1 ./internal/a", 0, "")
	h.shell("s1", "go vet ./internal/b && go test ./internal/b 2>&1 | tail", 0, "")
	h.shell("s1", "npm run dev", 0, "")
	h.shell("s1", "npm run worker", 0, "")
	keys := map[string]string{}
	for _, m := range h.list(model.Command) {
		keys[m.Key] = m.Text
	}
	want := map[string]string{
		"go test":        "tests: go vet ./internal/b && go test ./internal/b 2>&1 | tail",
		"npm run dev":    "run: npm run dev",
		"npm run worker": "run: npm run worker",
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v", keys)
	}
	for k, text := range want {
		if keys[k] != text {
			t.Errorf("key %q text = %q, want %q", k, keys[k], text)
		}
	}
}

func TestClassifyCommandReadsProgramsNotArguments(t *testing.T) {
	cases := map[string]string{
		"go test ./...":                      "test",
		"cd /p && go test ./internal/x":      "test",
		"CGO_ENABLED=0 go build ./cmd/x":     "build",
		"npx tsc --noEmit":                   "lint",
		"npm run lint":                       "lint",
		"./gradlew test":                     "test",
		"python -m pytest -q":                "test",
		"make":                               "build",
		"make dev":                           "run",
		"docker compose up -d":               "run",
		"git log --oneline && go vet ./... ": "lint",
		`sqlite3 db "delete x"; inscribe recall "go test"`: "",
		`git commit -m "fix: run the tests"`:               "",
		"rg -n test src":                                   "",
		`echo "go test" | tee out`:                         "",
		"ls -la":                                           "",
	}
	for cmd, want := range cases {
		if got := extract.ClassifyCommand(cmd); got != want {
			t.Errorf("ClassifyCommand(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestCommandSkipsHeredocAndLongCommands(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewCommand())
	h.shell("s1", "cat > /p/report.md <<'EOF'\nran the tests\nEOF", 0, "")
	h.shell("s1", "cat <<EOF > r.md ran tests EOF", 0, "")
	h.shell("s1", "go test\n./...", 0, "")
	h.shell("s1", "go test "+strings.Repeat("a", 193), 0, "")
	if ms := h.list(model.Command); len(ms) != 0 {
		t.Fatalf("command memories = %+v, want none", ms)
	}
	h.shell("s1", "go test ./internal/recall", 0, "")
	ms := h.list(model.Command)
	if len(ms) != 1 || ms[0].Text != "tests: go test ./internal/recall" {
		t.Fatalf("command memories = %+v", ms)
	}
}

func TestFixLinksErrorEditGreen(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewFix())
	errLine := "TypeError: cannot read properties of undefined (reading 'map') at /p/auth.ts:12:5"
	h.shell("s1", "go test ./...", 1, errLine+"\n    at next")
	h.edit("s1", "/p/auth.ts")
	h.shell("s1", "go test ./...", 0, "")
	ms := h.list(model.Fix)
	if len(ms) != 1 {
		t.Fatalf("fix memories = %+v", ms)
	}
	m := ms[0]
	if m.Key != extract.Signature(errLine) {
		t.Fatalf("key = %s", m.Key)
	}
	files, _ := m.Body["files"].([]any)
	if len(files) != 1 || files[0] != "auth.ts" {
		t.Fatalf("files = %v", m.Body["files"])
	}
	ev, err := h.p.Store.Evidence(m.ID)
	if err != nil || len(ev) != 3 {
		t.Fatalf("evidence = %d (%v), want 3", len(ev), err)
	}
}

func TestCochangeNeedsThreeTurns(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewCochange())
	turn := func() {
		h.emit("s1", "/p", model.Prompt, "", nil, "change both")
		h.edit("s1", "/p/b.go")
		h.edit("s1", "/p/a.go")
		h.emit("s1", "/p", model.TurnEnd, "", nil, "")
	}
	turn()
	turn()
	if ms := h.list(model.Cochange); len(ms) != 0 {
		t.Fatalf("after 2 turns: %+v", ms)
	}
	turn()
	ms := h.list(model.Cochange)
	if len(ms) != 1 || ms[0].Key != "a.go|b.go" {
		t.Fatalf("after 3 turns: %+v", ms)
	}
}

func TestNoteFromRemembered(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewNote())
	text := "deploys go through make deploy"
	h.emit("s1", "/p", model.Remembered, "", map[string]any{"text": text, "type": "note", "scope": "project"}, "")
	h.emit("s1", "/p", model.Prompt, "", nil, "just a prompt")
	ms := h.list(model.Note)
	sum := sha256.Sum256([]byte(text))
	if len(ms) != 1 || ms[0].Key != hex.EncodeToString(sum[:]) || ms[0].Confidence != 0.9 || ms[0].Text != text {
		t.Fatalf("notes = %+v", ms)
	}
}
