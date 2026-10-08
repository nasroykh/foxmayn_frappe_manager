package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestWithoutApp(t *testing.T) {
	got := withoutApp([]string{"erpnext", "https://github.com/org/hrms.git@main", "git@github.com:o/my_app.git"}, "hrms", "version-16")
	if strings.Join(got, ",") != "erpnext,git@github.com:o/my_app.git" {
		t.Errorf("got %v", got)
	}
}

func TestAppAddRecordsTheAppGetAppAdded(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, b := newTestBench(t, state.Bench{Name: "aa", Mode: "dev", SiteName: "aa.localhost", WebPort: pingServer(t), SocketIOPort: 9000,
		FrappeBranch: "version-16", Apps: []string{"erpnext"}})
	sites := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites")
	os.MkdirAll(sites, 0o755)
	os.WriteFile(filepath.Join(sites, "apps.txt"), []byte("frappe\nerpnext\n"), 0o644)
	// The fake get-app cannot write apps.txt, so the name falls back to the
	// spec's; the real apps.txt diff is exercised live.
	app, err := s.AppAdd(AppAddInput{Name: "aa", Spec: "https://github.com/org/AchatsExtern@main"}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if app != "AchatsExtern" {
		t.Errorf("app = %q", app)
	}
	for _, want := range []string{"bench get-app --branch 'main' 'https://github.com/org/AchatsExtern'", "install-app 'AchatsExtern'", "bench build --app 'AchatsExtern'", "[h]oncho start"} {
		if !fake.Called(want) {
			t.Errorf("missing %q; calls:\n%s", want, strings.Join(fake.Calls(), "\n"))
		}
	}
	rec, _ := s.GetBench("aa")
	if strings.Join(rec.Apps, ",") != "erpnext,https://github.com/org/AchatsExtern@main" {
		t.Errorf("recorded apps %v", rec.Apps)
	}
}

func TestAppRemoveSnapshotsFirst(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, b := newTestBench(t, state.Bench{Name: "ar", Mode: "dev", SiteName: "ar.localhost", WebPort: pingServer(t), SocketIOPort: 9000,
		FrappeBranch: "version-16", Apps: []string{"erpnext", "hrms"}})
	s.now = func() time.Time { return time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC) }
	sites := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites")
	os.MkdirAll(sites, 0o755)
	os.WriteFile(filepath.Join(sites, "apps.txt"), []byte("frappe\nerpnext\nhrms\n"), 0o644)

	if _, err := s.AppRemove(AppRemoveInput{Name: "ar", App: "nope"}, DiscardProgress{}); err == nil {
		t.Error("an app the bench lacks was removed")
	}
	if _, err := s.AppRemove(AppRemoveInput{Name: "ar", App: "frappe"}, DiscardProgress{}); err == nil {
		t.Error("frappe was removed")
	}
	snap, err := s.AppRemove(AppRemoveInput{Name: "ar", App: "hrms"}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if snap != "before-remove-hrms-20261009-080000" {
		t.Errorf("snapshot %q", snap)
	}
	calls := strings.Join(fake.Calls(), "\n")
	iSnap, iUn := strings.Index(calls, "backup --verbose --compress"), strings.Index(calls, "uninstall-app 'hrms' --yes --no-backup --force")
	if iSnap < 0 || iUn < 0 || iSnap > iUn {
		t.Errorf("the snapshot must come before uninstall; calls:\n%s", calls)
	}
	if !fake.Called("bench remove-app 'hrms' --force") {
		t.Error("code not removed")
	}
	rec, _ := s.GetBench("ar")
	if strings.Join(rec.Apps, ",") != "erpnext" {
		t.Errorf("recorded apps %v", rec.Apps)
	}
}
