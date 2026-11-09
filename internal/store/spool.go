package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/a6n-ai/hive/model"
)

// openSpool opens a claimed spool file; a seam so tests can simulate a file that vanishes.
var openSpool = os.Open

// claimTTL is how long a .draining- claim belongs to the process that made it.
// A claim older than this was left by a crashed drainer and may be adopted.
const claimTTL = 60 * time.Second

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
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // G304: spool paths are under the user's HIVE_HOME
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
		if strings.Contains(name, ".draining-") {
			if info, err := ent.Info(); err == nil && time.Since(info.ModTime()) < claimTTL {
				continue
			}
		} else {
			claimed = fmt.Sprintf("%s.draining-%d-%d", src, os.Getpid(), time.Now().UnixNano())
			if err := os.Rename(src, claimed); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return total, inserted, err
			}
			_ = os.Chtimes(claimed, time.Now(), time.Now())
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

// quarantine moves an unreadable spool file to spool/bad so that it cannot fail
// every later write. The file is kept for inspection.
func (t *Tx) quarantine(path string) error {
	bad := filepath.Join(t.spoolDir, "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(bad, filepath.Base(path))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (t *Tx) importSpoolFile(path string) (int, []model.Event, error) {
	f, err := openSpool(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, nil, nil
	case err != nil:
		return 0, nil, t.quarantine(path)
	}
	defer func() { _ = f.Close() }()
	// Wait for any append that opened the file before it was renamed.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, nil, err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	n := 0
	var inserted []model.Event
	err = forEachSpoolLine(bufio.NewReader(f), func(line []byte) error {
		var e model.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil // a torn line from a crashed writer; nothing to recover
		}
		e.ID = 0
		ok, err := t.InsertEvent(&e)
		if err != nil {
			return err
		}
		if ok {
			inserted = append(inserted, e)
		}
		n++
		return nil
	})
	return n, inserted, err
}

// maxSpoolLine bounds one spooled event. A longer line is skipped, so one
// oversized spool line cannot fail every drain.
const maxSpoolLine = 16 << 20

// forEachSpoolLine calls fn for each complete line of r, skipping lines longer
// than maxSpoolLine. Only read errors and errors from fn are returned.
func forEachSpoolLine(r *bufio.Reader, fn func([]byte) error) error {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > maxSpoolLine {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if !tooLong && len(line) > 0 {
				return fn(line)
			}
			return nil
		case err != nil:
			return err
		}
		if !tooLong {
			if err := fn(line); err != nil {
				return err
			}
		}
		line, tooLong = line[:0], false
	}
}
