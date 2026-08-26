//go:build windows

package manager

// freeBytes has no dependency-free implementation on Windows, so the space
// preflight is skipped there rather than pulling in golang.org/x/sys for it.
func freeBytes(string) (uint64, bool) { return 0, false }
