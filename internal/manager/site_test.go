package manager

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestSiteCommands(t *testing.T) {
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
		fakeexec.Rule{Match: "scheduler status --format json", Stdout: "noise\n{\"status\": \"Paused\"}\n"},
	)
	s, b := newTestBench(t, state.Bench{Name: "st", Mode: "dev", SiteName: "st.localhost", WebPort: 8000, SocketIOPort: 9000})
	writeSite(t, b, map[string]any{"maintenance_mode": 1})

	st, err := s.SiteStatus("st")
	if err != nil || !st.Maintenance || st.Scheduler != "paused" {
		t.Errorf("status = %+v, %v", st, err)
	}
	if err := s.SiteMaintenance("st", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SiteScheduler("st", "pause"); err != nil {
		t.Fatal(err)
	}
	if err := s.SiteScheduler("st", "explode"); err == nil {
		t.Error("an unknown scheduler action was accepted")
	}
	var out bytes.Buffer
	if err := s.SiteMigrate("st", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bench --site 'st.localhost' set-maintenance-mode off", "bench --site 'st.localhost' scheduler pause", "bench --site 'st.localhost' migrate"} {
		if !fake.Called(want) {
			t.Errorf("missing %q; calls:\n%s", want, strings.Join(fake.Calls(), "\n"))
		}
	}
}

func TestSiteMigrateRefusesStoppedBench(t *testing.T) {
	fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "sx", Mode: "dev", SiteName: "sx.localhost", WebPort: 8000, SocketIOPort: 9000})
	if err := s.SiteMigrate("sx", nil); err == nil {
		t.Error("migrated a stopped bench")
	}
}
