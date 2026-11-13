package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrLocked means another router already holds the data directory.
var ErrLocked = errors.New("another router is already using this data directory")

// Lock is an exclusive advisory lock on a file. The OS releases it when the
// process exits, even on a crash, so there is never a stale lock to clean up.
type Lock struct{ f *os.File }

// AcquireLock takes a non-blocking exclusive flock on path.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock file %s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error { return l.f.Close() }
