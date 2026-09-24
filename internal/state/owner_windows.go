//go:build windows

package state

import "os"

// keepOwner is a no-op on Windows, where a rename keeps no Unix owner to lose.
func keepOwner(*os.File, string) {}
