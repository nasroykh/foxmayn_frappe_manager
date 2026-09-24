package manager

import (
	"errors"
	"fmt"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/lock"
)

// ErrBenchStopped is returned by Backup when SkipIfStopped is set and the
// bench is not running.
var ErrBenchStopped = errors.New("bench is not running")

// ErrBenchBusy is returned when another operation holds a bench's lock.
var ErrBenchBusy = errors.New("bench is busy")

// lockBench takes the exclusive lock for a bench, without waiting.
//
// It is deliberately NOT re-entrant. The dashboard runs every request and
// background job on one shared Service, so re-entrancy keyed on the Service
// would let a dashboard Delete walk straight past a Recreate job on the same
// bench. OS file locks are per open handle, so a second lockBench in the same
// process is refused exactly like one from another process. Code that already
// holds the lock calls the *Locked variants (backupLocked, deleteLocked).
func (s *Service) lockBench(name string) (release func(), err error) {
	l, err := lock.TryAcquire(config.BenchLockFile(name))
	if errors.Is(err, lock.ErrHeld) {
		return nil, fmt.Errorf("%w: another ffm operation is backing up, restoring, deleting or "+
			"recreating %q — try again when it finishes", ErrBenchBusy, name)
	}
	if err != nil {
		return nil, fmt.Errorf("lock bench %q: %w", name, err)
	}
	return func() { _ = l.Release() }, nil
}
