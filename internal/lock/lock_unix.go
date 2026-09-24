//go:build !windows

package lock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func tryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrHeld
	}
	return err
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// inheritOwner gives path the owner of its parent directory when running as
// root. Otherwise the first `sudo ffm` would create ~/.config/ffm/locks (and
// its lock files) as root, and the real user could not create the lock file
// for any bench it had not already seen.
func inheritOwner(path string) {
	if os.Geteuid() != 0 {
		return
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return
	}
	if st, ok := parent.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}
