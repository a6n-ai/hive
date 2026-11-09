package consolidate

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes an exclusive, non-blocking lock on home/consolidate.lock. ok is
// false, with no error, when another process holds it.
func Lock(home string) (release func(), ok bool, err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(home, "consolidate.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: the lock path is under HIVE_HOME
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: a file descriptor fits in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // G115: a file descriptor fits in int
		_ = f.Close()
	}, true, nil
}
