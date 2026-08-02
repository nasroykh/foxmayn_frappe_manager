//go:build !windows

package cli

import "os"

// hasControllingTerminal reports whether /dev/tty can be opened. That is the
// exact descriptor huh/bubbletea uses for input, so it is the only check that
// reliably predicts whether a prompt can succeed — unlike stat-ing stdin,
// which reports a character device for /dev/null.
func hasControllingTerminal() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
