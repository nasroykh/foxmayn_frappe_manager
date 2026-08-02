//go:build windows

package cli

import "os"

// hasControllingTerminal reports whether stdin is a console handle. Windows has
// no /dev/tty; bubbletea reads the console via stdin/CONIN$, so a
// character-device stdin is the closest available signal.
func hasControllingTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
