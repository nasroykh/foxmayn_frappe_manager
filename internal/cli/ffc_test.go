package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

func TestFfcForceNeedsHost(t *testing.T) {
	cmd := newFfcCmd()
	cmd.SetArgs([]string{"--force", "somebench"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	var u usageError
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want a usageError", err)
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("error does not name --host: %v", err)
	}
}

func TestFfcStatusValue(t *testing.T) {
	pinned := strings.TrimPrefix(bench.FfcRef, "v")
	if got := ffcStatusValue(pinned); got != pinned {
		t.Errorf("matching version = %q, want it unchanged", got)
	}
	got := ffcStatusValue("0.0.1")
	if !strings.Contains(got, "0.0.1") || !strings.Contains(got, bench.FfcRef) || !strings.Contains(got, "restart --rebuild") {
		t.Errorf("mismatch note = %q", got)
	}
}
