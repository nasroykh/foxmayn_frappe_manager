package manager

import (
	"fmt"
	"io"
	"os"
)

// ProgressWriter receives step messages and log output during long operations.
type ProgressWriter interface {
	Step(msg string)
	Printf(format string, args ...any)
	Println(args ...any)
	Stderr() io.Writer
}

// CLIProgress writes to stdout/stderr like the ffm CLI.
type CLIProgress struct{}

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

func (DiscardProgress) Step(string)                          {}
func (DiscardProgress) Printf(string, ...any)                {}
func (DiscardProgress) Println(...any)                       {}
func (DiscardProgress) Stderr() io.Writer                    { return io.Discard }

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
