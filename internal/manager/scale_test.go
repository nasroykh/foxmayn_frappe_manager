package manager

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func ptr(v int) *int { return &v }

func prodScaleBench(name string, webPort int) state.Bench {
	return state.Bench{
		Name: name, Mode: "prod", Domain: name + ".example.com", SiteName: name + ".example.com",
		TLSMode: state.TLSNone, WebPort: webPort, SocketIOPort: webPort + 1000,
		AdminPassword: "s3cret-admin", DBPassword: "s3cret-db1", Bind: state.BindLoopback,
	}
}

// psWorkers is `docker compose ps` for a running prod bench with the given
// number of worker containers.
func psWorkers(long, short int) string {
	out := "mariadb running\nfrappe running\nscheduler running\n"
	out += strings.Repeat("worker-long running\n", long)
	out += strings.Repeat("worker-short running\n", short)
	return out
}

func init() {
	scaleWebWait = 2 * time.Second
	scaleSettleWait = 0
}

func TestScaleReplicasRewritesComposeAndRollsOut(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psWorkers(3, 1)})
	s, b := newTestBench(t, prodScaleBench("sc", 8000))

	got, err := s.Scale(ScaleInput{Name: "sc", WorkerLong: ptr(3)}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if got != (ScaleSettings{Gunicorn: 2, WorkerLong: 3, WorkerShort: 1}) {
		t.Errorf("settings = %+v", got)
	}
	compose, err := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "replicas: 3") {
		t.Errorf("worker-long replicas not in the compose file:\n%s", compose)
	}
	if !fake.Called("compose -p ffm-sc", "up -d") {
		t.Errorf("containers not rolled out; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	// Replicas only: the site is not touched, so nothing waits for it and
	// socket.io is not restarted.
	if fake.Called("restart") {
		t.Errorf("a service was restarted for a replica change:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	rec, _ := s.GetBench("sc")
	if rec.WorkerLongCount != 3 || rec.WorkerShortCount != 1 || rec.GunicornWorkers != 2 {
		t.Errorf("record = %d/%d/%d", rec.GunicornWorkers, rec.WorkerLongCount, rec.WorkerShortCount)
	}
}

func TestScaleGunicornWaitsForTheWebServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psWorkers(1, 1)})
	s, b := newTestBench(t, prodScaleBench("sg", port))

	if _, err := s.Scale(ScaleInput{Name: "sg", Gunicorn: ptr(5)}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	compose, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if !strings.Contains(string(compose), " -w 5 ") {
		t.Errorf("gunicorn -w 5 not in the compose file:\n%s", compose)
	}
	if !fake.Called("up -d") {
		t.Error("containers not rolled out")
	}

	// No server behind the port: the wait fails and the record keeps the
	// new value, with a hint to reconcile.
	srv.Close()
	_, err := s.Scale(ScaleInput{Name: "sg", Gunicorn: ptr(6)}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "ffm reconcile sg") {
		t.Errorf("err = %v, want the reconcile hint", err)
	}
	if rec, _ := s.GetBench("sg"); rec.GunicornWorkers != 6 {
		t.Errorf("recorded gunicorn = %d, want 6", rec.GunicornWorkers)
	}
}

func TestScaleValidationRunsNothing(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psWorkers(1, 1)})
	s, b := newTestBench(t, prodScaleBench("sv", 8000))
	if err := s.AddBench(state.Bench{Name: "devb", Mode: "dev", SiteName: "devb.localhost", WebPort: 8010, SocketIOPort: 9010}); err != nil {
		t.Fatal(err)
	}

	for _, in := range []ScaleInput{
		{Name: "sv", WorkerLong: ptr(0)},
		{Name: "sv", WorkerShort: ptr(ScaleMax + 1)},
		{Name: "sv", Gunicorn: ptr(-1)},
		{Name: "sv"}, // nothing to change
	} {
		if _, err := s.Scale(in, DiscardProgress{}); err == nil {
			t.Errorf("%+v accepted", in)
		}
	}
	if _, err := s.Scale(ScaleInput{Name: "devb", WorkerLong: ptr(2)}, DiscardProgress{}); !errors.Is(err, ErrProdOnly) {
		t.Errorf("dev bench: err = %v, want ErrProdOnly", err)
	}
	if _, err := s.ScaleStatus("devb"); !errors.Is(err, ErrProdOnly) {
		t.Errorf("dev bench status: err = %v, want ErrProdOnly", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("a rejected scale ran commands: %v", fake.Calls())
	}
	if raw, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml")); string(raw) != "stale: true\n" {
		t.Error("a rejected scale rewrote the compose file")
	}
	if rec, _ := s.GetBench("sv"); rec.WorkerLongCount != 0 || rec.GunicornWorkers != 0 {
		t.Errorf("a rejected scale changed the record: %+v", rec)
	}
	// The boundaries themselves are fine.
	if _, err := s.Scale(ScaleInput{Name: "sv", WorkerLong: ptr(ScaleMax), DryRun: true}, DiscardProgress{}); err != nil {
		t.Errorf("the maximum was refused: %v", err)
	}
}

func TestScaleDryRunAndNoOp(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psWorkers(1, 1)})
	s, b := newTestBench(t, prodScaleBench("sd", 8000))

	if _, err := s.Scale(ScaleInput{Name: "sd", WorkerLong: ptr(4), DryRun: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	// Unset counts fall back to 2 / 1 / 1, so asking for those is a no-op.
	if _, err := s.Scale(ScaleInput{Name: "sd", Gunicorn: ptr(2), WorkerLong: ptr(1)}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("dry run / no-op ran commands: %v", fake.Calls())
	}
	if raw, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml")); string(raw) != "stale: true\n" {
		t.Error("dry run / no-op rewrote the compose file")
	}
}

func TestScaleStoppedBenchOnlyRewritesTheFile(t *testing.T) {
	fake := fakeexec.Install(t) // ps prints nothing: stopped
	s, b := newTestBench(t, prodScaleBench("ss", 8000))
	if _, err := s.Scale(ScaleInput{Name: "ss", WorkerShort: ptr(2)}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if fake.Called("up -d") {
		t.Error("a stopped bench was started")
	}
	compose, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if !strings.Contains(string(compose), "replicas: 2") {
		t.Errorf("replicas not rendered:\n%s", compose)
	}
	if rec, _ := s.GetBench("ss"); rec.WorkerShortCount != 2 {
		t.Errorf("record worker-short = %d", rec.WorkerShortCount)
	}
}

func TestScaleStatusReportsRunningContainers(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psWorkers(2, 1)})
	b := prodScaleBench("st", 8000)
	b.WorkerLongCount = 2
	s, _ := newTestBench(t, b)
	v, err := s.ScaleStatus("st")
	if err != nil {
		t.Fatal(err)
	}
	if v.Settings != (ScaleSettings{Gunicorn: 2, WorkerLong: 2, WorkerShort: 1}) {
		t.Errorf("settings = %+v", v.Settings)
	}
	if v.Running["worker-long"] != 2 || v.Running["worker-short"] != 1 {
		t.Errorf("running = %v", v.Running)
	}
}
