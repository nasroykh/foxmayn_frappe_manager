package manager

import (
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

// A venv the image cannot run stops Start before compose up, with the way out.
func TestStartRefusesAVenvItsImageCannotRun(t *testing.T) {
	r := newMountRig(t, "old", fakeexec.Rule{Match: "FFM_VENV=", Stdout: "FFM_VENV=gone 3.12.4\n", Exit: 3})
	mountedBench(t, r, "old", nil)
	err := r.s.Start("old", DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "Python 3.12.4") || !strings.Contains(err.Error(), "ffm recreate old") {
		t.Fatalf("err = %v", err)
	}
	if !r.fake.Called(" run --rm --no-deps -T --label traefik.enable=false frappe bash -c ") {
		t.Errorf("repair not run:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if r.fake.Called(" up -d") {
		t.Error("compose up ran with a broken venv")
	}
}
