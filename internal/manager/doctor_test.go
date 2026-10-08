package manager

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func checkByName(h BenchHealth, name string) Check {
	for _, c := range h.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

// pingServer answers /api/method/ping like Frappe, on a free local port.
func pingServer(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/method/ping" {
			w.Write([]byte(`{"message":"pong"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	return p
}

func TestDoctorFindsAQueueNobodyConsumes(t *testing.T) {
	// The kb bench on 2026-10-07: up, answering ping, 47 jobs waiting on
	// "default" and no worker at all.
	fakeexec.Install(t,
		fakeexec.Rule{Match: " ps ", Stdout: "frappe running abc\nmariadb running def\n"},
		fakeexec.Rule{Match: "inspect --format {{.RestartCount}}", Stdout: "0\n0\n"},
		fakeexec.Rule{Match: "FFM_DOCTOR", Stdout: `FFM_DOCTOR={"scheduler_inactive": false, "queues": {"default": {"pending": 47, "workers": 0}, "short": {"pending": 0, "workers": 1}, "long": {"pending": 4, "workers": 0}}}` + "\n"},
	)
	port := pingServer(t)
	s, _ := newTestBench(t, state.Bench{Name: "kb", Mode: "prod", Domain: "erp.example.com", SiteName: "erp.example.com",
		WebPort: port, SocketIOPort: port + 1000, TemplateVersion: 3})
	h, err := s.Doctor("kb")
	if err != nil {
		t.Fatal(err)
	}
	if c := checkByName(h, "site"); c.Status != CheckOK {
		t.Errorf("site: %+v", c)
	}
	w := checkByName(h, "workers")
	if w.Status != CheckFail || !strings.Contains(w.Detail, "default (47 waiting)") || !strings.Contains(w.Detail, "long (4 waiting)") {
		t.Errorf("workers: %+v", w)
	}
	if c := checkByName(h, "backups"); c.Status != CheckWarn {
		t.Errorf("prod without a schedule must warn: %+v", c)
	}
	if h.Worst() != CheckFail || len(h.Failures()) != 1 {
		t.Errorf("worst %s, failures %v", h.Worst(), h.Failures())
	}
}

func TestDoctorPartialBenchAndDevScheduler(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: "redis-queue running abc\n"})
	s, _ := newTestBench(t, state.Bench{Name: "p", Mode: "dev", SiteName: "p.localhost", WebPort: 8000, SocketIOPort: 9000, TemplateVersion: 1,
		BackupSchedule: &state.BackupPolicy{Enabled: true, EveryHours: 24}})
	h, err := s.Doctor("p")
	if err != nil {
		t.Fatal(err)
	}
	if c := checkByName(h, "containers"); c.Status != CheckFail || !strings.Contains(c.Detail, "frappe container is down") {
		t.Errorf("containers: %+v", c)
	}
	if c := checkByName(h, "workers"); c.Status != CheckSkip {
		t.Errorf("workers on a down bench: %+v", c)
	}
	if c := checkByName(h, "backups"); c.Status != CheckFail {
		t.Errorf("scheduled without archives must fail: %+v", c)
	}
	if c := checkByName(h, "templates"); c.Status != CheckWarn {
		t.Errorf("templates: %+v", c)
	}

	fakeexec.Install(t,
		fakeexec.Rule{Match: " ps ", Stdout: "frappe running abc\n"},
		fakeexec.Rule{Match: "FFM_DOCTOR", Stdout: `FFM_DOCTOR={"scheduler_inactive": true, "queues": {"default": {"pending": 0, "workers": 1}}}` + "\n"},
	)
	h, _ = s.Doctor("p")
	if c := checkByName(h, "scheduler"); c.Status != CheckWarn {
		t.Errorf("a disabled scheduler on dev is a warning: %+v", c)
	}
	if c := checkByName(h, "site"); c.Status != CheckFail {
		t.Errorf("nothing listens on 8000 here; site: %+v", c)
	}
}
