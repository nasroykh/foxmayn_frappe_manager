//go:build !windows

package manager

import "syscall"

// freeBytes reports the space available to an unprivileged user at path, and
// whether the figure could be obtained at all.
func freeBytes(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	// Bavail, not Bfree: the reserved blocks are not ours to fill.
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
