package manager

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
)

// bench clones every app with --origin upstream. Archives written before this
// was handled looked only for "origin" and so recorded no remote at all.
func TestApplyProbeReadsBenchUpstreamRemote(t *testing.T) {
	var info AppInfo
	applyProbe(&info, parseAppProbe(strings.Join([]string{
		"git=yes",
		"commit=84901e298ef85c6da277e9189ebff64acbe06e5d",
		"branch=version-15",
		"tag=",
		"remote_name=upstream",
		"remote=https://github.com/frappe/erpnext.git",
		"tracking=upstream/version-15",
		"containing=refs/remotes/upstream/HEAD refs/remotes/upstream/version-15",
	}, "\n")))

	if info.Remote != "https://github.com/frappe/erpnext.git" {
		t.Errorf("Remote = %q", info.Remote)
	}
	if info.RemoteBranch != "version-15" || info.Unpushed || info.NoGit {
		t.Errorf("RemoteBranch=%q Unpushed=%v NoGit=%v", info.RemoteBranch, info.Unpushed, info.NoGit)
	}
	if vendorReason(info, false) != "" {
		t.Errorf("a pushed clone was marked for archiving: %s", vendorReason(info, false))
	}
}

func TestApplyProbe(t *testing.T) {
	const sha = "84901e298ef85c6da277e9189ebff64acbe06e5d"
	tests := []struct {
		name       string
		probe      appProbe
		wantBranch string
		wantReason string
	}{
		{
			name:       "not a git repository",
			probe:      appProbe{git: false},
			wantReason: VendorNoGit,
		},
		{
			name:       "bench new-app: git, no remote",
			probe:      appProbe{git: true, commit: sha, branch: "develop"},
			wantReason: VendorNoRemote,
		},
		{
			name:       "git init with nothing committed",
			probe:      appProbe{git: true, remoteName: "upstream", remote: "https://github.com/acme/x"},
			wantReason: VendorNoCommit,
		},
		{
			name: "commit on no remote ref",
			probe: appProbe{git: true, commit: sha, branch: "develop", remoteName: "upstream",
				remote: "https://github.com/acme/x", tracking: "upstream/develop"},
			wantReason: VendorUnpushed,
		},
		{
			name: "commit only on ANOTHER remote does not count",
			probe: appProbe{git: true, commit: sha, branch: "main", remoteName: "upstream",
				remote: "https://github.com/acme/x", containing: []string{"refs/remotes/fork/main"}},
			wantReason: VendorUnpushed,
		},
		{
			name: "fetched from a local directory",
			probe: appProbe{git: true, commit: sha, branch: "main", remoteName: "upstream",
				remote: "/home/me/src/my_app", containing: []string{"refs/remotes/upstream/main"}},
			wantReason: VendorNoRemote,
		},
		{
			name: "local branch renamed: clone the remote branch that holds the commit",
			probe: appProbe{git: true, commit: sha, branch: "my-work", remoteName: "upstream",
				remote: "https://github.com/acme/x", containing: []string{"refs/remotes/upstream/main"}},
			wantBranch: "main",
		},
		{
			name: "tracking branch wins",
			probe: appProbe{git: true, commit: sha, branch: "a", remoteName: "upstream",
				remote: "https://github.com/acme/x", tracking: "upstream/b",
				containing: []string{"refs/remotes/upstream/a", "refs/remotes/upstream/b"}},
			wantBranch: "b",
		},
		{
			name: "checked out at a tag",
			probe: appProbe{git: true, commit: sha, branch: "HEAD", tag: "v15.2.0", remoteName: "upstream",
				remote: "https://github.com/acme/x", containing: []string{"refs/tags/v15.2.0"}},
			wantBranch: "v15.2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := AppInfo{Name: "x"}
			applyProbe(&info, tt.probe)
			if got := info.CloneBranch(); tt.wantReason == "" && got != tt.wantBranch {
				t.Errorf("CloneBranch = %q, want %q", got, tt.wantBranch)
			}
			if got := vendorReason(info, false); got != tt.wantReason {
				t.Errorf("vendorReason = %q, want %q", got, tt.wantReason)
			}
		})
	}
}

// The probe is a shell script run inside the container; this runs it against
// real repositories so the parsing above is checked against real git output.
func TestAppProbeScriptAgainstGit(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	probe := func(dir string) appProbe {
		t.Helper()
		out, err := exec.Command("bash", "-c", appProbeScript, "probe", dir).CombinedOutput()
		if err != nil {
			t.Fatalf("probe %s: %v\n%s", dir, err, out)
		}
		return parseAppProbe(string(out))
	}

	// A repository cloned the way bench clones.
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	run(src, "init", "-q", "-b", "version-15")
	os.WriteFile(filepath.Join(src, "f"), []byte("1"), 0o644)
	run(src, "add", "f")
	run(src, "commit", "-q", "-m", "one")
	apps := filepath.Join(root, "bench", "apps")
	os.MkdirAll(apps, 0o755)
	run(apps, "clone", "-q", "--branch", "version-15", "--origin", "upstream", src, "cloned")

	p := probe(filepath.Join(apps, "cloned"))
	if !p.git || p.remoteName != "upstream" || p.remote != src || p.tracking != "upstream/version-15" {
		t.Fatalf("cloned app probe = %+v", p)
	}
	if !reflect.DeepEqual(p.containing, []string{"refs/remotes/upstream/HEAD", "refs/remotes/upstream/version-15"}) &&
		!reflect.DeepEqual(p.containing, []string{"refs/remotes/upstream/version-15"}) {
		t.Fatalf("containing = %v", p.containing)
	}

	// A local commit on top is unpushed.
	os.WriteFile(filepath.Join(apps, "cloned", "g"), []byte("2"), 0o644)
	run(filepath.Join(apps, "cloned"), "add", "g")
	run(filepath.Join(apps, "cloned"), "commit", "-q", "-m", "two")
	if p := probe(filepath.Join(apps, "cloned")); len(p.containing) != 0 {
		t.Fatalf("unpushed commit found on %v", p.containing)
	}

	// A plain directory inside a bench that is itself under git is NOT a repo.
	run(filepath.Join(root, "bench"), "init", "-q")
	plain := filepath.Join(apps, "plain")
	os.MkdirAll(plain, 0o755)
	if p := probe(plain); p.git {
		t.Fatalf("a directory inside a repository was reported as one: %+v", p)
	}
}

func TestCloneableRemote(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"https://github.com/frappe/erpnext.git", "https://github.com/frappe/erpnext.git", true},
		{"git@github.com:acme/app.git", "git@github.com:acme/app.git", true},
		{"ssh://git@git.acme.dev:2222/acme/app.git", "ssh://git@git.acme.dev:2222/acme/app.git", true},
		// A token in the URL would be printed by the clone step; --github-token supplies it instead.
		{"https://x-access-token:ghp_secret@github.com/acme/app.git", "https://github.com/acme/app.git", true},
		{"/home/me/src/app", "", false},
		{"file:///home/me/src/app", "", false},
		{"../app", "", false},
		{"", "", false},
		{"https://github.com/acme/app$(id)", "", false},
	}
	for _, tt := range tests {
		got, ok := cloneableRemote(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("cloneableRemote(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPlanAppSources(t *testing.T) {
	apps := []AppInfo{
		{Name: "frappe", Remote: "https://github.com/frappe/frappe", Commit: "a", RemoteBranch: "version-15"},
		{Name: "nogit", NoGit: true},
		{Name: "localgit", Commit: "b"},
		{Name: "pushed", Remote: "https://github.com/acme/pushed", Commit: "c", RemoteBranch: "main"},
		{Name: "stray", NoGit: true}, // in apps/, but neither installed nor recorded
	}
	forced, err := resolveVendorRequest([]string{"pushed"}, []string{"frappe", "nogit", "localgit", "pushed", "stray"})
	if err != nil {
		t.Fatal(err)
	}
	vendored := planAppSources(apps, vendorCandidates([]string{"frappe", "nogit", "localgit"}, []string{"pushed"}, forced),
		forced, "/tmp/stage")

	var got []string
	for _, v := range vendored {
		got = append(got, apps[v.index].Name+":"+apps[v.index].SourceReason)
	}
	want := []string{"nogit:no-git", "localgit:no-remote", "pushed:requested"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vendored = %v, want %v", got, want)
	}
	if apps[0].Source != AppSourceGit || apps[4].Source != "" {
		t.Fatalf("frappe source %q, stray source %q", apps[0].Source, apps[4].Source)
	}
	if apps[1].Member != archive.Prefix+"/apps/nogit.tar.gz" || !apps[1].Archived() {
		t.Fatalf("nogit member = %q", apps[1].Member)
	}
}

func TestResolveVendorRequest(t *testing.T) {
	present := []string{"frappe", "a", "b"}
	all, err := resolveVendorRequest([]string{"all"}, present)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %v, %v", all, err)
	}
	if _, err := resolveVendorRequest([]string{"a", "nope"}, present); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown app accepted: %v", err)
	}
}

// Only an archive that needs the new reader demands it; everything else stays
// restorable by an ffm that predates app source.
func TestHeaderMinReaderVersion(t *testing.T) {
	m := devManifest()
	if h := NewHeader(m.Bench, "s", "", "", []string{TierCore}, m.Header.CreatedAt); h.MinReaderVersion != MinReaderVersion {
		t.Fatalf("plain archive min reader = %d", h.MinReaderVersion)
	}
	h := NewHeader(m.Bench, "s", "", "", []string{TierCore, TierAppSource}, m.Header.CreatedAt)
	if h.MinReaderVersion != MinReaderVersionAppSource || MinReaderVersionAppSource > SchemaVersion {
		t.Fatalf("app-source archive min reader = %d (schema %d)", h.MinReaderVersion, SchemaVersion)
	}
}

func TestCloneBranch(t *testing.T) {
	// Archives from before Source existed recorded only the local branch.
	if got := (AppInfo{Branch: "version-15"}).CloneBranch(); got != "version-15" {
		t.Errorf("legacy CloneBranch = %q", got)
	}
	if got := (AppInfo{Branch: "HEAD"}).CloneBranch(); got != "" {
		t.Errorf("legacy detached CloneBranch = %q", got)
	}
	// A newer archive never clones a local-only branch name.
	if got := (AppInfo{Source: AppSourceGit, Branch: "my-work"}).CloneBranch(); got != "" {
		t.Errorf("new CloneBranch = %q", got)
	}
}

// The archive from the original bug report: no bench record apps, no recorded
// remotes, private apps installed by hand.
func legacyPrivateManifest() Manifest {
	m := devManifest()
	m.Bench.Apps = nil
	m.Bench.FrappeBranch = "version-15"
	m.Site.InstalledApps = []string{"frappe", "kb_pro", "kb_compta"}
	m.Apps = []AppInfo{
		{Name: "frappe", Commit: "3c3a96a5db3333d7c7a6c50a90d2183edf605306", Branch: "main"},
		{Name: "kb_compta", Commit: "e0e7b4cffc69ea9547604cd0a9cb3a5531eba3c2", Branch: "erpnext"},
		{Name: "kb_pro", Commit: "84901e298ef85c6da277e9189ebff64acbe06e5d", Branch: "erpnext"},
	}
	return m
}

func TestPlanRestoreAppsOverrides(t *testing.T) {
	m := legacyPrivateManifest()
	overrides, err := parseAppOverrides([]string{
		"kb_pro=git@github.com:kb/kb_pro.git@erpnext",
		"frappe=https://github.com/kb/frappe@main",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := planRestoreApps(m, overrides)
	if plan.FrappeRepo != "https://github.com/kb/frappe" || plan.FrappeBranch != "main" || plan.FrappeOrigin != originOverride {
		t.Fatalf("frappe = %s@%s (%s)", plan.FrappeRepo, plan.FrappeBranch, plan.FrappeOrigin)
	}
	want := []string{"git@github.com:kb/kb_pro.git@erpnext", "kb_compta"}
	if got := plan.createSpecs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("specs = %v, want %v", got, want)
	}

	// Without an override, kb_compta is a bare name bench cannot find — and
	// the restore says so before building anything.
	lookup := func(_ context.Context, name, _ string) (bool, error) { return name == "erpnext", nil }
	problems := checkBareNames(plan, "", lookup, func(string) {})
	if len(problems) != 1 || !strings.Contains(problems[0].Message, `"kb_compta"`) ||
		!strings.Contains(problems[0].Override, "--app kb_compta=") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestCheckBareNames(t *testing.T) {
	m := devManifest()
	m.Bench.Apps = []string{"erpnext", "https://github.com/acme/private"}
	m.Site.InstalledApps = []string{"frappe", "erpnext", "private", "local"}
	m.Apps = []AppInfo{{Name: "local", NoGit: true, Source: AppSourceArchive, Member: appSourceMember("local")}}
	plan := planRestoreApps(m, nil)

	var asked []string
	lookup := func(_ context.Context, name, _ string) (bool, error) {
		asked = append(asked, name)
		return true, nil
	}
	if problems := checkBareNames(plan, "", lookup, func(string) {}); len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	// URLs and archived apps are never looked up.
	if !reflect.DeepEqual(asked, []string{"erpnext"}) {
		t.Fatalf("looked up %v, want [erpnext]", asked)
	}

	var warned []string
	failing := func(context.Context, string, string) (bool, error) { return false, errors.New("rate limited") }
	if problems := checkBareNames(plan, "", failing, func(s string) { warned = append(warned, s) }); len(problems) != 0 ||
		len(warned) != 1 {
		t.Fatalf("an unreachable API blocked the restore: problems=%v warned=%v", problems, warned)
	}
}

func TestPlanFrappe(t *testing.T) {
	tests := []struct {
		name       string
		info       *AppInfo
		override   string
		wantRepo   string
		wantBranch string
	}{
		{"legacy archive keeps the bench record", &AppInfo{Name: "frappe", Branch: "main"}, "", "", "version-15"},
		{"upstream remote at the checked-out branch",
			&AppInfo{Name: "frappe", Source: AppSourceGit, Remote: "https://github.com/frappe/frappe.git", RemoteBranch: "version-16"},
			"", "", "version-16"},
		{"a fork is restored as the fork",
			&AppInfo{Name: "frappe", Source: AppSourceGit, Remote: "git@github.com:kb/frappe.git", RemoteBranch: "main"},
			"", "git@github.com:kb/frappe.git", "main"},
		{"override to another upstream branch", nil, "frappe@version-16", "", "version-16"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := devManifest()
			m.Bench.FrappeBranch = "version-15"
			plan := planFrappe(m, tt.info, tt.override)
			if plan.FrappeRepo != tt.wantRepo || plan.FrappeBranch != tt.wantBranch {
				t.Fatalf("frappe = %q@%q, want %q@%q", plan.FrappeRepo, plan.FrappeBranch, tt.wantRepo, tt.wantBranch)
			}
		})
	}

	t.Run("archived frappe is unpacked over a normal init", func(t *testing.T) {
		m := devManifest()
		m.Bench.FrappeBranch = "version-15"
		info := &AppInfo{Name: "frappe", NoGit: true, Source: AppSourceArchive, Member: appSourceMember("frappe")}
		plan := planFrappe(m, info, "")
		if plan.FrappeArchive == nil || plan.FrappeBranch != "version-15" {
			t.Fatalf("plan = %+v", plan)
		}
	})
}

func TestIsUpstreamFrappe(t *testing.T) {
	for _, r := range []string{"https://github.com/frappe/frappe", "https://github.com/frappe/frappe.git",
		"git@github.com:frappe/frappe.git", "ssh://git@github.com/frappe/frappe", "HTTPS://GitHub.com/Frappe/Frappe/"} {
		if !isUpstreamFrappe(r) {
			t.Errorf("%q not recognised as upstream", r)
		}
	}
	for _, r := range []string{"https://github.com/kb/frappe", "https://github.com/frappe/frappe-fork", "https://gitlab.com/frappe/frappe"} {
		if isUpstreamFrappe(r) {
			t.Errorf("%q taken for upstream", r)
		}
	}
}

func TestCheckRestorePlan(t *testing.T) {
	m := devManifest()
	m.Site.InstalledApps = append(m.Site.InstalledApps, "local")
	m.Apps = []AppInfo{{Name: "local", NoGit: true, Source: AppSourceArchive, Member: appSourceMember("local")}}
	plan := planRestoreApps(m, nil)
	if !hasProblem(checkRestorePlan(m, plan, nil), "has no member") {
		t.Fatal("an archived app without its member was accepted")
	}
	m.Members = append(m.Members, archive.Member{Path: appSourceMember("local")})
	if problems := checkRestorePlan(m, plan, nil); len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if !hasProblem(checkRestorePlan(m, plan, map[string]string{"ghost": "https://x/y"}), "--app names ghost") {
		t.Fatal("an override for an app the archive does not have was accepted")
	}
}

func TestParseAppOverrides(t *testing.T) {
	for _, bad := range [][]string{
		{"kb_pro"},
		{"=https://x/y"},
		{"Bad-Name=https://x/y"},
		{"kb=https://x/y;id"},
		{"kb=https://x/y", "kb=https://x/z"},
	} {
		if _, err := parseAppOverrides(bad); err == nil {
			t.Errorf("parseAppOverrides(%q) accepted", bad)
		}
	}
	got, err := parseAppOverrides([]string{" kb = https://github.com/kb/kb@main "})
	if err != nil || got["kb"] != "https://github.com/kb/kb@main" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// bench writes apps.txt without a trailing newline; appending must not glue
// the new app onto the last line.
func TestMergeApps(t *testing.T) {
	got := mergeApps([]string{"frappe", "erpnext", "", " extra"}, []string{"local", "erpnext"})
	want := []string{"frappe", "erpnext", "extra", "local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeApps = %v, want %v", got, want)
	}
}

// The frappe fork from the original report: on "main" at backup time, created
// as version-15, no remote recorded.
func TestPlanWarningsLegacyFrappeBranch(t *testing.T) {
	m := legacyPrivateManifest()
	if w := planWarnings(planRestoreApps(m, nil)); len(w) != 1 || !strings.Contains(w[0], "--app frappe=<git-url>@main") {
		t.Fatalf("warnings = %v", w)
	}
	overrides, _ := parseAppOverrides([]string{"frappe=https://github.com/kb/frappe@main"})
	if w := planWarnings(planRestoreApps(m, overrides)); len(w) != 0 {
		t.Fatalf("warned despite an override: %v", w)
	}
	m.Apps[0].Branch = "version-15"
	if w := planWarnings(planRestoreApps(m, nil)); len(w) != 0 {
		t.Fatalf("warned about a matching branch: %v", w)
	}
}

// A repository is often named differently from the app it holds; the bench
// record must keep the URL the app was restored from, or `ffm recreate` is left
// with a bare name bench cannot resolve.
func TestAppsWithoutFrameworkKeepsSpecsByAppName(t *testing.T) {
	m := legacyPrivateManifest()
	overrides, err := parseAppOverrides([]string{"kb_pro=git@github.com:kb/AchatsExtern.git@erpnext"})
	if err != nil {
		t.Fatal(err)
	}
	got := appsWithoutFramework([]string{"frappe", "kb_pro", "kb_compta"}, planRestoreApps(m, overrides).specsByName())
	want := []string{"git@github.com:kb/AchatsExtern.git@erpnext", "kb_compta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apps = %v, want %v", got, want)
	}
}

// A token only reaches HTTPS clones, and an archive records whatever the source
// bench cloned with — usually SSH.
func TestWithGitHubHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:KB-Developpement/kb_pro.git@erpnext": "https://github.com/KB-Developpement/kb_pro@erpnext",
		"git@github.com:acme/app.git":                        "https://github.com/acme/app",
		"ssh://git@github.com/acme/app.git@v1.2":             "https://github.com/acme/app@v1.2",
		"git@gitlab.com:acme/app.git@main":                   "git@gitlab.com:acme/app.git@main",
		"https://github.com/acme/app@main":                   "https://github.com/acme/app@main",
		"erpnext":                                            "erpnext",
	} {
		if got := githubHTTPS(in); got != want {
			t.Errorf("githubHTTPS(%q) = %q, want %q", in, got, want)
		}
	}

	m := legacyPrivateManifest()
	m.Apps[0] = AppInfo{Name: "frappe", Source: AppSourceGit, Remote: "git@github.com:kb/kb_frappe.git", RemoteBranch: "main"}
	m.Apps[2] = AppInfo{Name: "kb_pro", Source: AppSourceGit, Remote: "git@github.com:kb/kb_pro.git", RemoteBranch: "erpnext"}
	m.Site.InstalledApps = []string{"frappe", "kb_pro", "local"}
	m.Apps = append(m.Apps, AppInfo{Name: "local", NoGit: true, Source: AppSourceArchive, Member: appSourceMember("local")})
	plan := planRestoreApps(m, nil).withGitHubHTTPS()
	if plan.FrappeRepo != "https://github.com/kb/kb_frappe" {
		t.Errorf("frappe repo = %q", plan.FrappeRepo)
	}
	if got := plan.createSpecs(); !reflect.DeepEqual(got, []string{"https://github.com/kb/kb_pro@erpnext"}) {
		t.Errorf("specs = %v", got)
	}
}

func TestSpecsByNameKeepsArchivedAppRemote(t *testing.T) {
	m := devManifest()
	m.Site.InstalledApps = []string{"frappe", "kb_impexp", "local"}
	m.Bench.Apps = nil
	m.Apps = []AppInfo{
		{Name: "kb_impexp", Branch: "erpnext", Remote: "https://github.com/kb/AchatsExtern.git", Unpushed: true,
			Source: AppSourceArchive, Member: appSourceMember("kb_impexp")},
		{Name: "local", NoGit: true, Source: AppSourceArchive, Member: appSourceMember("local")},
	}
	got := planRestoreApps(m, nil).specsByName()
	want := map[string]string{"kb_impexp": "https://github.com/kb/AchatsExtern.git@erpnext"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("specs = %v, want %v", got, want)
	}
}
