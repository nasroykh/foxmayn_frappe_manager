package manager

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

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

// secretAdder is implemented by the ProgressWriters that mask secrets in what
// they record (jobs). A step that learns a secret late, such as Restore after
// reading an encrypted archive's manifest, registers it so everything recorded
// afterwards is masked.
type secretAdder interface{ AddSecrets(vals ...string) }

// addSecrets registers vals with pw when it can mask them; any other writer
// ignores them.
func addSecrets(pw ProgressWriter, vals ...string) {
	if a, ok := pw.(secretAdder); ok {
		a.AddSecrets(vals...)
	}
}

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

// maxPartialLine bounds the bytes a lineWriter holds waiting for a newline.
const maxPartialLine = 64 << 10

// lineWriter turns a byte stream into lines. It backs Stderr() for the
// ProgressWriters that keep their output as lines (BufferProgress, jobs), so
// what bench.Runner prints to stderr on a failed build, and the warnings, land
// in the job instead of in the dashboard's log file.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(line string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxPartialLine {
		w.emit(string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}

// Flush emits a final line that had no newline.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(strings.TrimSuffix(string(w.buf), "\r"))
		w.buf = nil
	}
}

// BufferProgress captures steps and lines for callers that read them
// afterwards. Its Stderr writes lines into Lines. It is not safe for
// concurrent use; jobs use jobProgress instead.
type BufferProgress struct {
	Steps []string
	Lines []string

	errw *lineWriter
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

func (b *BufferProgress) Stderr() io.Writer {
	if b.errw == nil {
		b.errw = &lineWriter{emit: func(line string) { b.Lines = append(b.Lines, line) }}
	}
	return b.errw
}
