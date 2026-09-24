//go:build !windows

package state

import (
	"os"
	"syscall"
)

// keepOwner gives tmp the owner of the file it is about to replace.
//
// A rename replaces the file's inode, so the state file would otherwise take
// the owner of whoever wrote it last. After a single `sudo ffm ...` that is
// root — and with mode 0600, every later ffm run as the real user could no
// longer even read its own state. Only root can chown, and only root needs to.
func keepOwner(tmp *os.File, path string) {
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = tmp.Chown(int(st.Uid), int(st.Gid))
	}
}
