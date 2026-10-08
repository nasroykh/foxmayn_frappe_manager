//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func configureSysProcAttr(sys *syscall.SysProcAttr) {
	sys.Setsid = true
}

// processAlive reports whether pid is a running process (signal 0 probes it
// without delivering anything).
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// stopProcess asks pid to shut down gracefully.
func stopProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
