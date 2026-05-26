//go:build windows

package cli

import "syscall"

func configureSysProcAttr(sys *syscall.SysProcAttr) {
	sys.HideWindow = true
}
