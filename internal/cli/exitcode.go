package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Exit codes. Scripts may rely on these; changing one is a breaking change and
// goes under "Upgrade notes" in the release.
const (
	ExitOK         = 0
	ExitFailure    = 1 // anything not listed below
	ExitUsage      = 2 // bad flags or arguments, or a prompt needed without a terminal
	ExitNotFound   = 3 // the named bench does not exist
	ExitBusy       = 4 // another ffm operation holds the bench
	ExitDocker     = 5 // reserved: Docker unavailable
	ExitWrongState = 6 // the bench is not in a state the operation needs (e.g. stopped)
	ExitPreflight  = 7 // the operation refused before changing anything (restore preflight)
)

// usageError marks an error as a usage error (exit 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// ExitCode maps an error returned by Execute to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var usage usageError
	var preflight *manager.PreflightError
	switch {
	case errors.As(err, &usage):
		return ExitUsage
	case errors.Is(err, state.ErrNotFound), errors.Is(err, state.ErrNoBenches):
		return ExitNotFound
	case errors.Is(err, manager.ErrBenchBusy):
		return ExitBusy
	case errors.Is(err, manager.ErrBenchStopped):
		return ExitWrongState
	case errors.As(err, &preflight):
		return ExitPreflight
	}
	// Cobra's own command lookup errors are plain strings.
	if strings.HasPrefix(err.Error(), "unknown command ") {
		return ExitUsage
	}
	return ExitFailure
}

// markUsageErrors makes flag and positional-argument errors usage errors on
// every command of the tree.
func markUsageErrors(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	if args := cmd.Args; args != nil {
		cmd.Args = func(c *cobra.Command, a []string) error {
			if err := args(c, a); err != nil {
				return usageError{err}
			}
			return nil
		}
	}
	for _, c := range cmd.Commands() {
		markUsageErrors(c)
	}
}
