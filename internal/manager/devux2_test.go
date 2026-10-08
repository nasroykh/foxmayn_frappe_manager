package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const procfile = "redis_cache: redis-server config/redis_cache.conf\n" +
	"web: bench serve --port 8000\n" +
	"socketio: /usr/bin/node apps/frappe/socketio.js\n"

func TestSetDebugWebRoundTrip(t *testing.T) {
	on, changed, err := setDebugWeb(procfile, true)
	if err != nil || !changed {
		t.Fatalf("on: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(on, debugMarker+"web: bench serve --port 8000\n") || !strings.Contains(on, "-m debugpy --listen 0.0.0.0:8005") {
		t.Errorf("on:\n%s", on)
	}
	if strings.Count(on, "\nweb:") != 1 {
		t.Errorf("more than one web line:\n%s", on)
	}
	again, changed, _ := setDebugWeb(on, true)
	if changed || again != on {
		t.Error("turning debug on twice changed the Procfile again")
	}
	off, changed, err := setDebugWeb(on, false)
	if err != nil || !changed || off != procfile {
		t.Errorf("off did not restore the original (changed=%v err=%v):\n%s", changed, err, off)
	}
	if _, changed, _ := setDebugWeb(procfile, false); changed {
		t.Error("turning debug off on a plain Procfile changed it")
	}
	if _, _, err := setDebugWeb("worker: x\n", true); err == nil {
		t.Error("a Procfile without web: was accepted")
	}
}

func TestDebugRefusesLANAndProd(t *testing.T) {
	fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "dl", Mode: "dev", SiteName: "dl.localhost", WebPort: 8000, SocketIOPort: 9000, Bind: state.BindLAN})
	if err := s.Debug("dl", true, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "every interface") {
		t.Errorf("LAN bench: err = %v", err)
	}
	if err := s.AddBench(state.Bench{Name: "dp", Mode: "prod", SiteName: "dp.example.com", WebPort: 8010, SocketIOPort: 9010}); err != nil {
		t.Fatal(err)
	}
	if err := s.Debug("dp", true, DiscardProgress{}); err == nil {
		t.Error("prod bench accepted")
	}
}

func TestDebugOnWritesLaunchJSONAndRestarts(t *testing.T) {
	fake := fakeexec.Install(t)
	s, b := newTestBench(t, state.Bench{Name: "dg", Mode: "dev", SiteName: "dg.localhost", WebPort: 8040, SocketIOPort: 9040, Bind: state.BindLoopback})
	bench := filepath.Join(b.Dir, "workspace", "frappe-bench")
	os.MkdirAll(bench, 0o755)
	os.WriteFile(filepath.Join(bench, "Procfile"), []byte(procfile), 0o644)

	if err := s.Debug("dg", true, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("debugpy") || !fake.Called("[h]oncho start") {
		t.Errorf("debugpy not installed or server not restarted; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	raw, err := os.ReadFile(filepath.Join(bench, ".vscode", "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Configurations []struct {
			Connect struct{ Port int } `json:"connect"`
		} `json:"configurations"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Configurations) != 2 ||
		cfg.Configurations[0].Connect.Port != 8045 || cfg.Configurations[1].Connect.Port != 8005 {
		t.Errorf("launch.json = %s (err %v)", raw, err)
	}
	if on, port, _ := s.DebugStatus("dg"); !on || port != 8045 {
		t.Errorf("status = %v %d", on, port)
	}

	// A launch.json the user wrote is left alone.
	os.WriteFile(filepath.Join(bench, ".vscode", "launch.json"), []byte("// mine\n"), 0o644)
	if err := s.Debug("dg", false, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Debug("dg", true, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(bench, ".vscode", "launch.json")); string(raw) != "// mine\n" {
		t.Error("user's launch.json overwritten")
	}
}

func TestTestCommand(t *testing.T) {
	fake := fakeexec.Install(t)
	s, b := newTestBench(t, state.Bench{Name: "tt", Mode: "dev", SiteName: "tt.localhost", WebPort: 8050, SocketIOPort: 9050})
	path := writeSite(t, b, map[string]any{"db_name": "_x"})
	var out bytes.Buffer
	if err := s.Test(TestInput{Name: "tt", App: "erpnext", Doctype: "Sales Invoice", Failfast: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("exec -T -w /workspace/frappe-bench frappe", "bench --site 'tt.localhost' run-tests --app 'erpnext' --doctype 'Sales Invoice' --failfast") {
		t.Errorf("calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if cfg := readSite(t, path); cfg["allow_tests"] != true {
		t.Errorf("allow_tests not set: %v", cfg)
	}
	if !strings.Contains(out.String(), "Enabled allow_tests") {
		t.Errorf("no notice: %q", out.String())
	}
	if err := s.Test(TestInput{Name: "tt", App: "erpnext", Test: "test_x"}); err == nil {
		t.Error("--test without --module/--doctype accepted")
	}
}

func TestTestFailureAndProd(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: "run-tests", Exit: 1})
	s, b := newTestBench(t, state.Bench{Name: "tf", Mode: "dev", SiteName: "tf.localhost", WebPort: 8060, SocketIOPort: 9060})
	writeSite(t, b, map[string]any{"allow_tests": true})
	if err := s.Test(TestInput{Name: "tf", App: "x"}); !errors.Is(err, ErrTestsFailed) {
		t.Errorf("err = %v, want ErrTestsFailed", err)
	}
	if err := s.AddBench(state.Bench{Name: "tp", Mode: "prod", SiteName: "tp.example.com", WebPort: 8070, SocketIOPort: 9070}); err != nil {
		t.Fatal(err)
	}
	if err := s.Test(TestInput{Name: "tp", App: "x"}); err == nil {
		t.Error("prod bench accepted")
	}
}

func TestCleanPlanOnlyOrphans(t *testing.T) {
	vols := `[{"Name":"ffm-live_mariadb-data","Labels":"com.docker.compose.project=ffm-live,com.docker.compose.volume=mariadb-data","Size":"300MB"},` +
		`{"Name":"ffm-gone_mariadb-data","Labels":"com.docker.compose.project=ffm-gone","Size":"200MB"},` +
		`{"Name":"ffm-inflight_mariadb-data","Labels":"com.docker.compose.project=ffm-inflight","Size":"1MB"},` +
		`{"Name":"other_data","Labels":"com.docker.compose.project=other","Size":"9GB"},` +
		`{"Name":"ffm-x","Labels":"","Size":"1MB"}]`
	fakeexec.Install(t,
		fakeexec.Rule{Match: "system df -v", Stdout: vols},
		fakeexec.Rule{Match: "image ls", Stdout: "ffm-gone-frappe:latest\t3GB\tid1\nffm-live-frappe:latest\t3GB\tid2\nnginx:1\t50MB\tid3\n"},
		fakeexec.Rule{Match: "image inspect --format {{index .Config.Labels \"com.docker.compose.project\"}} id1", Stdout: "ffm-gone"},
		fakeexec.Rule{Match: "image inspect --format {{index .Config.Labels \"com.docker.compose.project\"}} id2", Stdout: "ffm-live"},
	)
	s, _ := newTestBench(t, state.Bench{Name: "live", Mode: "dev", SiteName: "live.localhost", WebPort: 8080, SocketIOPort: 9080})
	// A bench being created has its directory but no record yet.
	os.MkdirAll(config.BenchDir("inflight"), 0o755)

	items, err := s.CleanPlan(CleanInput{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Kind+" "+it.Name)
	}
	want := []string{"volume ffm-gone_mariadb-data", "image ffm-gone-frappe:latest"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("plan = %v, want %v", got, want)
	}
	release, err := s.lockBench("gone")
	if err != nil {
		t.Fatal(err)
	}
	items, _ = s.CleanPlan(CleanInput{})
	release()
	if len(items) != 0 {
		t.Errorf("a locked project was planned for removal: %v", items)
	}
}

func TestPoweroffStopsRunningBenches(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, _ := newTestBench(t, state.Bench{Name: "pa", Mode: "prod", SiteName: "pa.example.com", WebPort: 8090, SocketIOPort: 9090})
	if err := s.Poweroff(false, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("compose -p ffm-pa", " stop") {
		t.Errorf("bench not stopped; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestTestInstallsDevDepsOnce(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "import xmlrunner", Exit: 1})
	s, b := newTestBench(t, state.Bench{Name: "td", Mode: "dev", SiteName: "td.localhost", WebPort: 8100, SocketIOPort: 9100})
	writeSite(t, b, map[string]any{"allow_tests": true})
	if err := s.Test(TestInput{Name: "td", App: "x"}); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("bench setup requirements --dev") {
		t.Errorf("dev dependencies not installed; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestNewLines(t *testing.T) {
	got := newLines("frappe/a.json\nmyapp/b.py\n", "frappe/a.json\nmyapp/b.py\nfrappe/desk/todo.json\n")
	if len(got) != 1 || got[0] != "frappe/desk/todo.json" {
		t.Errorf("newLines = %v", got)
	}
}
