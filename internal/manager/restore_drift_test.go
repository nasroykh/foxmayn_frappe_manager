package manager

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	commitA = "aefe8bb11a9b9062a6a704999b3ad4105e20e96b"
	commitB = "b5520a568052425f2ba83ee8aa6ae6c4c46176c6"
	commitC = "5d84239000000000000000000000000000000000"
)

func TestGitAuthHint(t *testing.T) {
	cases := []struct {
		name, out string
		token     bool
		want      string // substring; "" = no hint
	}{
		{"no token, https", "fatal: could not read Username for 'https://github.com': No such device or address", false, "FFM_GITHUB_TOKEN"},
		{"no token names gh", "fatal: could not read Username for 'https://github.com'", false, `"$(gh auth token)"`},
		{"repo not found", "remote: Repository not found.\nfatal: repository 'https://github.com/x/y.git/' not found", false, "--github-token"},
		{"token refused", "fatal: Authentication failed for 'https://github.com/x/y.git/'", true, "token given"},
		{"ssh", "git@github.com: Permission denied (publickey).", false, "--ssh-agent"},
		{"ssh with token still ssh advice", "Host key verification failed.", true, "--ssh-agent"},
		{"unrelated failure", "error: pathspec 'main' did not match any file(s) known to git", false, ""},
		{"empty", "", true, ""},
	}
	for _, c := range cases {
		got := gitAuthHint(c.out, c.token)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: unexpected hint %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: hint %q lacks %q", c.name, got, c.want)
		}
	}
	if gitAuthSuffix("nothing to see", false) != "" {
		t.Error("gitAuthSuffix added a line for unrelated output")
	}
}

func TestBenchInitFailureCarriesAuthHint(t *testing.T) {
	// bench init exits 1 with git's message buried in bench's traceback; the
	// error used to be just "bench init: exit status 1".
	fakeexec.Install(t, fakeexec.Rule{
		Match:  "bench init",
		Stdout: "git ls-remote ... kb_frappe.git main\nfatal: could not read Username for 'https://github.com': No such device or address\nInvalidRemoteException: Invalid frappe path\nDo you want to rollback these changes? [y/N]: Aborted!\n",
		Exit:   1,
	})
	runner := bench.NewRunner("t", t.TempDir(), false)
	var dump bytes.Buffer
	runner.ErrOut = &dump
	s := &Service{}
	err := s.benchInit(runner, "prod", "main", "https://github.com/acme/frappe.git", "x", bench.ToolchainFor("main"),
		"", t.TempDir(), &BufferProgress{})
	if err == nil {
		t.Fatal("benchInit succeeded on a failed bench init")
	}
	if !strings.Contains(err.Error(), "FFM_GITHUB_TOKEN") {
		t.Errorf("error has no token hint: %v", err)
	}
	if !strings.Contains(dump.String(), "could not read Username") {
		t.Errorf("the failure dump no longer reaches the original ErrOut: %q", dump.String())
	}
	if runner.ErrOut != &dump {
		t.Error("benchInit left its tee in runner.ErrOut")
	}
}

func TestArchivedInitBranch(t *testing.T) {
	cases := []struct {
		b    state.Bench
		want string
	}{
		{state.Bench{FrappeBranch: "version-15"}, "version-15"},
		{state.Bench{FrappeBranch: "version-15", FrappeRepo: "https://github.com/acme/frappe.git@main"}, "main"},
		{state.Bench{FrappeBranch: "version-15", FrappeRepo: "https://github.com/acme/frappe.git"}, "version-15"},
		{state.Bench{FrappeBranch: "version-16", FrappeRepo: "git@github.com:acme/frappe.git@develop"}, "develop"},
	}
	for _, c := range cases {
		if got := archivedInitBranch(c.b); got != c.want {
			t.Errorf("archivedInitBranch(%+v) = %q, want %q", c.b, got, c.want)
		}
	}
}

func TestRestoreKeepsToolchainOfForkOnBranch(t *testing.T) {
	// --frappe-branch version-15 --frappe-repo <fork>@main --python 3.12: the
	// plan clones main, the record says version-15, and the archived toolchain
	// was dropped because the two did not match.
	m := Manifest{
		Header:  Header{Mode: "dev", BenchName: "src"},
		Bench:   state.Bench{FrappeBranch: "version-15", FrappeRepo: "https://github.com/acme/frappe.git@main", Python: "3.12", Node: "22"},
		Secrets: Secrets{AdminPassword: "admin"},
		Apps:    []AppInfo{{Name: "frappe", Remote: "https://github.com/acme/frappe.git", RemoteBranch: "main", Commit: commitA, Source: AppSourceGit}},
	}
	plan := planRestoreApps(m, nil)
	if plan.FrappeBranch != "main" {
		t.Fatalf("plan branch = %q, want main", plan.FrappeBranch)
	}
	in, err := restoreCreateInput(m, plan, "dst", RestoreInput{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if in.Python != "3.12" || in.Node != "22" {
		t.Errorf("toolchain = %s/%s, want the archived 3.12/22", in.Python, in.Node)
	}

	// An override onto another branch still gets that branch's default.
	plan = planRestoreApps(m, map[string]string{"frappe": "https://github.com/acme/frappe.git@version-16"})
	in, err = restoreCreateInput(m, plan, "dst", RestoreInput{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if in.Python != "" || in.Node != "" {
		t.Errorf("override kept the archived toolchain %s/%s", in.Python, in.Node)
	}
}

func TestCommitDrift(t *testing.T) {
	apps := []AppInfo{
		{Name: "frappe", Commit: commitA},
		{Name: "kb_pro", Commit: commitB},
		{Name: "raven", Commit: commitA[:7]}, // abbreviated in the record
		{Name: "nocommit"},
		{Name: "gone", Commit: commitB},
	}
	heads := map[string]string{"frappe": commitC, "kb_pro": commitB, "raven": commitA, "nocommit": commitA}
	r := commitDrift(apps, heads)
	if r.Checked != 3 {
		t.Errorf("Checked = %d, want 3 (no commit and missing app skipped)", r.Checked)
	}
	if len(r.Drift) != 1 || r.Drift[0].Name != "frappe" || r.Drift[0].Restored != commitC {
		t.Errorf("Drift = %+v, want frappe only", r.Drift)
	}
	if !strings.Contains(r.summaryLine(), "1 of 3") {
		t.Errorf("summary = %q", r.summaryLine())
	}
	if (driftReport{Checked: 2}).summaryLine() != "at the commits the archive recorded" {
		t.Error("clean report summary wrong")
	}
	if (driftReport{}).summaryLine() != "" {
		t.Error("nothing checked must print nothing")
	}
}

// pinPlan is a plan with frappe and one app cloned from git, plus one archived.
func pinPlan() restorePlan {
	frappe := AppInfo{Name: "frappe", Commit: commitA, Remote: "https://github.com/acme/frappe.git", RemoteBranch: "main", Source: AppSourceGit}
	return restorePlan{
		FrappeBranch: "main",
		frappeInfo:   &frappe,
		Apps: []restoreApp{
			{Name: "kb_pro", Spec: "https://github.com/acme/kb_pro@dev", Origin: originRemote,
				Info: &AppInfo{Name: "kb_pro", Commit: commitB, Source: AppSourceGit}},
			{Name: "achats", Origin: originArchive,
				Info: &AppInfo{Name: "achats", Commit: commitC, Source: AppSourceArchive, Member: "ffm-backup/apps/achats.tar.gz"}},
		},
	}
}

func TestCheckDriftWarnsBeforeMigrate(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{
		Match:  "rev-parse HEAD",
		Stdout: "frappe " + commitC + "\nkb_pro " + commitB + "\n",
	})
	runner := bench.NewRunner("t", t.TempDir(), false)
	pw := &BufferProgress{}
	r := checkDrift(runner, pinPlan(), false, pw)
	if r.Checked != 2 || len(r.Drift) != 1 || r.Drift[0].Name != "frappe" {
		t.Fatalf("report = %+v", r)
	}
	out := strings.Join(pw.Lines, "\n")
	for _, want := range []string{"1 app(s) are not at the commit", "frappe", shortCommit(commitA), shortCommit(commitC), "--pin-apps"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "achats") {
		t.Error("an archived app was compared")
	}
}

func TestCheckDriftReadsOnlyOnce(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "rev-parse HEAD", Stdout: "frappe " + commitA + "\nkb_pro " + commitB + "\n"})
	runner := bench.NewRunner("t", t.TempDir(), false)
	pw := &BufferProgress{}
	r := checkDrift(runner, pinPlan(), true, pw)
	if len(r.Drift) != 0 || r.Checked != 2 {
		t.Fatalf("report = %+v", r)
	}
	if len(pw.Lines) != 0 {
		t.Errorf("clean restore printed: %v", pw.Lines)
	}
	n := 0
	for _, c := range fake.Calls() {
		if strings.Contains(c, "rev-parse HEAD") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("rev-parse execs = %d, want 1", n)
	}
}

func TestPinAppsCommands(t *testing.T) {
	fake := fakeexec.Install(t)
	runner := bench.NewRunner("t", t.TempDir(), false)
	pw := &BufferProgress{}
	if err := (&Service{}).pinApps(runner, pinPlan(), false, pw); err != nil {
		t.Fatal(err)
	}
	for _, app := range []struct{ dir, commit string }{{"frappe", commitA}, {"kb_pro", commitB}} {
		if !fake.Called("apps/"+app.dir+" &&", `--depth 1 "$remote" `+app.commit, "checkout --force --quiet "+app.commit) {
			t.Errorf("%s was not fetched shallow and checked out at %s:\n%s", app.dir, app.commit, strings.Join(fake.Calls(), "\n"))
		}
	}
	if fake.Called("apps/achats") {
		t.Error("an archived app was pinned")
	}
	if !fake.Called("setup requirements --python", "frappe", "kb_pro") ||
		!fake.Called("setup requirements --node", "frappe", "kb_pro") {
		t.Error("requirements were not re-run in two halves for the pinned apps")
	}
}

func TestPinAppsSkipsAppNotOnBench(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "test -d /workspace/frappe-bench/apps/kb_pro/.git", Exit: 1})
	runner := bench.NewRunner("t", t.TempDir(), false)
	pw := &BufferProgress{}
	if err := (&Service{}).pinApps(runner, pinPlan(), false, pw); err != nil {
		t.Fatal(err)
	}
	if fake.Called("checkout --force --quiet " + commitB) {
		t.Error("pinned an app that is not on the bench")
	}
	if !strings.Contains(strings.Join(pw.Lines, "\n"), `app "kb_pro" is not on this bench`) {
		t.Errorf("no warning: %v", pw.Lines)
	}
}

func TestPinAppsFetchFailureCarriesAuthHint(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{
		Match:  "checkout --force",
		Stdout: "fatal: could not read Username for 'https://github.com': No such device or address",
		Exit:   128,
	})
	runner := bench.NewRunner("t", t.TempDir(), false)
	err := (&Service{}).pinApps(runner, pinPlan(), false, &BufferProgress{})
	if err == nil || !strings.Contains(err.Error(), "pin frappe") || !strings.Contains(err.Error(), "FFM_GITHUB_TOKEN") {
		t.Errorf("err = %v", err)
	}
}
