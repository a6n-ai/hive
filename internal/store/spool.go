package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/a6n-ai/hive/model"
)

// shortcut: uses syscall.Flock, so spooling is unix-only (macOS, Linux); add a
// Windows locking path if Windows becomes a tested platform.

func (s *Store) spoolDir() string { return filepath.Join(filepath.Dir(s.path), "spool") }

// Spool appends e to this process's spool file so a later writer can import
// it. Used when Write returns ErrLocked.
func (s *Store) Spool(e *model.Event) error {
	dir := s.spoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := filepath.Join(dir, fmt.Sprintf("%d.jsonl", os.Getpid()))
	for attempt := 0; attempt < 5; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			return err
		}
		// A drainer may have renamed the file between open and lock; if so the
		// fd no longer names `path` and the line must go to a fresh file.
		if sameFile(f, path) {
			_, werr := f.Write(line)
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			cerr := f.Close()
			if werr != nil {
				return werr
			}
			return cerr
		}
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return fmt.Errorf("hive: spool file %s kept moving", path)
}

func sameFile(f *os.File, path string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

// DrainSpool imports every spooled event inside this transaction and returns
// the number of spooled lines read. Spool files are deleted only after the
// transaction commits.
func (t *Tx) DrainSpool() (int, error) {
	n, _, err := t.drain()
	return n, err
}

// DrainSpoolEvents is DrainSpool returning the events actually inserted, so
// the caller can run extractors on them.
func (t *Tx) DrainSpoolEvents() ([]model.Event, error) {
	_, evs, err := t.drain()
	return evs, err
}

func (t *Tx) drain() (int, []model.Event, error) {
	var inserted []model.Event
	if t.spoolDir == "" {
		return 0, nil, nil
	}
	entries, err := os.ReadDir(t.spoolDir)
	if os.IsNotExist(err) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	total := 0
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".jsonl") && !strings.Contains(name, ".draining-") {
			continue
		}
		src := filepath.Join(t.spoolDir, name)
		claimed := src
		if !strings.Contains(name, ".draining-") {
			claimed = fmt.Sprintf("%s.draining-%d-%d", src, os.Getpid(), time.Now().UnixNano())
			if err := os.Rename(src, claimed); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return total, inserted, err
			}
		}
		n, evs, err := t.importSpoolFile(claimed)
		if err != nil {
			return total, inserted, err
		}
		total += n
		inserted = append(inserted, evs...)
		path := claimed
		t.AfterCommit(func() { _ = os.Remove(path) })
	}
	return total, inserted, nil
}

func (t *Tx) importSpoolFile(path string) (int, []model.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = f.Close() }()
	// Wait for any append that opened the file before it was renamed.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, nil, err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	n := 0
	var inserted []model.Event
	for sc.Scan() {
		var e model.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn line from a crashed writer; nothing to recover
		}
		e.ID = 0
		ok, err := t.InsertEvent(&e)
		if err != nil {
			return n, inserted, err
		}
		if ok {
			inserted = append(inserted, e)
		}
		n++
	}
	return n, inserted, sc.Err()
}
