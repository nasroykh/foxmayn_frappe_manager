package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// wtRig is a Service with a source bench "src" that mounts the checkout
// <root>/src/my_app, a fake git and docker, and a Clone stand-in that makes a
// plain restored bench (apps/my_app a real directory).
type wtRig struct {
	s        *Service
	fake     *fakeexec.Fake
	root     string
	repo     string // canonical
	wt       string // the default worktree path for feat/x
	snapshot string
}

func (r wtRig) benchDir(name string) string { return filepath.Join(r.root, "benches", name) }

func newWTRig(t *testing.T, extra ...fakeexec.Rule) wtRig {
	t.Helper()
	root, err := canonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	t.Setenv("FFM_SEEDS_DIR", filepath.Join(root, "seeds"))

	repo := filepath.Join(root, "src", "my_app")
	if err := os.MkdirAll(filepath.Join(repo, "my_app"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := "app_name = \"my_app\"\n"
	if err := os.WriteFile(filepath.Join(repo, "my_app", "hooks.py"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	r := wtRig{root: root, repo: repo, wt: filepath.Join(root, "src", "my_app.worktrees", "feat-x"), snapshot: filepath.Join(root, "compose.yml")}

	rules := append(extra,
		fakeexec.Rule{Match: "rev-parse --path-format=absolute --show-toplevel", Stdout: repo + "\n"},
		fakeexec.Rule{Match: "--git-common-dir", Stdout: filepath.Join(repo, ".git") + "\n"},
		fakeexec.Rule{Match: "show-ref --verify", Exit: 1},
		fakeexec.Rule{Match: "worktree add", Write: map[string]string{filepath.Join(r.wt, "my_app", "hooks.py"): hooks}},
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
		fakeexec.Rule{Match: " up -d", Copy: map[string]string{filepath.Join(r.benchDir("src-feat-x"), "docker-compose.yml"): r.snapshot}},
	)
	r.fake = fakeexec.Install(t, rules...)
	r.s = New(false)

	r.addBench(t, state.Bench{Name: "src", ProjectApp: "my_app", ProjectAppPath: repo})

	old := worktreeClone
	worktreeClone = func(s *Service, in CloneInput, pw ProgressWriter) error {
		_ = execx.Command("docker", "clone-marker", in.Source, in.Target).Run()
		return r.addBench(t, state.Bench{Name: in.Target, Apps: []string{"my_app"}})
	}
	t.Cleanup(func() { worktreeClone = old })
	return r
}

// addBench registers a dev bench with its directories; fields in b win.
func (r wtRig) addBench(t *testing.T, b state.Bench) error {
	t.Helper()
	b.Dir = r.benchDir(b.Name)
	b.Mode, b.SiteName, b.Bind = "dev", b.Name+".localhost", state.BindLoopback
	b.AdminPassword, b.DBPassword = "admin-pass-1", "ffm123456"
	b.WebPort, b.SocketIOPort = 18800+len(b.Name), 19800+len(b.Name)
	fb := filepath.Join(b.Dir, "workspace", "frappe-bench")
	if err := os.MkdirAll(filepath.Join(fb, "apps", "my_app"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(fb, "apps", "my_app", "own-clone.py"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(fb, "sites"), 0o755)
	os.WriteFile(filepath.Join(fb, "sites", "apps.txt"), []byte("frappe\nmy_app\n"), 0o644)
	os.WriteFile(filepath.Join(b.Dir, "docker-compose.yml"), []byte("stale: true\n"), 0o644)
	return r.s.AddBench(b)
}

// noDestructiveGit fails the test when a git call could discard work or
// change the user's checkout beyond what a worktree add does.
func noDestructiveGit(t *testing.T, calls []string, allowForce bool) {
	t.Helper()
	for _, c := range calls {
		if !strings.HasPrefix(c, "git ") {
			continue
		}
		for _, bad := range []string{" reset", " checkout", " clean", " branch -D", " -D ", " rm ", " stash"} {
			if strings.Contains(c, bad) {
				t.Errorf("destructive git call: %s", c)
			}
		}
		if !allowForce && strings.Contains(c, "--force") {
			t.Errorf("git call with --force: %s", c)
		}
	}
}

func TestWorktreeAddOrderAndRecord(t *testing.T) {
	r := newWTRig(t)
	res, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", From: "src"}, DiscardProgress{})
	if err != nil {
		t.Fatalf("WorktreeAdd: %v\ncalls:\n%s", err, strings.Join(r.fake.Calls(), "\n"))
	}
	calls := r.fake.Calls()
	clone := indexOfCall(calls, "clone-marker src src-feat-x")
	add := indexOfCall(calls, "git -C "+r.repo+" worktree add -b feat/x "+r.wt)
	if clone < 0 || add < 0 || clone > add {
		t.Errorf("the clone must come before git worktree add: clone %d, add %d\n%s", clone, add, strings.Join(calls, "\n"))
	}
	noDestructiveGit(t, calls, false)

	if res.Bench != "src-feat-x" || res.Path != r.wt || res.URL != "http://feat-x.src.localhost" || !res.CreatedBranch {
		t.Errorf("result = %+v", res)
	}
	rec, err := r.s.GetBench("src-feat-x")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ProjectApp != "my_app" || rec.ProjectAppPath != r.wt || rec.WorktreeOf != "src" ||
		rec.WorktreeBranch != "feat/x" || !rec.WorktreeCreatedBranch {
		t.Errorf("record = %+v", rec)
	}
	if len(rec.DomainAliases) != 1 || rec.DomainAliases[0] != "feat-x.src.localhost" {
		t.Errorf("aliases = %v", rec.DomainAliases)
	}
	if len(rec.Apps) != 0 {
		t.Errorf("the mounted app stayed in Apps: %v", rec.Apps)
	}
	// ffm's own copy of the app is gone; an empty mountpoint is left.
	ents, err := os.ReadDir(filepath.Join(r.benchDir("src-feat-x"), "workspace", "frappe-bench", "apps", "my_app"))
	if err != nil || len(ents) != 0 {
		t.Errorf("mountpoint: %v %v", ents, err)
	}
	// The source bench is untouched.
	if src, _ := r.s.GetBench("src"); src.WorktreeOf != "" || len(src.DomainAliases) != 0 {
		t.Errorf("source changed: %+v", src)
	}
	compose, _ := os.ReadFile(r.snapshot)
	for _, want := range []string{"source: \"" + r.wt + "\"", "target: /workspace/frappe-bench/apps/my_app", "Host(`feat-x.src.localhost`)", "Host(`src-feat-x.localhost`)"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("compose lacks %q:\n%s", want, compose)
		}
	}
	req := indexOfCall(calls, "setup requirements --python 'my_app'")
	mig := indexOfCall(calls, "migrate")
	bld := indexOfCall(calls, "bench build --app 'my_app'")
	if req < 0 || mig < req || bld < mig {
		t.Errorf("order: requirements %d, migrate %d, build %d", req, mig, bld)
	}
	if r.fake.Called("install-app") {
		t.Error("the app was installed again; the restored database has it")
	}
}

func TestWorktreeAddExistingBranchIsNotRecreated(t *testing.T) {
	r := newWTRig(t, fakeexec.Rule{Match: "show-ref --verify --quiet refs/heads/feat/x"})
	// The rule above wins over the rig's (it is first): the branch exists.
	res, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", From: "src"}, DiscardProgress{})
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if res.CreatedBranch {
		t.Error("an existing branch was recorded as created by ffm")
	}
	if !r.fake.Called("worktree add "+r.wt+" feat/x") || r.fake.Called("worktree add -b") {
		t.Errorf("calls:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if rec, _ := r.s.GetBench("src-feat-x"); rec.WorktreeCreatedBranch {
		t.Error("record says ffm created the branch")
	}
	if _, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", From: "src", Base: "main", Name: "other"}, DiscardProgress{}); err == nil {
		t.Error("--base with an existing branch was accepted")
	}
}

func TestWorktreeAddGitFailureRemovesTheBenchOnly(t *testing.T) {
	r := newWTRig(t, fakeexec.Rule{Match: "worktree add", Exit: 128})
	_, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", From: "src"}, DiscardProgress{})
	if err == nil {
		t.Fatal("no error")
	}
	if _, gerr := r.s.GetBench("src-feat-x"); gerr == nil {
		t.Error("the bench was kept")
	}
	if _, serr := os.Stat(r.benchDir("src-feat-x")); serr == nil {
		t.Error("the bench directory was kept")
	}
	if r.fake.Called("worktree remove") || r.fake.Called("branch -") {
		t.Errorf("git cleanup ran:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	noDestructiveGit(t, r.fake.Calls(), false)
}

func TestWorktreeAddAttachFailureKeepsTheWorktree(t *testing.T) {
	r := newWTRig(t, fakeexec.Rule{Match: "setup requirements --node", Exit: 1})
	_, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", From: "src"}, DiscardProgress{})
	if err == nil {
		t.Fatal("no error")
	}
	hint := "git -C '" + r.repo + "' worktree remove '" + r.wt + "'"
	if !strings.Contains(err.Error(), hint) || !strings.Contains(err.Error(), "branch -d 'feat/x'") {
		t.Errorf("the error does not name the cleanup commands:\n%v", err)
	}
	if _, gerr := r.s.GetBench("src-feat-x"); gerr == nil {
		t.Error("the bench was kept")
	}
	if _, serr := os.Stat(filepath.Join(r.wt, "my_app", "hooks.py")); serr != nil {
		t.Errorf("the worktree was touched: %v", serr)
	}
	for _, c := range r.fake.Calls() {
		if strings.HasPrefix(c, "git ") && (strings.Contains(c, "worktree remove") || strings.Contains(c, "branch -")) {
			t.Errorf("ffm ran a git cleanup itself: %s", c)
		}
	}
	noDestructiveGit(t, r.fake.Calls(), false)
}

func TestWorktreeAddRefusals(t *testing.T) {
	r := newWTRig(t)
	cases := map[string]WorktreeAddInput{
		"unknown source":        {Branch: "feat/x", From: "nope"},
		"option-like branch":    {Branch: "-bad", From: "src"},
		"empty branch":          {Branch: " ", From: "src"},
		"reserved bench name":   {Branch: "feat/x", From: "src", Name: "list"},
		"path inside the repo":  {Branch: "feat/x", From: "src", Path: filepath.Join(r.repo, "wt")},
		"path in the benches":   {Branch: "feat/x", From: "src", Path: filepath.Join(r.root, "benches", "wt")},
		"path is the repo":      {Branch: "feat/x", From: "src", Path: r.repo},
		"path above the repo":   {Branch: "feat/x", From: "src", Path: filepath.Join(r.root, "src")},
		"empty branch slug":     {Branch: "///", From: "src"},
		"base is an option":     {Branch: "feat/x", From: "src", Base: "--force"},
		"source is the mounted": {Branch: "feat/x", From: "src", Name: "src"},
	}
	for name, in := range cases {
		if _, err := r.s.WorktreeAdd(in, DiscardProgress{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if r.fake.Called("clone-marker") || r.fake.Called("worktree add") {
		t.Errorf("work started before a refusal:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}

	// An existing path is never reused.
	if err := os.MkdirAll(r.wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", From: "src"}, DiscardProgress{}); err == nil {
		t.Error("an existing worktree path was accepted")
	}
	// A bench with no mount, and a worktree bench as source, are refused.
	r.addBench(t, state.Bench{Name: "plain"})
	if _, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/y", From: "plain"}, DiscardProgress{}); err == nil {
		t.Error("a bench without a mount was accepted")
	}
	r.addBench(t, state.Bench{Name: "child", ProjectApp: "my_app", ProjectAppPath: filepath.Join(r.root, "w"), WorktreeOf: "src"})
	if _, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/y", From: "child"}, DiscardProgress{}); err == nil {
		t.Error("a worktree bench as source was accepted")
	}
}

func TestAttachRefusesMountedBenchAndSymlinkedApp(t *testing.T) {
	r := newWTRig(t)
	other := filepath.Join(r.root, "other-checkout")
	os.MkdirAll(filepath.Join(other, "my_app"), 0o755)
	os.WriteFile(filepath.Join(other, "my_app", "hooks.py"), []byte("app_name = \"my_app\"\n"), 0o644)

	// A bench that already mounts something.
	if err := r.s.attachProjectMount("src", "my_app", other, worktreeLink{Of: "x", Branch: "b"}, DiscardProgress{}); err == nil {
		t.Error("a bench with a mount was attached to")
	}
	if rec, _ := r.s.GetBench("src"); rec.WorktreeOf != "" {
		t.Errorf("record changed: %+v", rec)
	}

	// apps/<app> a symlink: its target is the user's, and survives.
	precious := filepath.Join(r.root, "precious")
	os.MkdirAll(precious, 0o755)
	os.WriteFile(filepath.Join(precious, "work.py"), []byte("keep"), 0o644)
	r.addBench(t, state.Bench{Name: "linked"})
	appDir := filepath.Join(r.benchDir("linked"), "workspace", "frappe-bench", "apps", "my_app")
	os.RemoveAll(appDir)
	if err := os.Symlink(precious, appDir); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := r.s.attachProjectMount("linked", "my_app", other, worktreeLink{Of: "src", Branch: "b"}, DiscardProgress{}); err == nil {
		t.Error("a symlinked apps/<app> was accepted")
	}
	if raw, err := os.ReadFile(filepath.Join(precious, "work.py")); err != nil || string(raw) != "keep" {
		t.Errorf("the symlink's target was touched: %q %v", raw, err)
	}
	if rec, _ := r.s.GetBench("linked"); rec.ProjectApp != "" {
		t.Errorf("record changed: %+v", rec)
	}
}

// wtBench registers the worktree bench WorktreeAdd would have made.
func (r wtRig) wtBench(t *testing.T, created bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(r.wt, "my_app"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.addBench(t, state.Bench{Name: "src-feat-x", ProjectApp: "my_app", ProjectAppPath: r.wt, WorktreeOf: "src",
		WorktreeBranch: "feat/x", WorktreeCreatedBranch: created, DomainAliases: []string{"feat-x.src.localhost"}})
}

func TestWorktreeRemoveRefusedByGitKeepsEverything(t *testing.T) {
	r := newWTRig(t, fakeexec.Rule{Match: "worktree remove", Exit: 128})
	r.wtBench(t, true)
	_, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: "feat/x", NoBackup: true, DeleteBranch: true}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if _, gerr := r.s.GetBench("src-feat-x"); gerr != nil {
		t.Errorf("the bench was deleted: %v", gerr)
	}
	if _, serr := os.Stat(r.wt); serr != nil {
		t.Errorf("the worktree is gone: %v", serr)
	}
	if r.fake.Called("branch -d") || r.fake.Called("compose") {
		t.Errorf("work continued after the refusal:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	noDestructiveGit(t, r.fake.Calls(), false)
}

func TestWorktreeRemoveDirtyIsRefusedBeforeAnyBackup(t *testing.T) {
	r := newWTRig(t, fakeexec.Rule{Match: "status --porcelain", Stdout: " M my_app/hooks.py\n?? scratch.txt\n"})
	r.wtBench(t, true)
	_, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: r.wt}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("err = %v", err)
	}
	if r.fake.Called("worktree remove") || r.fake.Called("compose") {
		t.Errorf("calls after the refusal:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if _, gerr := r.s.GetBench("src-feat-x"); gerr != nil {
		t.Error("the bench was deleted")
	}
	// --force goes to git, and only then.
	if _, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: r.wt, Force: true, NoBackup: true}, DiscardProgress{}); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
	if !r.fake.Called("git -C " + r.repo + " worktree remove --force " + r.wt) {
		t.Errorf("--force did not reach git:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	noDestructiveGit(t, r.fake.Calls(), true)
	if _, gerr := r.s.GetBench("src-feat-x"); gerr == nil {
		t.Error("the bench was kept")
	}
}

func TestWorktreeRemoveNeverForcesByDefault(t *testing.T) {
	r := newWTRig(t)
	r.wtBench(t, true)
	res, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: "src-feat-x", NoBackup: true}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.WorktreeRemoved || res.BranchDeleted {
		t.Errorf("res = %+v", res)
	}
	if !r.fake.Called("git -C " + r.repo + " worktree remove " + r.wt) {
		t.Errorf("no worktree remove:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if r.fake.Called("branch") {
		t.Error("a branch was touched without --delete-branch")
	}
	noDestructiveGit(t, r.fake.Calls(), false)
	if _, gerr := r.s.GetBench("src-feat-x"); gerr == nil {
		t.Error("the bench was kept")
	}
}

func TestWorktreeRemoveDeletesOnlyABranchFfmCreated(t *testing.T) {
	for _, created := range []bool{true, false} {
		r := newWTRig(t)
		r.wtBench(t, created)
		res, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: "feat/x", NoBackup: true, DeleteBranch: true}, DiscardProgress{})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.fake.Called("git -C " + r.repo + " branch -d feat/x"); got != created || res.BranchDeleted != created {
			t.Errorf("created=%v: branch -d called=%v deleted=%v\n%s", created, got, res.BranchDeleted, strings.Join(r.fake.Calls(), "\n"))
		}
		noDestructiveGit(t, r.fake.Calls(), false)
	}

	// An unmerged branch (git refuses -d) is a warning; the removal stands.
	r := newWTRig(t, fakeexec.Rule{Match: "branch -d", Exit: 1})
	r.wtBench(t, true)
	res, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: "feat/x", NoBackup: true, DeleteBranch: true}, DiscardProgress{})
	if err != nil || res.BranchDeleted || !res.WorktreeRemoved {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestWorktreeRemoveKeepWorktreeAndGonePath(t *testing.T) {
	r := newWTRig(t)
	r.wtBench(t, true)
	res, err := r.s.WorktreeRemove(WorktreeRemoveInput{Target: "src-feat-x", NoBackup: true, KeepWorktree: true, DeleteBranch: true}, DiscardProgress{})
	if err != nil || res.WorktreeRemoved || res.BranchDeleted {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if r.fake.Called("git ") {
		t.Errorf("git ran with --keep-worktree:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	if _, serr := os.Stat(r.wt); serr != nil {
		t.Error("the worktree was removed")
	}

	// The path is already gone: nothing for git to remove.
	r2 := newWTRig(t)
	r2.wtBench(t, false)
	os.RemoveAll(r2.wt)
	if _, err := r2.s.WorktreeRemove(WorktreeRemoveInput{Target: "src-feat-x", NoBackup: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if r2.fake.Called("worktree remove") {
		t.Error("git was asked to remove a missing worktree")
	}
}

func TestWorktreeRemoveUnknownTargetChangesNothing(t *testing.T) {
	r := newWTRig(t)
	r.wtBench(t, true)
	for _, in := range []WorktreeRemoveInput{
		{Target: filepath.Join(r.root, "elsewhere"), PathOnly: true},
		{Target: "src", PathOnly: false},       // a bench, but not a worktree bench
		{Target: "src-feat-x", PathOnly: true}, // a name where a path is required
		{Target: r.repo, PathOnly: true},       // the main checkout is never a target
	} {
		if _, err := r.s.WorktreeRemove(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), ErrWorktreeUnknown.Error()) {
			t.Errorf("%+v: err = %v", in, err)
		}
	}
	if len(r.fake.Calls()) != 0 {
		t.Errorf("commands ran:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
}

func TestWorktreeList(t *testing.T) {
	r := newWTRig(t)
	r.wtBench(t, true)
	got, err := r.s.WorktreeList()
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	w := got[0]
	if w.Bench != "src-feat-x" || w.Source != "src" || w.Branch != "feat/x" || w.Path != r.wt || !w.PathExists || w.URL != "http://feat-x.src.localhost" {
		t.Errorf("%+v", w)
	}
}

func TestWorktreeSourceIsTheBenchOfTheMainCheckout(t *testing.T) {
	r := newWTRig(t)
	b, err := r.s.WorktreeSource(r.repo)
	if err != nil || b.Name != "src" {
		t.Fatalf("source = %q, %v", b.Name, err)
	}
	// With no --from the add resolves it the same way.
	if res, err := r.s.WorktreeAdd(WorktreeAddInput{Branch: "feat/x", Cwd: r.repo}, DiscardProgress{}); err != nil || res.Source != "src" {
		t.Errorf("add: %+v, %v", res, err)
	}

	other := newWTRig(t, fakeexec.Rule{Match: "--git-common-dir"})
	// (the empty answer is no repository)
	if _, err := other.s.WorktreeSource("."); err == nil {
		t.Error("no repository was accepted")
	}
	elsewhere := filepath.Join(other.root, "elsewhere")
	os.MkdirAll(elsewhere, 0o755)
	third := newWTRig(t, fakeexec.Rule{Match: "--git-common-dir", Stdout: filepath.Join(elsewhere, ".git") + "\n"})
	if _, err := third.s.WorktreeSource("."); err == nil || !strings.Contains(err.Error(), "ffm up") {
		t.Errorf("a repository without a bench: %v", err)
	}
}

func TestLocalhostAliasesNeedNoLAN(t *testing.T) {
	for alias, want := range map[string]bool{
		"feat-x.src.localhost": false, "FEAT.localhost": false,
		"erp.internal": true, "localhost.example.com": true, "notlocalhost": true,
	} {
		if got := aliasesNeedLAN([]string{alias}); got != want {
			t.Errorf("aliasesNeedLAN(%q) = %v, want %v", alias, got, want)
		}
	}
	if aliasesNeedLAN(nil) {
		t.Error("no aliases need the LAN")
	}

	// ffm domain add on a loopback dev bench.
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, b := newTestBench(t, state.Bench{
		Name: "dom", Mode: "dev", SiteName: "dom.localhost", WebPort: 8000, SocketIOPort: 9000,
		AdminPassword: "s3cret-admin", DBPassword: "s3cret-db1", Bind: state.BindLoopback,
	})
	if err := s.DomainAdd(DomainInput{Name: "dom", Domain: "erp.internal"}, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "LAN") {
		t.Errorf("erp.internal on a loopback dev bench: %v", err)
	}
	if err := s.DomainAdd(DomainInput{Name: "dom", Domain: "feat.dom.localhost"}, DiscardProgress{}); err != nil {
		t.Fatalf("a .localhost alias was refused: %v", err)
	}
	if rec, _ := s.GetBench("dom"); len(rec.DomainAliases) != 1 || rec.DomainAliases[0] != "feat.dom.localhost" {
		t.Errorf("aliases = %v", rec.DomainAliases)
	}
	if raw, _ := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml")); !strings.Contains(string(raw), "feat.dom.localhost") {
		t.Errorf("compose lacks the alias:\n%s", raw)
	}

	// ffm reconcile --loopback keeps working with only .localhost aliases.
	if err := checkReconcile(state.Bench{Mode: "dev", Bind: state.BindLoopback, DomainAliases: []string{"a.b.localhost"}, AdminPassword: "x"}, ReconcileInput{}); err != nil {
		t.Errorf("reconcile refused a .localhost alias: %v", err)
	}
	if err := checkReconcile(state.Bench{Mode: "dev", Bind: state.BindLoopback, DomainAliases: []string{"erp.internal"}, AdminPassword: "x"}, ReconcileInput{}); err == nil {
		t.Error("reconcile accepted erp.internal on loopback")
	}
}

func TestCreateAliasRules(t *testing.T) {
	r := newMountRig(t, "als")
	in := r.createInput("als")
	in.ProjectApp, in.ProjectAppPath = "", ""
	in.DomainAliases = []string{"erp.internal"}
	if err := r.s.Create(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "--lan") {
		t.Fatalf("erp.internal on loopback: %v", err)
	}
	if len(r.fake.Calls()) != 0 {
		t.Errorf("work started before the refusal: %v", r.fake.Calls())
	}
	in.DomainAliases = []string{"feat.als.localhost"}
	if err := r.s.Create(in, DiscardProgress{}); err != nil {
		t.Fatalf("a .localhost alias on loopback: %v", err)
	}
}
