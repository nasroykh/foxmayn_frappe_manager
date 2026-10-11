package manager

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/project"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// mountRig is a Service with isolated directories and a fake docker, plus a
// project checkout holding the app my_app.
type mountRig struct {
	s        *Service
	fake     *fakeexec.Fake
	root     string
	checkout string // canonical
	snaps    string // compose-file snapshots taken by the fake
}

func (r mountRig) benchDir(name string) string { return filepath.Join(r.root, "benches", name) }
func (r mountRig) frappeBench(name string) string {
	return filepath.Join(r.benchDir(name), "workspace", "frappe-bench")
}

// newMountRig installs a fake docker that, like the real one, leaves
// apps/ and sites/apps.txt behind after bench init (`run --rm`) and the seed
// copy (`cp`), and snapshots docker-compose.yml at `run --rm` and `up -d`.
func newMountRig(t *testing.T, name string, extra ...fakeexec.Rule) mountRig {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	t.Setenv("FFM_SEEDS_DIR", filepath.Join(root, "seeds"))
	old := createWebWait
	createWebWait = 0
	t.Cleanup(func() { createWebWait = old })

	checkout, err := canonicalDir(filepath.Join(root, "src", "my_app"))
	if err != nil { // does not exist yet
		checkout = filepath.Join(root, "src", "my_app")
	}
	hooks := filepath.Join(checkout, "my_app", "hooks.py")
	if err := os.MkdirAll(filepath.Dir(hooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte("app_name = \"my_app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if checkout, err = canonicalDir(checkout); err != nil {
		t.Fatal(err)
	}

	r := mountRig{root: root, checkout: checkout, snaps: filepath.Join(root, "snaps")}
	os.MkdirAll(r.snaps, 0o755)
	fb := r.frappeBench(name)
	compose := filepath.Join(r.benchDir(name), "docker-compose.yml")
	rules := append(extra,
		fakeexec.Rule{Match: "SELECT 1", Stdout: "1\n"},
		fakeexec.Rule{Match: " run --rm ", Mkdir: []string{filepath.Join(fb, "apps")},
			Write: map[string]string{filepath.Join(fb, "sites", "apps.txt"): "frappe"},
			Copy:  map[string]string{compose: filepath.Join(r.snaps, "run.yml")}},
		fakeexec.Rule{Match: "cp -a", Mkdir: []string{filepath.Join(fb, "apps")},
			Write: map[string]string{filepath.Join(fb, "sites", "apps.txt"): "frappe"}},
		fakeexec.Rule{Match: " up -d", Copy: map[string]string{compose: filepath.Join(r.snaps, "up.yml")}},
	)
	r.fake = fakeexec.Install(t, rules...)
	r.s = New(false)
	return r
}

func (r mountRig) snapshot(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.snaps, name))
	if err != nil {
		t.Fatalf("no %s snapshot: %v\ncalls:\n%s", name, err, strings.Join(r.fake.Calls(), "\n"))
	}
	return string(raw)
}

func (r mountRig) createInput(name string) CreateInput {
	return CreateInput{Name: name, Mode: "dev", AdminPassword: "admin", DBPassword: "ffm123456", NoSeed: true,
		FixedWebPort: 18800, FixedSocketIOPort: 19800,
		ProjectApp: "my_app", ProjectAppPath: r.checkout}
}

func indexOfCall(calls []string, parts ...string) int {
	for i, c := range calls {
		all := true
		for _, p := range parts {
			if !strings.Contains(c, p) {
				all = false
			}
		}
		if all {
			return i
		}
	}
	return -1
}

func TestCreateMountsTheCheckoutAfterBenchInit(t *testing.T) {
	r := newMountRig(t, "mnt")
	if err := r.s.Create(r.createInput("mnt"), DiscardProgress{}); err != nil {
		t.Fatalf("Create: %v\ncalls:\n%s", err, strings.Join(r.fake.Calls(), "\n"))
	}

	// bench init ran through `compose run`, which uses the same service
	// definition: its rm -rf must not be able to reach the checkout.
	if run := r.snapshot(t, "run.yml"); strings.Contains(run, "create_host_path") || strings.Contains(run, r.checkout) {
		t.Errorf("the mount was in the compose file during bench init:\n%s", run)
	}
	up := r.snapshot(t, "up.yml")
	for _, want := range []string{"source: \"" + r.checkout + "\"", "target: /workspace/frappe-bench/apps/my_app", "create_host_path: false"} {
		if !strings.Contains(up, want) {
			t.Errorf("compose at `up` lacks %q:\n%s", want, up)
		}
	}

	// The mountpoint is made by ffm, as the host user.
	st, err := os.Stat(filepath.Join(r.frappeBench("mnt"), "apps", "my_app"))
	if err != nil || !st.IsDir() || st.Mode().Perm()&0o700 != 0o700 {
		t.Errorf("mountpoint: %v %v", st, err)
	}
	if apps, _ := os.ReadFile(filepath.Join(r.frappeBench("mnt"), "sites", "apps.txt")); !strings.Contains(string(apps), "my_app") {
		t.Errorf("apps.txt = %q", apps)
	}

	calls := r.fake.Calls()
	py := indexOfCall(calls, "bench setup requirements --python 'my_app'")
	node := indexOfCall(calls, "bench setup requirements --node 'my_app'")
	inst := indexOfCall(calls, "install-app 'my_app'")
	build := indexOfCall(calls, "&& bench build")
	if py < 0 || node < py || inst < node || build < inst {
		t.Errorf("order: --python %d, --node %d, install-app %d, build %d; calls:\n%s", py, node, inst, build, strings.Join(calls, "\n"))
	}
	if r.fake.Called("get-app") {
		t.Error("the mounted app was fetched with get-app")
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "cp ") {
			t.Errorf("a seed was captured from a bench with a mounted checkout: %s", c)
		}
	}

	rec, err := r.s.GetBench("mnt")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ProjectApp != "my_app" || rec.ProjectAppPath != r.checkout || len(rec.Apps) != 0 {
		t.Errorf("record: app %q path %q apps %v", rec.ProjectApp, rec.ProjectAppPath, rec.Apps)
	}
}

func TestCreateWithSeedBuildsTheMountedApp(t *testing.T) {
	r := newMountRig(t, "seeded")
	// A fresh seed for the inputs ffm create would use.
	key := seedKeyFor("", bench.DefaultFrappeBranch, bench.ToolchainFor(bench.DefaultFrappeBranch), 0, 0, nil, bench.DefaultFrappeBranch)
	dir := filepath.Join(os.Getenv("FFM_SEEDS_DIR"), key.id())
	os.MkdirAll(filepath.Join(dir, "bench", "apps", "frappe"), 0o755)
	meta := `{"key":` + mustJSON(t, key) + `,"created_at":"` + time.Now().UTC().Format(time.RFC3339) + `","from_bench":"x"}`
	os.WriteFile(filepath.Join(dir, "seed.json"), []byte(meta), 0o644)

	in := r.createInput("seeded")
	in.NoSeed = false
	if err := r.s.Create(in, DiscardProgress{}); err != nil {
		t.Fatalf("Create: %v\ncalls:\n%s", err, strings.Join(r.fake.Calls(), "\n"))
	}
	calls := r.fake.Calls()
	if indexOfCall(calls, "cp -a") < 0 {
		t.Fatalf("the seed was not used; calls:\n%s", strings.Join(calls, "\n"))
	}
	if run := r.snapshot(t, "run.yml"); strings.Contains(run, "create_host_path") {
		t.Errorf("the mount was in the compose file during the seed's skills copy:\n%s", run)
	}
	inst := indexOfCall(calls, "install-app 'my_app'")
	app := indexOfCall(calls, "bench build --app 'my_app'")
	if inst < 0 || app < inst {
		t.Errorf("install-app %d, build --app %d; calls:\n%s", inst, app, strings.Join(calls, "\n"))
	}
	// Only the seed copy itself is a cp; nothing is captured afterwards.
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "cp ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d cp calls, want 1 (the seed copy)", n)
	}
}

func TestCreateRefusesBadMounts(t *testing.T) {
	r := newMountRig(t, "bad")
	prod := r.createInput("bad")
	prod.Mode, prod.Domain, prod.AdminPassword, prod.NoSSL = "prod", "erp.example.com", "Str0ng-pass", true
	restore := r.createInput("bad")
	restore.SkipAppInstall = true
	clash := r.createInput("bad")
	clash.Apps = []string{"https://github.com/acme/my-app.git@main"}
	gone := r.createInput("bad")
	gone.ProjectAppPath = filepath.Join(r.root, "nowhere")
	dollar := r.createInput("bad")
	dollar.ProjectAppPath = "/tmp/$HOME/x"

	for name, in := range map[string]CreateInput{"prod": prod, "restore": restore, "collision": clash, "missing checkout": gone, "$ in path": dollar} {
		if err := r.s.Create(in, DiscardProgress{}); err == nil {
			t.Errorf("%s: Create succeeded", name)
		}
	}
	if len(r.fake.Calls()) != 0 {
		t.Errorf("work started before the refusal:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if _, err := os.Stat(r.benchDir("bad")); err == nil {
		t.Error("a bench directory was created")
	}
}

func TestCreateWithoutAMountRendersNone(t *testing.T) {
	r := newMountRig(t, "plain")
	in := r.createInput("plain")
	in.ProjectApp, in.ProjectAppPath = "", ""
	if err := r.s.Create(in, DiscardProgress{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if up := r.snapshot(t, "up.yml"); strings.Contains(up, "create_host_path") {
		t.Errorf("mount rendered for a plain bench:\n%s", up)
	}
	if r.fake.Called("install-app 'my_app'") {
		t.Error("a plain bench registered a project app")
	}
}

// stderrProgress captures Stderr so a test can read warnings.
type stderrProgress struct {
	DiscardProgress
	buf bytes.Buffer
}

func (p *stderrProgress) Stderr() io.Writer { return &p.buf }

func TestUpMountsTheCheckoutOfTheFile(t *testing.T) {
	r := newMountRig(t, "myproj")
	readOSRelease = func() (string, error) { return "5.15.167.4-microsoft-standard-WSL2", nil }
	t.Cleanup(func() { readOSRelease = func() (string, error) { return "", os.ErrNotExist } })

	ff := filepath.Join(r.checkout, "ffm.yaml")
	if err := os.WriteFile(ff, []byte("version: 1\nname: myproj\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := project.Load(ff)
	if err != nil {
		t.Fatal(err)
	}
	pw := &stderrProgress{}
	res, err := r.s.Up(UpInput{File: f}, pw)
	if err != nil {
		t.Fatalf("Up: %v\ncalls:\n%s", err, strings.Join(r.fake.Calls(), "\n"))
	}
	if res.Action != "created" || res.Mounted != "my_app" || res.MountedPath != r.checkout {
		t.Errorf("result %+v", res)
	}
	if !strings.Contains(r.snapshot(t, "up.yml"), r.checkout) {
		t.Error("the checkout is not mounted")
	}
	rec, _ := r.s.GetBench("myproj")
	if rec.ProjectFile != ff || rec.ProjectApp != "my_app" {
		t.Errorf("record %+v", rec)
	}
	// The checkout is under the temp dir, not /mnt/<drive>: no WSL warning.
	if strings.Contains(pw.buf.String(), "9p") {
		t.Errorf("unexpected warning: %s", pw.buf.String())
	}
}

func TestUpConfigOnlyFileMountsNothing(t *testing.T) {
	r := newMountRig(t, "cfg")
	dir := filepath.Join(r.root, "docsrepo")
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	ff := filepath.Join(dir, "ffm.yaml")
	os.WriteFile(ff, []byte("version: 1\nname: cfg\n"), 0o644)
	f, err := project.Load(ff)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.s.Up(UpInput{File: f}, DiscardProgress{})
	if err != nil || res.Mounted != "" {
		t.Fatalf("Up: %+v %v", res, err)
	}
	if strings.Contains(r.snapshot(t, "up.yml"), "create_host_path") {
		t.Error("a mount was rendered")
	}
}

func TestProjectCheckoutResolvesSymlinks(t *testing.T) {
	r := newMountRig(t, "sym")
	link := filepath.Join(r.root, "link")
	if err := os.Symlink(r.checkout, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	ff := filepath.Join(link, "ffm.yaml")
	os.WriteFile(ff, []byte("version: 1\nname: sym\n"), 0o644)
	f, err := project.Load(ff)
	if err != nil {
		t.Fatal(err)
	}
	app, path, err := projectCheckout(f)
	if err != nil || app != "my_app" || path != r.checkout {
		t.Errorf("got %q %q %v, want my_app %q", app, path, err, r.checkout)
	}
}

func TestWSLMountWarning(t *testing.T) {
	defer func(old func() (string, error)) { readOSRelease = old }(readOSRelease)
	for _, tt := range []struct {
		release, path string
		warn          bool
	}{
		{"5.15.167.4-microsoft-standard-WSL2", "/mnt/c/Users/me/app", true},
		{"5.15.90.1-Microsoft", "/mnt/d/src/app", true},
		{"5.15.167.4-microsoft-standard-WSL2", "/mnt/c", true},
		{"5.15.167.4-microsoft-standard-WSL2", "/home/me/app", false},
		{"5.15.167.4-microsoft-standard-WSL2", "/mnt/wsl/x", false},
		{"5.15.167.4-microsoft-standard-WSL2", "/mnt/cc/x", false},
		{"6.8.0-generic", "/mnt/c/app", false},
	} {
		readOSRelease = func() (string, error) { return tt.release, nil }
		got := wslMountWarning(tt.path)
		if (got != "") != tt.warn {
			t.Errorf("%s on %s: warning %q, want warn=%v", tt.release, tt.path, got, tt.warn)
		}
		if tt.warn && (!strings.Contains(got, "9p") || !strings.Contains(got, "Linux filesystem")) {
			t.Errorf("warning does not explain itself: %s", got)
		}
	}
	readOSRelease = func() (string, error) { return "", os.ErrNotExist }
	if got := wslMountWarning("/mnt/c/app"); got != "" {
		t.Errorf("warned without a readable osrelease: %s", got)
	}
}

// mountedBench registers a dev bench with a project mount, in the rig's dirs.
func mountedBench(t *testing.T, r mountRig, name string, mutate func(*state.Bench)) state.Bench {
	t.Helper()
	b := state.Bench{Name: name, Mode: "dev", SiteName: name + ".localhost", WebPort: 8000, SocketIOPort: 9000,
		AdminPassword: "s3cret-admin", DBPassword: "s3cret-db1", Bind: state.BindLoopback,
		ProjectApp: "my_app", ProjectAppPath: r.checkout}
	if mutate != nil {
		mutate(&b)
	}
	b.Dir = r.benchDir(name)
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, "docker-compose.yml"), []byte("stale: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sites := filepath.Join(r.frappeBench(name), "sites")
	os.MkdirAll(sites, 0o755)
	os.WriteFile(filepath.Join(sites, "apps.txt"), []byte("frappe\nmy_app\n"), 0o644)
	if err := r.s.AddBench(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReconcileKeepsTheMountAndProdNeverGetsOne(t *testing.T) {
	r := newMountRig(t, "keep", fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	b := mountedBench(t, r, "keep", nil)
	if err := r.s.Reconcile(ReconcileInput{Name: "keep"}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	compose, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if !strings.Contains(string(compose), "target: /workspace/frappe-bench/apps/my_app") {
		t.Errorf("reconcile dropped the mount:\n%s", compose)
	}
	if !r.fake.Called(" up -d") {
		t.Error("containers were not updated")
	}

	// A record that somehow carries the fields in prod still renders none.
	prod := mountedBench(t, r, "prodb", func(b *state.Bench) {
		b.Mode, b.Domain, b.SiteName, b.TLSMode = "prod", "erp.example.com", "erp.example.com", state.TLSNone
		b.ProxyHost = "http://erp.example.com"
	})
	data, err := r.s.composeDataFor(prod)
	if err != nil {
		t.Fatal(err)
	}
	out, err := bench.RenderCompose(data)
	if err != nil {
		t.Fatal(err)
	}
	if data.ProjectApp != "" || strings.Contains(string(out), "my_app") {
		t.Errorf("prod got the mount:\n%s", out)
	}
}

func TestReconcileRefusesWhenTheCheckoutMoved(t *testing.T) {
	r := newMountRig(t, "moved", fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	mountedBench(t, r, "moved", nil)
	os.RemoveAll(r.checkout)
	err := r.s.Reconcile(ReconcileInput{Name: "moved"}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), r.checkout) {
		t.Fatalf("err = %v", err)
	}
	if r.fake.Called(" up -d") {
		t.Error("compose up ran with the checkout missing")
	}
}

func TestStartRefusesBeforeComposeUpWhenTheCheckoutMoved(t *testing.T) {
	r := newMountRig(t, "gone")
	mountedBench(t, r, "gone", nil)
	if err := os.Remove(filepath.Join(r.checkout, "my_app", "hooks.py")); err != nil {
		t.Fatal(err)
	}
	err := r.s.Start("gone", DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), r.checkout) || !strings.Contains(err.Error(), "hooks.py") {
		t.Fatalf("err = %v", err)
	}
	if len(r.fake.Calls()) != 0 {
		t.Errorf("docker was called:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
}

func TestRecreateKeepsTheMountAndChecksItFirst(t *testing.T) {
	r := newMountRig(t, "again")
	b := mountedBench(t, r, "again", func(b *state.Bench) { b.WebPort, b.SocketIOPort = 18820, 19820 })

	// A moved checkout fails before anything is torn down.
	os.Rename(r.checkout, r.checkout+".moved")
	if err := r.s.Recreate(RecreateInput{Name: "again", NoBackup: true}, DiscardProgress{}); err == nil {
		t.Fatal("Recreate succeeded without the checkout")
	}
	if len(r.fake.Calls()) != 0 {
		t.Errorf("work started before the refusal:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if _, err := os.Stat(b.Dir); err != nil {
		t.Errorf("the bench directory was removed: %v", err)
	}
	os.Rename(r.checkout+".moved", r.checkout)

	if err := r.s.Recreate(RecreateInput{Name: "again", NoBackup: true}, DiscardProgress{}); err != nil {
		t.Fatalf("Recreate: %v\ncalls:\n%s", err, strings.Join(r.fake.Calls(), "\n"))
	}
	if run := r.snapshot(t, "run.yml"); strings.Contains(run, "create_host_path") {
		t.Errorf("the mount was in the compose file during bench init:\n%s", run)
	}
	if !strings.Contains(r.snapshot(t, "up.yml"), "apps/my_app") {
		t.Error("recreate lost the mount")
	}
	rec, _ := r.s.GetBench("again")
	if rec.ProjectApp != "my_app" || rec.ProjectAppPath != r.checkout || len(rec.Apps) != 0 {
		t.Errorf("record %+v", rec)
	}
	// The checkout is untouched by the teardown.
	if _, err := os.Stat(filepath.Join(r.checkout, "my_app", "hooks.py")); err != nil {
		t.Errorf("the checkout was damaged: %v", err)
	}
}

func TestDeleteLeavesTheCheckout(t *testing.T) {
	r := newMountRig(t, "del")
	b := mountedBench(t, r, "del", nil)
	// A host-side view of the mountpoint is an empty directory.
	os.MkdirAll(filepath.Join(r.frappeBench("del"), "apps", "my_app"), 0o755)
	if err := r.s.Delete(DeleteInput{Name: "del", NoBackup: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Dir); err == nil {
		t.Error("the bench directory is still there")
	}
	if _, err := os.Stat(filepath.Join(r.checkout, "my_app", "hooks.py")); err != nil {
		t.Errorf("the checkout was damaged: %v", err)
	}
}

func TestAppRemoveRefusesTheMountedApp(t *testing.T) {
	r := newMountRig(t, "rm")
	mountedBench(t, r, "rm", nil)
	_, err := r.s.AppRemove(AppRemoveInput{Name: "rm", App: "my_app"}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), r.checkout) || !strings.Contains(err.Error(), "delete") {
		t.Fatalf("err = %v", err)
	}
	if r.fake.Called("remove-app") || r.fake.Called("uninstall-app") || len(r.fake.Calls()) != 0 {
		t.Errorf("a command ran:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	// --keep-code is refused too: the app is the mount.
	if _, err := r.s.AppRemove(AppRemoveInput{Name: "rm", App: "my_app", KeepCode: true}, DiscardProgress{}); err == nil {
		t.Error("--keep-code removal accepted")
	}
}

func TestAppAddRefusesTheMountedApp(t *testing.T) {
	r := newMountRig(t, "add", fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	mountedBench(t, r, "add", nil)
	for _, spec := range []string{"my_app", "my-app@develop", "https://github.com/acme/my_app.git"} {
		if _, err := r.s.AppAdd(AppAddInput{Name: "add", Spec: spec}, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "mounted") {
			t.Errorf("%s: err = %v", spec, err)
		}
	}
	if r.fake.Called("get-app") {
		t.Error("get-app ran")
	}
}

func TestListAppsMarksTheMountedApp(t *testing.T) {
	out := `FFM_APPS=[{"name":"frappe","installed":true},{"name":"my_app","installed":true}]` + "\n"
	r := newMountRig(t, "ls", fakeexec.Rule{Match: " ps ", Stdout: psRunning}, fakeexec.Rule{Match: "FFM_APPS", Stdout: out})
	mountedBench(t, r, "ls", nil)
	apps, err := r.s.ListApps("ls")
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].Mounted || !apps[1].Mounted {
		t.Errorf("apps %+v", apps)
	}
}

func TestUpdateLeavesTheMountedAppAlone(t *testing.T) {
	dirty := "version-16\nlocal999\nupstr111\nmy_app/hooks.py\n"
	clean := "version-16\nabc1234567\nabc1234567\n"
	r := newMountRig(t, "upd",
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
		// Were the mounted app probed it would be dirty and ahead of upstream.
		fakeexec.Rule{Match: "apps/'my_app'", Stdout: dirty},
		fakeexec.Rule{Match: "git rev-parse --abbrev-ref HEAD", Stdout: clean})
	b := mountedBench(t, r, "upd", func(b *state.Bench) { b.FrappeBranch, b.WebPort = "version-16", pingServer(t) })
	rt := filepath.Join(r.frappeBench("upd"), "apps", "frappe", "realtime")
	os.MkdirAll(filepath.Join(rt, "middlewares"), 0o755)
	os.WriteFile(filepath.Join(rt, "middlewares", "authenticate.js"), []byte("// stub\n"), 0o644)
	os.WriteFile(filepath.Join(rt, "utils.js"), []byte("// stub\n"), 0o644)

	plan, err := r.s.planUpdate(b, UpdateInput{})
	if err != nil {
		t.Fatalf("planUpdate: %v", err)
	}
	if plan.Mounted != "my_app" || strings.Join(plan.Pull, ",") != "frappe" || len(plan.Apps) != 1 {
		t.Errorf("plan %+v", plan)
	}
	if r.fake.Called("apps/'my_app'") {
		t.Errorf("the mounted app's git was read:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if _, err := r.s.planUpdate(b, UpdateInput{Apps: []string{"my_app"}}); err == nil || !strings.Contains(err.Error(), "mounted") {
		t.Errorf("--apps my_app: %v", err)
	}

	// A branch switch moves frappe only, and a rollback checks out only what the plan holds.
	sw, err := r.s.planUpdate(b, UpdateInput{ToBranch: "version-17"})
	if err != nil || strings.Join(sw.Switch, ",") != "frappe" {
		t.Errorf("switch plan %+v %v", sw, err)
	}
	if err := r.s.applyUpdate(b, UpdateInput{}, plan, io.Discard, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, b, "before-update-x", r.s.clock(), false)
	if err := r.s.rollbackUpdate(b, plan, "before-update-x", nil, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.fake.Calls() {
		if strings.Contains(c, "my_app") && (strings.Contains(c, "git ") || strings.Contains(c, "--apps") || strings.Contains(c, "find ")) {
			t.Errorf("a git or prune command touched the mounted app: %s", c)
		}
	}
	if !r.fake.Called("bench update --pull --reset --no-backup --apps 'frappe'") {
		t.Errorf("pull not limited to frappe:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	// It stays in requirements, migrate and build (bench-wide commands).
	if !r.fake.Called("bench setup requirements") || !r.fake.Called("migrate") || !r.fake.Called("bench build") {
		t.Error("requirements, migrate and build must still cover the whole bench")
	}
}

func TestRestoreNeverMounts(t *testing.T) {
	m := devManifest()
	m.Bench.ProjectApp, m.Bench.ProjectAppPath, m.Bench.ProjectFile = "my_app", "/home/someone/my_app", "/home/someone/my_app/ffm.yaml"
	m.Secrets.AdminPassword = "s3cret-admin"
	m.Bench.DBPassword = "s3cret-db1"
	in, err := restoreCreateInput(m, restorePlan{FrappeBranch: bench.DefaultFrappeBranch}, "restored", RestoreInput{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if in.ProjectApp != "" || in.ProjectAppPath != "" || in.ProjectFile != "" {
		t.Errorf("restore carries the mount: %+v", in)
	}
	// And Create refuses one if a caller tried.
	in.ProjectApp, in.ProjectAppPath = "my_app", "/x"
	r := newMountRig(t, "restored")
	if err := r.s.Create(in, DiscardProgress{}); err == nil {
		t.Error("Create accepted a mount on a restore")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDirtyMountedAppIsVendored(t *testing.T) {
	apps := func(dirty bool) []AppInfo {
		return []AppInfo{
			{Name: "frappe", Remote: "https://github.com/frappe/frappe", Commit: "a", RemoteBranch: "version-16", Dirty: true},
			{Name: "my_app", Remote: "https://github.com/acme/my_app", Commit: "b", RemoteBranch: "main", Dirty: dirty},
		}
	}
	candidates := map[string]bool{"frappe": true, "my_app": true}

	a := apps(true)
	got := planAppSources(a, candidates, nil, "my_app", "/tmp/stage")
	if len(got) != 1 || a[got[0].index].Name != "my_app" || a[got[0].index].SourceReason != VendorDirtyMounted || !a[got[0].index].Archived() {
		t.Fatalf("dirty mounted app: %+v %+v", got, a)
	}
	if describeVendorReason(VendorDirtyMounted) == VendorDirtyMounted {
		t.Error("no description for dirty-mounted")
	}
	if a[0].Source != AppSourceGit {
		t.Errorf("another dirty app was vendored: %+v", a[0])
	}

	// Clean: a clone is enough. Not mounted: dirty alone changes nothing.
	if got := planAppSources(apps(false), candidates, nil, "my_app", "/tmp/stage"); len(got) != 0 {
		t.Errorf("clean mounted app vendored: %+v", got)
	}
	if got := planAppSources(apps(true), candidates, nil, "", "/tmp/stage"); len(got) != 0 {
		t.Errorf("dirty unmounted app vendored: %+v", got)
	}
}
