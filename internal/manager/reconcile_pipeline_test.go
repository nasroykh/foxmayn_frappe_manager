package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }

// newTestBench registers a dev bench whose docker-compose.yml is stale, in an
// isolated config and benches directory.
func newTestBench(t *testing.T, b state.Bench) (*Service, state.Bench) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(dir, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(dir, "backups"))
	b.Dir = filepath.Join(dir, "benches", b.Name)
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, "docker-compose.yml"), []byte("stale: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(false)
	if err := s.AddBench(b); err != nil {
		t.Fatal(err)
	}
	return s, b
}

// psRunning is `docker compose ps` output for a running bench.
const psRunning = "NAME   IMAGE   SERVICE   STATUS\nffm-x-frappe-1   img   frappe   Up 2 hours\n"

func TestReconcileRunningDevBench(t *testing.T) {
	// `docker compose ps` reports the bench as running; everything else
	// succeeds silently.
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, b := newTestBench(t, state.Bench{
		Name: "alpha", Mode: "dev", SiteName: "alpha.localhost", WebPort: 8000, SocketIOPort: 9000,
		AdminPassword: "s3cret-admin", DBPassword: "s3cret-db1", Bind: state.BindLoopback,
	})

	if err := s.Reconcile(ReconcileInput{Name: "alpha"}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}

	compose, err := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "127.0.0.1:8000-8005:8000-8005") {
		t.Errorf("compose not re-rendered from the record:\n%s", compose)
	}
	if !fake.Called("compose -p ffm-alpha", "up -d") {
		t.Errorf("containers not updated; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if !fake.Called("exec -T frappe", "[h]oncho start") {
		t.Errorf("dev server not restarted; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "s3cret") {
			t.Errorf("a password reached a command line: %s", c)
		}
	}
	rec, err := s.GetBench("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rec.TemplateVersion != bench.TemplateVersion {
		t.Errorf("template version = %d, want %d", rec.TemplateVersion, bench.TemplateVersion)
	}
}

func TestReconcileStoppedBenchOnlyRewritesTheFile(t *testing.T) {
	fake := fakeexec.Install(t) // ps prints nothing: the bench is not running
	s, b := newTestBench(t, state.Bench{
		Name: "beta", Mode: "dev", SiteName: "beta.localhost", WebPort: 8010, SocketIOPort: 9010,
		AdminPassword: "s3cret-admin", DBPassword: "s3cret-db1",
	})
	if err := s.Reconcile(ReconcileInput{Name: "beta"}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if fake.Called("up -d") {
		t.Error("a stopped bench was started")
	}
	compose, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if strings.Contains(string(compose), "stale") {
		t.Error("compose file not rewritten")
	}
}

func TestReconcileDryRunChangesNothing(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, b := newTestBench(t, state.Bench{
		Name: "gamma", Mode: "dev", SiteName: "gamma.localhost", WebPort: 8020, SocketIOPort: 9020,
		AdminPassword: "s3cret-admin", DBPassword: "s3cret-db1",
	})
	if err := s.Reconcile(ReconcileInput{Name: "gamma", DryRun: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	compose, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if string(compose) != "stale: true\n" {
		t.Error("dry run rewrote the compose file")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("dry run ran commands: %v", fake.Calls())
	}
}
