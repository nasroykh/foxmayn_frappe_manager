package manager

import "testing"

func TestTailLines(t *testing.T) {
	const log = "a\nb\nc\nd\ne"
	if got := tailLines(log, 2); got != "d\ne" {
		t.Errorf("tailLines(2) = %q", got)
	}
	if got := tailLines(log, 99); got != log {
		t.Errorf("tailLines(99) = %q, want the whole log", got)
	}
	if got := tailLines("\n\n  \n", 5); got != "" {
		t.Errorf("tailLines on a blank log = %q, want empty so callers omit it", got)
	}
}
