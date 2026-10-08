package manager

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func updateBench(t *testing.T, rules ...fakeexec.Rule) (*Service, state.Bench, *fakeexec.Fake) {
	t.Helper()
	fake := fakeexec.Install(t, append(rules, fakeexec.Rule{Match: " ps ", Stdout: psRunning})...)
	s, b := newTestBench(t, state.Bench{Name: "up", Mode: "dev", SiteName: "up.localhost", WebPort: pingServer(t), SocketIOPort: 9000,
		FrappeBranch: "version-15", Python: "3.12", Node: "22", Apps: []string{"erpnext", "https://github.com/o/custom@main"}})
	sites := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites")
	os.MkdirAll(sites, 0o755)
	os.WriteFile(filepath.Join(sites, "apps.txt"), []byte("frappe\nerpnext\ncustom\n"), 0o644)
	rt := filepath.Join(b.Dir, "workspace", "frappe-bench", "apps", "frappe", "realtime")
	os.MkdirAll(filepath.Join(rt, "middlewares"), 0o755)
	os.WriteFile(filepath.Join(rt, "middlewares", "authenticate.js"), []byte("// stub\n"), 0o644)
	os.WriteFile(filepath.Join(rt, "utils.js"), []byte("// stub\n"), 0o644)
	return s, b, fake
}

func TestPlanUpdate(t *testing.T) {
	// frappe and erpnext on version-15; the fake answers the same for each.
	s, b, _ := updateBench(t, fakeexec.Rule{Match: "git rev-parse --abbrev-ref HEAD", Stdout: "version-15\nabc1234567\nabc1234567\n"})
	plan, err := s.planUpdate(b, UpdateInput{ToBranch: "version-16"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Apps) != 3 || len(plan.Pull) != 3 || strings.Join(plan.Switch, ",") != "frappe,erpnext,custom" {
		t.Errorf("plan %+v", plan)
	}
	if plan.Toolchain.Python != "3.14" || !plan.Rebuild {
		t.Errorf("toolchain %+v rebuild %v", plan.Toolchain, plan.Rebuild)
	}
	if _, err := s.planUpdate(b, UpdateInput{Apps: []string{"nope"}}); err == nil {
		t.Error("an unknown app was accepted")
	}
}

func TestPlanUpdateRefusesUncommittedWork(t *testing.T) {
	s, b, _ := updateBench(t, fakeexec.Rule{Match: "git rev-parse --abbrev-ref HEAD", Stdout: "version-15\nabc\nabc\nerpnext/hooks.py\n"})
	if _, err := s.planUpdate(b, UpdateInput{}); !errors.Is(err, errDirtyApps) {
		t.Errorf("err = %v, want errDirtyApps", err)
	}
}

func TestApplyUpdateOrder(t *testing.T) {
	s, b, fake := updateBench(t)
	var out bytes.Buffer
	plan := updatePlan{Apps: []appState{{"frappe", "version-15", "a1"}, {"erpnext", "version-15", "b2"}}, Pull: []string{"erpnext"}}
	if err := s.applyUpdate(b, UpdateInput{}, plan, &out, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls(), "\n")
	order := []string{"git checkout -- realtime/middlewares/authenticate.js", "bench update --pull --reset --no-backup --apps 'erpnext'",
		"bench setup requirements", "bench --site 'up.localhost' clear-cache", "bench --site 'up.localhost' migrate", "bench build", "[h]oncho start"}
	last := -1
	for _, step := range order {
		i := strings.Index(calls, step)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order; calls:\n%s", step, calls)
		}
		last = i
	}
}

func TestApplyUpdateSwitchStaysShallow(t *testing.T) {
	s, b, fake := updateBench(t)
	plan := updatePlan{Apps: []appState{{"frappe", "version-15", "a1"}}, Switch: []string{"frappe"},
		Toolchain: bench.ToolchainFor("version-15")}
	if err := s.applyUpdate(b, UpdateInput{ToBranch: "version-15"}, plan, io.Discard, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls(), "\n")
	for _, want := range []string{
		"git fetch -q --depth 1 upstream '+refs/heads/version-15:refs/remotes/upstream/version-15'",
		"git checkout -q -B 'version-15' 'refs/remotes/upstream/version-15'",
		"git branch -q --set-upstream-to 'upstream/version-15'",
		// bench setup env keeps an existing virtualenv: it must move aside.
		"rm -rf env.ffm-prev && mv env env.ffm-prev && bench setup env --python",
		// A module the branch dropped must not survive as a __pycache__-only
		// folder, which imports as a namespace package and breaks migrate.
		"-type d -name __pycache__ -prune -exec rm -rf {} + && find 'apps/frappe' -type d -empty",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("switch lacks %q; calls:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "switch-to-branch") || strings.Contains(calls, "unshallow") {
		t.Errorf("switch went through bench switch-to-branch:\n%s", calls)
	}
}

func TestApplyUpdateFailureAndRollback(t *testing.T) {
	s, b, fake := updateBench(t, fakeexec.Rule{Match: " migrate", Exit: 1})
	plan := updatePlan{Apps: []appState{{"frappe", "version-15", "a1"}, {"erpnext", "version-15", "b2"}}, Pull: []string{"frappe", "erpnext"}}
	if err := s.applyUpdate(b, UpdateInput{}, plan, nil, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "Migrating") {
		t.Fatalf("err = %v", err)
	}
	writeSnapshot(t, b, "before-update-x", s.clock(), false)
	if err := s.rollbackUpdate(b, plan, "before-update-x", nil, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apps/'frappe' && git checkout -q -B 'version-15' 'a1'", "apps/'erpnext' && git checkout -q -B 'version-15' 'b2'",
		"restore /workspace/.ffm-snapshots/before-update-x/database.sql.gz"} {
		if !fake.Called(want) {
			t.Errorf("rollback lacks %q; calls:\n%s", want, strings.Join(fake.Calls(), "\n"))
		}
	}
}

func TestWaitForSiteUnderMaintenance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	b := state.Bench{Mode: "dev", WebPort: p}
	if err := waitForSite(b, true, nil); err != nil {
		t.Errorf("503 under maintenance must count as up: %v", err)
	}
	old := devServerWait
	devServerWait = 0
	defer func() { devServerWait = old }()
	if err := waitForSite(b, false, nil); err == nil {
		t.Error("503 without maintenance counted as up")
	}
}

func TestPlanUpdateLocalCommitsAndLocalApps(t *testing.T) {
	s, b, _ := updateBench(t, fakeexec.Rule{Match: "git rev-parse --abbrev-ref HEAD", Stdout: "version-15\nlocal999\nupstr111\n"})
	if _, err := s.planUpdate(b, UpdateInput{}); !errors.Is(err, errLocalCommits) {
		t.Errorf("err = %v, want errLocalCommits", err)
	}
	s, b, _ = updateBench(t, fakeexec.Rule{Match: "git rev-parse --abbrev-ref HEAD", Stdout: "version-15\nabc\nnone\n"})
	plan, err := s.planUpdate(b, UpdateInput{})
	if err != nil || len(plan.Pull) != 0 || len(plan.Local) != 3 {
		t.Errorf("apps without upstream must be skipped: %+v %v", plan, err)
	}
	if _, err := s.planUpdate(b, UpdateInput{Apps: []string{"custom"}}); err == nil {
		t.Error("an app without upstream was accepted by --apps")
	}
}

func TestPlanUpdateNonGitApp(t *testing.T) {
	s, b, _ := updateBench(t, fakeexec.Rule{Match: "git rev-parse --abbrev-ref HEAD", Stdout: "nogit\n\nnone\n"})
	plan, err := s.planUpdate(b, UpdateInput{})
	if err != nil || len(plan.Local) != 3 || plan.Apps[0].Commit != "" {
		t.Errorf("non-git apps: %+v %v", plan, err)
	}
}
