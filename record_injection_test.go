package inscribe

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
)

func TestRecordInjectionLocked(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h, err := Open(Options{Home: home, Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	holder, err := store.Open(filepath.Join(home, "inscribe.db"), false)
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
	err = h.RecordInjection(ctx, store.InjectionInput{TS: 1, Session: "s", Project: "/p", Hook: "UserPromptSubmit", LineCount: 1, EstTokens: 5})
	close(release)
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("RecordInjection err = %v, want nil", err)
	}
	if h.DroppedInjections() != 1 {
		t.Fatalf("DroppedInjections = %d, want 1", h.DroppedInjections())
	}
}
