package manager

import (
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestDoctorFailsABrokenProxyOnADevBench(t *testing.T) {
	// A running Traefik container with no network endpoint and no published port.
	fakeexec.Install(t,
		fakeexec.Rule{Match: "inspect ffm-proxy", Stdout: `{"status":"running","image":"traefik:v3.7","networks":{},"ports":{},"bindings":{},"args":[]}` + "\n"},
		fakeexec.Rule{Match: " ps ", Stdout: "frappe running abc\nmariadb running def\n"},
		fakeexec.Rule{Match: "inspect --format {{.RestartCount}}", Stdout: "0\n0\n"},
		fakeexec.Rule{Match: "FFM_DOCTOR", Stdout: `FFM_DOCTOR={"scheduler_inactive": false, "queues": {}}` + "\n"},
	)
	port := pingServer(t)
	s, _ := newTestBench(t, state.Bench{Name: "dev1", Mode: "dev", SiteName: "dev1.localhost", WebPort: port, SocketIOPort: port + 1000})
	h, err := s.Doctor("dev1")
	if err != nil {
		t.Fatal(err)
	}
	c := checkByName(h, "proxy")
	if c.Status != CheckFail || !strings.Contains(c.Detail, "not attached") || !strings.Contains(c.Detail, "ffm proxy start") {
		t.Errorf("proxy check: %+v", c)
	}
	if got := checkByName(h, "proxy route"); got.Name != "" {
		t.Errorf("the route check must not run against a broken proxy: %+v", got)
	}
}
