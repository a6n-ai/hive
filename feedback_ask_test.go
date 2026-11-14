package inscribe

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func TestMaybeAskIDs(t *testing.T) {
	ctx := context.Background()
	h, err := Open(Options{Home: t.TempDir(), Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	var mem []string
	for _, text := range []string{"alpha note", "beta note", "gamma note"} {
		id, err := h.Remember(ctx, text, model.Note, model.ProjectScope, "/p")
		if err != nil {
			t.Fatal(err)
		}
		mem = append(mem, id)
	}
	if err := h.Forget(ctx, mem[2], false); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := h.MarkMCPServed(ctx, now); err != nil {
		t.Fatal(err)
	}
	err = h.store.Write(ctx, func(tx *store.Tx) error {
		for i, id := range []string{mem[0], mem[0], mem[1], mem[2], mem[2]} {
			if err := tx.InsertScoreOutcome(store.OutcomeInput{MemoryID: id, Session: "s", Agent: "claude-code", TS: now.UnixMilli() + int64(i), PromptDigest: "d"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	askID, _, ids, n, err := h.MaybeAsk(ctx, "s", true, now)
	if err != nil || askID == "" {
		t.Fatalf("MaybeAsk = %q, %v; want an ask", askID, err)
	}
	slices.Sort(ids)
	want := []string{mem[0], mem[1]}
	slices.Sort(want)
	if !slices.Equal(ids, want) || n != 2 {
		t.Fatalf("ids = %v, n = %d; want %v", ids, n, want)
	}
}
