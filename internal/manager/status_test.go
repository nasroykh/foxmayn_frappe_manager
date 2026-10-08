package manager

import (
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestBenchStatus(t *testing.T) {
	for ps, want := range map[string]string{
		"mariadb running\nfrappe running\nredis-cache running": StatusRunning,
		"frappe running": StatusRunning,
		// The case ffm list used to call running: the frappe container is
		// down while Redis or the database still run.
		"redis-queue running":                  StatusPartial,
		"mariadb running\nredis-cache running": StatusPartial,
		"":                                     StatusStopped,
		"frappe restarting":                    StatusStopped,
		// A service whose name merely contains "up" is not a running bench.
		"backup-sidecar exited": StatusStopped,
	} {
		if got := benchStatus(ps); got != want {
			t.Errorf("benchStatus(%q) = %q, want %q", ps, got, want)
		}
	}
}

func TestPoweroffStopsPartialBenches(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: "redis-queue running\n"})
	s, _ := newTestBench(t, state.Bench{Name: "pp", Mode: "dev", SiteName: "pp.localhost", WebPort: 8000, SocketIOPort: 9000})
	if st := s.LiveStatus(state.Bench{Name: "pp", Dir: t.TempDir()}); st != StatusPartial {
		t.Fatalf("status = %q", st)
	}
	if err := s.Poweroff(true, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("compose -p ffm-pp", " stop") {
		t.Errorf("a partly running bench was not stopped; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestTeardownRemovesVolumesTheTemplateDropped(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "volume ls -q --filter label=com.docker.compose.project=ffm-td", Stdout: "ffm-td_mariadb-data\nffm-td_yarn-cache\n"})
	s, b := newTestBench(t, state.Bench{Name: "td", Mode: "dev", SiteName: "td.localhost", WebPort: 8000, SocketIOPort: 9000})
	s.TeardownBenchFiles(b)
	if !fake.Called("volume rm ffm-td_yarn-cache") {
		t.Errorf("leftover volume kept; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}
