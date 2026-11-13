package inscribe

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func TestInscribeRememberWhyForget(t *testing.T) {
	ctx := context.Background()
	h, err := Open(Options{Home: t.TempDir(), Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()

	id, err := h.Remember(ctx, "deploys go through make deploy", model.Note, model.ProjectScope, "/p")
	if err != nil || id == "" {
		t.Fatalf("Remember: id=%q err=%v", id, err)
	}
	ms, _ := h.List(ctx, store.ListFilter{Types: []model.MemoryType{model.Note}})
	if len(ms) != 1 || ms[0].ID != id {
		t.Fatalf("List = %+v", ms)
	}
	ev, err := h.Why(ctx, id)
	if err != nil || len(ev) != 1 || ev[0].Kind != model.Remembered {
		t.Fatalf("Why = %+v err=%v", ev, err)
	}
	q := recall.Query{Project: "/p", Text: "how do we deploy", Mode: recall.Prompt}
	if lines, _ := h.Recall(ctx, q); len(lines) != 1 {
		t.Fatalf("Recall before forget = %+v", lines)
	}
	if err := h.Forget(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if lines, _ := h.Recall(ctx, q); len(lines) != 0 {
		t.Fatalf("Recall after forget = %+v", lines)
	}
	if err := h.Forget(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if ms, _ := h.List(ctx, store.ListFilter{}); len(ms) != 0 {
		t.Fatalf("purged memory still listed: %+v", ms)
	}
}

func TestQueryEvents(t *testing.T) {
	ctx := context.Background()
	h, err := Open(Options{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	for i, k := range []model.EventKind{model.Prompt, model.TurnEnd, model.Prompt} {
		if err := h.Capture(ctx, Event{TS: int64(i + 1), Agent: "a", Session: "s", Project: "/p", Kind: k, PromptText: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := h.QueryEvents(ctx, EventFilter{Session: "s", Kinds: []model.EventKind{model.Prompt}})
	if err != nil || len(evs) != 2 {
		t.Fatalf("QueryEvents = %d (%v), want 2", len(evs), err)
	}
}

func TestBadProjectConfigIgnored(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	file := filepath.Join(proj, ".inscribe.toml")
	for _, body := range []string{"this is [not toml", "[scrub]\npatterns = [\"(\"]\n"} {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		h, err := Open(Options{Home: home, Project: proj})
		if err != nil {
			t.Fatalf("Open with project config %q: %v", body, err)
		}
		_ = h.Close()
	}
	logged, err := os.ReadFile(filepath.Join(home, "inscribe.log"))
	if err != nil || !strings.Contains(string(logged), file) {
		t.Fatalf("inscribe.log does not warn about %s: %q (%v)", file, logged, err)
	}
}

func TestPrepareTwiceIsStable(t *testing.T) {
	h, err := Open(Options{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	e := Event{Agent: "a", Session: "s", Project: "/p", Kind: model.Prompt,
		PromptText: "export ANTHROPIC_API_KEY=sk-ant-api03-" + strings.Repeat("Q", 30) + ` {"password": "hunter2zz9"}`}
	h.Prepare(&e)
	first, redactions := e, h.pipeline.Redactions.Load()
	h.Prepare(&e)
	if e.Digest == "" || e.TS == 0 {
		t.Fatalf("Prepare left Digest=%q TS=%d", e.Digest, e.TS)
	}
	if e.Digest != first.Digest || e.TS != first.TS || e.PromptText != first.PromptText || !reflect.DeepEqual(e.Payload, first.Payload) {
		t.Fatalf("second Prepare changed the event: %+v, want %+v", e, first)
	}
	if got := h.pipeline.Redactions.Load(); got != redactions {
		t.Fatalf("second Prepare counted %d more redactions", got-redactions)
	}
	if strings.Contains(e.PromptText, "sk-ant-") {
		t.Fatalf("PromptText not scrubbed: %q", e.PromptText)
	}
}
