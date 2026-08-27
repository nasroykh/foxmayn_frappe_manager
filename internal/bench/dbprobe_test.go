package bench

import "testing"

func TestExecRefusedNamesTheReason(t *testing.T) {
	cases := []struct{ out, want string }{
		{`service "frappe" is not running`, "the frappe container is not running"},
		{"Error: No container found for frappe_1", "the frappe container is not running"},
		{`no such service: frappe`, "this bench has no frappe service — its docker-compose.yml may be from an older ffm"},
		{`exec: "python3": executable file not found in $PATH`, "python3 is missing from the frappe image"},
		// A probe that ran and failed on its own terms is not an exec refusal:
		// this is the case the retry loop exists for.
		{"ConnectionRefusedError: [Errno 111] Connection refused", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := execRefused(c.out); got != c.want {
			t.Errorf("execRefused(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestLastLineSkipsTrailingBlanks(t *testing.T) {
	if got := lastLine("first\nTimeoutError: timed out\n\n  \n"); got != "TimeoutError: timed out" {
		t.Fatalf("lastLine = %q", got)
	}
	if got := lastLine("   "); got != "" {
		t.Fatalf("lastLine on blank input = %q", got)
	}
}
