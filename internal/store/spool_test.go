package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/a6n-ai/hive/model"
)

// holdLock keeps a write transaction open on st until release is closed.
func holdLock(t *testing.T, st *Store) (release func(), done <-chan error) {
	t.Helper()
	started := make(chan struct{})
	rel := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- st.Write(context.Background(), func(tx *Tx) error {
			if _, err := tx.InsertEvent(event("holder-" + fmt.Sprint(time.Now().UnixNano()))); err != nil {
				return err
			}
			close(started)
			<-rel
			return nil
		})
	}()
	<-started
	return func() { close(rel) }, errc
}

// captureOrSpool mimics what a hook does.
func captureOrSpool(st *Store, e *model.Event) error {
	err := st.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.DrainSpool(); err != nil {
			return err
		}
		_, err := tx.InsertEvent(e)
		return err
	})
	if errors.Is(err, ErrLocked) {
		return st.Spool(e)
	}
	return err
}

func spoolLines(t *testing.T, dir string) int {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "spool"))
	n := 0
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, "spool", e.Name()))
		for _, c := range b {
			if c == '\n' {
				n++
			}
		}
	}
	return n
}

func TestLockedWriteSpools(t *testing.T) {
	holder, path := openTemp(t)
	other, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	release, done := holdLock(t, holder)
	start := time.Now()
	err = other.Write(context.Background(), func(tx *Tx) error { return nil })
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Write err = %v, want ErrLocked", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("locked write took %v, want < 200ms", d)
	}
	if err := other.Spool(event("s1")); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := spoolLines(t, filepath.Dir(path)); n != 1 {
		t.Fatalf("spool lines = %d, want 1", n)
	}
}

func TestDrainImportsOnceAndDeletes(t *testing.T) {
	st, path := openTemp(t)
	for _, d := range []string{"a", "b", "a"} {
		if err := st.Spool(event(d)); err != nil {
			t.Fatal(err)
		}
	}
	var drained int
	if err := st.Write(context.Background(), func(tx *Tx) error {
		var err error
		drained, err = tx.DrainSpool()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if drained != 3 {
		t.Fatalf("drained = %d, want 3", drained)
	}
	var n int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
	if left := spoolLines(t, filepath.Dir(path)); left != 0 {
		t.Fatalf("spool lines left = %d, want 0", left)
	}
}

func TestConcurrentHooksNoLoss(t *testing.T) {
	holder, path := openTemp(t)
	release, done := holdLock(t, holder)
	go func() { time.Sleep(300 * time.Millisecond); release() }()

	var wg sync.WaitGroup
	errs := make(chan error, 150)
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			st, err := Open(path, false)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = st.Close() }()
			for i := 0; i < 50; i++ {
				if err := captureOrSpool(st, event(fmt.Sprintf("g%d-%d", g, i))); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := holder.Write(context.Background(), func(tx *Tx) error { _, err := tx.DrainSpool(); return err }); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = holder.db.QueryRow("SELECT COUNT(*) FROM events WHERE digest LIKE 'g%'").Scan(&n)
	if n != 150 {
		t.Fatalf("captured events = %d, want 150", n)
	}
}
