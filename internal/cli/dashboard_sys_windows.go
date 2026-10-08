//go:build windows

package cli

import (
	"os"
	"syscall"
)

func configureSysProcAttr(sys *syscall.SysProcAttr) {
	sys.HideWindow = true
}

// processAlive reports whether pid is a running process. On Windows
// os.FindProcess opens a handle and fails when the process does not exist;
// Go supports no signal but Kill there, so the Unix Signal(0) probe always
// reported "not running".
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = proc.Release()
	return true
}

// stopProcess terminates pid. Windows has no SIGTERM for a detached process,
// so this is a hard kill; the dashboard holds no state that needs flushing.
func stopProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
