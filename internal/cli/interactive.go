package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
)

// nonInteractive is set by the global --non-interactive flag.
var nonInteractive bool

// isInteractive reports whether it is safe to open a TUI prompt.
//
// huh/bubbletea does not read os.Stdin directly — it opens the controlling
// terminal — so checking stdin is not a reliable signal (/dev/null is itself a
// character device, and a redirected stdin says nothing about whether a
// terminal exists). Two failure modes matter for automation: a host with no
// controlling terminal fails the form with "could not open a new TTY", and a
// host that has one but no human attached blocks forever. The second is far
// worse in CI — it burns the whole job timeout with no output — so we refuse
// to prompt unless an interactive session can be positively confirmed.
func isInteractive() bool {
	if nonInteractive {
		return false
	}
	if envEnabled("FFM_NON_INTERACTIVE") || envEnabled("CI") {
		return false
	}
	// Escape hatch for any environment where the terminal probe is wrong but
	// prompting genuinely works. The explicit --non-interactive flag still wins,
	// so this can only turn prompting back on, never force it off.
	if envEnabled("FFM_INTERACTIVE") {
		return true
	}
	return hasControllingTerminal()
}

// envEnabled reports whether an environment variable is set to a truthy value.
// Present-but-empty, "0" and "false" all count as unset so that a caller can
// neutralise an inherited variable.
func envEnabled(key string) bool {
	switch os.Getenv(key) {
	case "", "0", "false", "FALSE", "False":
		return false
	}
	return true
}

// mustNotPrompt builds the error returned in place of an interactive prompt.
// hint should name the flag that supplies the missing value non-interactively.
func mustNotPrompt(what, hint string) error {
	return errors.New("refusing to prompt for " + what +
		" — no interactive terminal (or --non-interactive/$CI set): " + hint)
}

// cancelled reports whether a form error is a user abort (Esc or ctrl+c), and
// prints the standard notice when it is. Callers should return nil so that
// backing out of a prompt is not reported as a command failure.
//
// Forms must be built with benchPickKeyMap for Esc to reach this — huh's
// default keymap binds Quit to ctrl+c only.
func cancelled(err error) bool {
	if errors.Is(err, huh.ErrUserAborted) {
		fmt.Println("Cancelled.")
		return true
	}
	return false
}

// withSpinner runs action, showing a spinner only when interactive. The spinner
// is a bubbletea program; starting one without a terminal fails and would skip
// the action entirely, so non-interactive callers run it directly instead.
func withSpinner(title string, action func()) {
	if !isInteractive() {
		action()
		return
	}
	runSpinner(title, action)
}
