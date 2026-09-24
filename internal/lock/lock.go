// Package lock provides non-blocking, process-exclusive file locks.
//
// The lock is held by the operating system on an open file handle, so it is
// released automatically when the process exits or is killed — there is no
// stale-lock or PID bookkeeping to get wrong. The lock file itself is left in
// place; its existence means nothing, only the OS lock on it does.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// ErrHeld is returned by TryAcquire when another process holds the lock.
var ErrHeld = errors.New("lock is held by another process")

// Lock is an acquired file lock. Release it exactly once.
type Lock struct {
	f    *os.File
	path string
}

// TryAcquire takes an exclusive lock on path without waiting. It returns
// ErrHeld when another process (or another handle in this process) holds it.
func TryAcquire(path string) (*Lock, error) {
	// Lock files hold nothing secret, so the directory and files are readable
	// by all: a lock file created once under `sudo ffm` must not make the same
	// lock unopenable for the real user afterwards. A lock only needs a
	// read-only handle, which is the fallback when the file is not writable.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	writable := true
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if errors.Is(err, os.ErrPermission) {
		writable = false
		f, err = os.Open(path)
	}
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return nil, err
	}
	if writable {
		// The PID is informational only, for a human wondering who holds it.
		_ = f.Truncate(0)
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f, path: path}, nil
}

// Release drops the lock. Safe to call on a nil Lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// Path returns the lock file's path.
func (l *Lock) Path() string { return l.path }
