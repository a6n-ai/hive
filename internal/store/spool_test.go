package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/model"
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

func TestWriteRemembersLock(t *testing.T) {
	st, path := openTemp(t)
	fresh, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	holder, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	release, done := holdLock(t, holder)
	if st.HitLock() {
		t.Fatal("HitLock true before any lock")
	}
	if err := st.Write(context.Background(), func(*Tx) error { return nil }); !errors.Is(err, ErrLocked) {
		t.Fatalf("Write err = %v, want ErrLocked", err)
	}
	if !st.HitLock() {
		t.Fatal("HitLock false after ErrLocked")
	}
	if fresh.HitLock() {
		t.Fatal("fresh store reports HitLock")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func writeSpoolFile(t *testing.T, dir, name string, mtime time.Time, evs ...*model.Event) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, e := range evs {
		line, _ := json.Marshal(e)
		b = append(append(b, line...), '\n')
	}
	path := filepath.Join(dir, "spool", name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDrainToleratesClaimedFileVanishing(t *testing.T) {
	st, path := openTemp(t)
	claimed := writeSpoolFile(t, filepath.Dir(path), "1.jsonl.draining-x-1", time.Now().Add(-2*time.Hour), event("vanish"))
	saved := openSpool
	t.Cleanup(func() { openSpool = saved })
	openSpool = func(name string) (*os.File, error) {
		_ = os.Remove(name)
		return saved(name)
	}
	if err := st.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.DrainSpool(); err != nil {
			return err
		}
		_, err := tx.InsertEvent(event("own"))
		return err
	}); err != nil {
		t.Fatalf("Write after the claimed file vanished: %v", err)
	}
	if _, err := os.Stat(claimed); !os.IsNotExist(err) {
		t.Fatalf("claimed file still present: %v", err)
	}
	var n int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM events WHERE digest = 'own'").Scan(&n)
	if n != 1 {
		t.Fatalf("own event stored %d times, want 1", n)
	}
}

func TestDrainSkipsFreshClaim(t *testing.T) {
	st, path := openTemp(t)
	claimed := writeSpoolFile(t, filepath.Dir(path), "2.jsonl.draining-y-1", time.Now(), event("live"))
	if err := st.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.DrainSpool()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claimed); err != nil {
		t.Fatalf("fresh claim was taken over: %v", err)
	}
	var n int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM events WHERE digest = 'live'").Scan(&n)
	if n != 0 {
		t.Fatalf("fresh claim imported %d times, want 0", n)
	}
}

func TestUnreadableSpoolFileDoesNotWedge(t *testing.T) {
	st, path := openTemp(t)
	bad := writeSpoolFile(t, filepath.Dir(path), "3.jsonl", time.Now().Add(-2*time.Hour), event("unreadable"))
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.DrainSpool(); err != nil {
			return err
		}
		_, err := tx.InsertEvent(event("after"))
		return err
	}); err != nil {
		t.Fatalf("Write with an unreadable spool file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "spool", "bad")); err != nil {
		t.Fatalf("no spool/bad directory: %v", err)
	}
}

func TestConcurrentDrainersNoLoss(t *testing.T) {
	holder, path := openTemp(t)
	release, done := holdLock(t, holder)
	go func() { time.Sleep(300 * time.Millisecond); release() }()
	var wg sync.WaitGroup
	errs := make(chan error, 200)
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
			for i := 0; i < 14; i++ {
				if err := captureOrSpool(st, event(fmt.Sprintf("d%d-%d", g, i))); err != nil {
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
	_ = holder.db.QueryRow("SELECT COUNT(*) FROM events WHERE digest LIKE 'd%'").Scan(&n)
	if n != 42 {
		t.Fatalf("captured events = %d, want 42", n)
	}
}

func TestOversizedSpoolLineSkipped(t *testing.T) {
	st, path := openTemp(t)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Join(dir, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(event("kept"))
	huge := []byte(`{"digest":"too-big","pad":"` + strings.Repeat("x", 17<<20) + `"}` + "\n")
	spool := filepath.Join(dir, "spool", "9.jsonl")
	if err := os.WriteFile(spool, append(append(huge, line...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(spool, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), func(tx *Tx) error { _, err := tx.DrainSpool(); return err }); err != nil {
		t.Fatalf("drain with an oversized line: %v", err)
	}
	var n int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM events WHERE digest = 'kept'").Scan(&n)
	if n != 1 {
		t.Fatalf("event after the oversized line stored %d times, want 1", n)
	}
}
