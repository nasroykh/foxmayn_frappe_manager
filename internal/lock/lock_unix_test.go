//go:build !windows

package lock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRootCreatedLocksBelongToTheUser: as root, the lock directory and file
// take the owner of the directory they live in, so a first `sudo ffm` does not
// lock the real user out of creating locks later.
func TestRootCreatedLocksBelongToTheUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	cfg := t.TempDir()
	if err := os.Chown(cfg, 4242, 4343); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg, "locks", "bench-x.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	for _, p := range []string{filepath.Dir(path), path} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != 4242 || st.Gid != 4343 {
			t.Errorf("%s owned by %d:%d, want 4242:4343", filepath.Base(p), st.Uid, st.Gid)
		}
	}
}
