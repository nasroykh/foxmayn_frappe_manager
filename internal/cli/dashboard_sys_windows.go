//go:build windows

package cli

import (
	"os"
	"syscall"
)

// detachedProcess is the Win32 DETACHED_PROCESS creation flag; package syscall
// does not export it.
const detachedProcess = 0x00000008

func configureSysProcAttr(sys *syscall.SysProcAttr) {
	sys.HideWindow = true
	// Without these the daemon shares the launching console and can die with
	// it (Ctrl+C, closing the window).
	sys.CreationFlags = detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP
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
