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

// ErrBenchBusy is returned when another ffm process holds a bench's lock.
var ErrBenchBusy = errors.New("bench is busy")

// heldBenchLock is a bench lock this Service holds, with a nesting count.
type heldBenchLock struct {
	l     *lock.Lock
	depth int
}

// lockBench takes the cross-process lock for a bench, without waiting.
//
// It is re-entrant within one Service: Restore holds the target's lock and,
// when it fails, calls Delete on that same bench, which takes the lock again.
// OS file locks are per open handle, so without the count that nested call
// would find the lock "held by someone else" — itself.
func (s *Service) lockBench(name string) (release func(), err error) {
	s.benchLocksMu.Lock()
	defer s.benchLocksMu.Unlock()
	if s.benchLocks == nil {
		s.benchLocks = map[string]*heldBenchLock{}
	}
	if h, ok := s.benchLocks[name]; ok {
		h.depth++
		return func() { s.unlockBench(name) }, nil
	}
	l, err := lock.TryAcquire(config.BenchLockFile(name))
	if errors.Is(err, lock.ErrHeld) {
		return nil, fmt.Errorf("%w: another ffm process is backing up, restoring, deleting or "+
			"recreating %q — try again when it finishes", ErrBenchBusy, name)
	}
	if err != nil {
		return nil, fmt.Errorf("lock bench %q: %w", name, err)
	}
	s.benchLocks[name] = &heldBenchLock{l: l, depth: 1}
	return func() { s.unlockBench(name) }, nil
}

func (s *Service) unlockBench(name string) {
	s.benchLocksMu.Lock()
	defer s.benchLocksMu.Unlock()
	h, ok := s.benchLocks[name]
	if !ok {
		return
	}
	h.depth--
	if h.depth > 0 {
		return
	}
	_ = h.l.Release()
	delete(s.benchLocks, name)
}
