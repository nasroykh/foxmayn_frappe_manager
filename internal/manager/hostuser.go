package manager

import (
	"fmt"
	"os"
)

// hostUserIDs returns the uid/gid the container's `frappe` user should be
// remapped to, or an error explaining why remapping is not possible here.
//
// Remapping only makes sense for a real unprivileged host user. Running ffm as
// root would remap the container user to uid 0, defeating the point, and
// Windows has no meaningful uid to report (os.Getuid returns -1).
func hostUserIDs() (uid, gid int, err error) {
	uid, gid = os.Getuid(), os.Getgid()
	if uid <= 0 || gid <= 0 {
		return 0, 0, fmt.Errorf(
			"--match-host-user needs an unprivileged POSIX host user (got uid=%d gid=%d); "+
				"it is not supported on Windows or when running as root", uid, gid)
	}
	return uid, gid, nil
}

// composeUserIDs resolves the ComposeData HostUID/HostGID pair for a bench.
// When match is false it returns 0,0 — the templates then render exactly the
// Dockerfile ffm produced before this option existed.
func composeUserIDs(match bool) (uid, gid int, err error) {
	if !match {
		return 0, 0, nil
	}
	return hostUserIDs()
}
