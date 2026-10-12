package manager

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// statusRig registers dev benches alpha (running), beta (partial: only the
// database runs), gamma (stopped: its containers exited) and delta (stopped,
// no containers), plus epsilon whose compose file is gone. The fake docker
// answers both the single `docker ps -a` and the per-bench `compose ps`.
func statusRig(t *testing.T) (*Service, *fakeexec.Fake) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "ps -a --filter", Stdout: "" +
			"ffm-alpha frappe running\nffm-alpha mariadb running\n" +
			"ffm-beta mariadb running\nffm-beta frappe exited\n" +
			"ffm-gamma frappe exited\nffm-gamma mariadb exited\n" +
			"some-other-project web running\n"},
		// compose ps lists running containers only.
		fakeexec.Rule{Match: "-p ffm-alpha ", Stdout: "frappe running\nmariadb running\n"},
		fakeexec.Rule{Match: "-p ffm-beta ", Stdout: "mariadb running\n"},
	)
	s := New(false)
	for _, name := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		dir := filepath.Join(root, "benches", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if name != "epsilon" {
			if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.AddBench(state.Bench{Name: name, Dir: dir, Mode: "dev", SiteName: name + ".localhost", WebPort: 8000}); err != nil {
			t.Fatal(err)
		}
	}
	return s, fake
}

func TestStatusAllIsOneDockerCallAndMatchesLiveStatus(t *testing.T) {
	s, fake := statusRig(t)
	got, err := s.StatusAll()
	if err != nil {
		t.Fatal(err)
	}
	if n := countCalls(fake, "docker ps -a"); n != 1 {
		t.Fatalf("StatusAll made %d docker ps calls for 5 benches, want 1", n)
	}
	if n := countCalls(fake, "docker compose"); n != 0 {
		t.Fatalf("StatusAll ran %d compose commands, want 0", n)
	}
	want := map[string]string{
		"alpha": StatusRunning, "beta": StatusPartial, "gamma": StatusStopped,
		"delta": StatusStopped, "epsilon": StatusUnknown,
	}
	benches, _ := s.LoadBenches()
	for _, b := range benches {
		if got[b.Name] != want[b.Name] {
			t.Errorf("StatusAll[%s] = %q, want %q", b.Name, got[b.Name], want[b.Name])
		}
		if live := s.LiveStatus(b); live != got[b.Name] && b.Name != "epsilon" {
			// epsilon: the fake succeeds without a compose file; real compose fails.
			t.Errorf("%s: StatusAll %q differs from LiveStatus %q", b.Name, got[b.Name], live)
		}
	}
}

func TestStatusAllDockerUnreachableIsUnknown(t *testing.T) {
	s, _ := statusRig(t)
	fakeexec.Install(t, fakeexec.Rule{Match: "docker", Exit: 1})
	got, err := s.StatusAll()
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range got {
		if st != StatusUnknown {
			t.Errorf("%s = %q with docker down, want unknown", name, st)
		}
	}
}

func TestStatusAllCacheTTL(t *testing.T) {
	s, fake := statusRig(t)
	now := time.Now()
	s.now = func() time.Time { return now }

	// No TTL: every call asks docker, so a CLI listing is never stale.
	s.StatusAll()
	s.StatusAll()
	if n := countCalls(fake, "docker ps -a"); n != 2 {
		t.Fatalf("without a TTL: %d docker calls, want 2", n)
	}

	s.StatusTTL = 2 * time.Second
	s.StatusAll()
	now = now.Add(time.Second)
	s.StatusAll()
	if n := countCalls(fake, "docker ps -a"); n != 3 {
		t.Fatalf("inside the TTL: %d docker calls, want 3 (one more)", n)
	}
	now = now.Add(2 * time.Second)
	s.StatusAll()
	if n := countCalls(fake, "docker ps -a"); n != 4 {
		t.Fatalf("after the TTL: %d docker calls, want 4", n)
	}

	s.InvalidateStatus()
	s.StatusAll()
	if n := countCalls(fake, "docker ps -a"); n != 5 {
		t.Fatalf("after InvalidateStatus: %d docker calls, want 5", n)
	}
}

// The home page needs the counts and the table: one status query and one
// proxy check, not 2N compose calls.
func TestDashboardOverviewViewsIsOneStatusQueryAndOneProxyCheck(t *testing.T) {
	s, fake := statusRig(t)
	stats, views, err := s.DashboardOverviewViews(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 5 || stats.TotalBenches != 5 || stats.RunningBenches != 1 || stats.StoppedBenches != 4 {
		t.Fatalf("stats = %+v, %d views", stats, len(views))
	}
	if n := countCalls(fake, "docker ps -a"); n != 1 {
		t.Errorf("%d docker ps calls, want 1", n)
	}
	if n := countCalls(fake, "docker inspect"); n != 1 {
		t.Errorf("%d proxy checks for 5 dev benches, want 1", n)
	}
	if n := countCalls(fake, "docker compose"); n != 0 {
		t.Errorf("%d compose calls, want 0", n)
	}
}
