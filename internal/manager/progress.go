package manager

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// ProgressWriter receives step messages and log output during long operations.
type ProgressWriter interface {
	Step(msg string)
	Printf(format string, args ...any)
	Println(args ...any)
	Stderr() io.Writer
}

// CLIProgress writes to stdout/stderr like the ffm CLI. Secrets in its output
// (passwords after create, an API secret) are shown when stdout is a terminal
// or Reveal is set (--show-secrets), and masked otherwise: output that is
// piped, redirected or captured by CI ends up in logs and scrollback.
type CLIProgress struct {
	Reveal bool
}

// RevealSecrets reports whether secrets may be printed.
func (c CLIProgress) RevealSecrets() bool { return c.Reveal || StdoutIsTerminal() }

// StdoutIsTerminal reports whether stdout is a terminal. It is NOT the same as
// the CLI's isInteractive(): that probes /dev/tty so prompts work with stdin
// redirected, whereas secrets must stay off anything stdout is redirected to.
func StdoutIsTerminal() bool {
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// secretRevealer is implemented by the ProgressWriters whose output goes to a
// person at a terminal. Every other writer (jobs, MCP, hooks, log files)
// persists or relays what it is given, so it never reveals.
type secretRevealer interface{ RevealSecrets() bool }

// MaskedSecret is what a secret is printed as when it must not be shown.
const MaskedSecret = "********"

// SecretFor returns secret when pw may reveal it, else "******** (hint)" where
// hint says how to get the value, e.g. "ffm status mybench --show-secrets".
func SecretFor(pw ProgressWriter, secret, hint string) string {
	if r, ok := pw.(secretRevealer); ok && r.RevealSecrets() {
		return secret
	}
	return maskedWith(hint)
}

func maskedWith(hint string) string {
	if hint == "" {
		return MaskedSecret
	}
	return MaskedSecret + " (" + hint + ")"
}

// Secret is SecretFor for a command's text output with no ProgressWriter:
// show when stdout is a terminal or the user passed --show-secrets.
func Secret(secret, hint string, show bool) string {
	if show || StdoutIsTerminal() {
		return secret
	}
	return maskedWith(hint)
}

func (CLIProgress) Step(msg string) {
	fmt.Printf("  → %s\n", msg)
}

func (CLIProgress) Printf(format string, args ...any) {
	fmt.Printf(format, args...)
}

func (CLIProgress) Println(args ...any) {
	fmt.Println(args...)
}

func (CLIProgress) Stderr() io.Writer { return os.Stderr }

// DiscardProgress drops step messages (used when output is captured elsewhere).
type DiscardProgress struct{}

func (DiscardProgress) Step(string)           {}
func (DiscardProgress) Printf(string, ...any) {}
func (DiscardProgress) Println(...any)        {}
func (DiscardProgress) Stderr() io.Writer     { return io.Discard }

// BufferProgress captures steps and lines for jobs / SSE.
type BufferProgress struct {
	Steps []string
	Lines []string
}

func (b *BufferProgress) Step(msg string) {
	b.Steps = append(b.Steps, msg)
	b.Lines = append(b.Lines, "→ "+msg)
}

func (b *BufferProgress) Printf(format string, args ...any) {
	b.Lines = append(b.Lines, fmt.Sprintf(format, args...))
}

func (b *BufferProgress) Println(args ...any) {
	b.Lines = append(b.Lines, fmt.Sprint(args...))
}

func (b *BufferProgress) Stderr() io.Writer { return io.Discard }
