package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// No test in this package may reach api.github.com: a restore now checks every
// pinned commit before Create. Tests of the check pass their own lookup.
func init() {
	githubCommitStatus = func(context.Context, string, string, string) (int, error) {
		return 0, errors.New("network disabled in tests")
	}
}

func TestGithubRepoOf(t *testing.T) {
	cases := []struct {
		src, repo string
		ssh       bool
	}{
		{"https://github.com/KB-Developpement/kb_frappe.git", "KB-Developpement/kb_frappe", false},
		{"https://github.com/frappe/frappe", "frappe/frappe", false},
		{"https://github.com/frappe/frappe/", "frappe/frappe", false},
		{"git@github.com:KB-Developpement/kb_pro.git", "KB-Developpement/kb_pro", true},
		{"ssh://git@github.com/acme/app", "acme/app", true},
		{"https://gitlab.com/acme/app.git", "", false},
		{"https://github.com/acme/app/../../x", "", false},
		{"erpnext", "", false},
		{"https://github.com/../x", "", false},
	}
	for _, c := range cases {
		repo, ssh := githubRepoOf(c.src)
		if repo != c.repo || (repo != "" && ssh != c.ssh) {
			t.Errorf("githubRepoOf(%q) = %q, %v; want %q, %v", c.src, repo, ssh, c.repo, c.ssh)
		}
	}
}

// pinCheckPlan: frappe from a private https fork, one https app, one SSH app,
// one archived app and one app on another host.
func pinCheckPlan() restorePlan {
	frappe := AppInfo{Name: "frappe", Commit: commitA}
	return restorePlan{
		FrappeRepo: "https://github.com/acme/kb_frappe.git", FrappeBranch: "main", frappeInfo: &frappe,
		Apps: []restoreApp{
			{Name: "kb_pro", Spec: "https://github.com/acme/kb_pro@dev", Origin: originRemote, Info: &AppInfo{Name: "kb_pro", Commit: commitB}},
			{Name: "emire", Spec: "git@github.com:acme/emire.git@x", Origin: originRemote, Info: &AppInfo{Name: "emire", Commit: commitB}},
			{Name: "achats", Origin: originArchive, Info: &AppInfo{Name: "achats", Commit: commitC, Source: AppSourceArchive, Member: "m"}},
			{Name: "other", Spec: "https://gitlab.com/acme/other@main", Origin: originRemote, Info: &AppInfo{Name: "other", Commit: commitC}},
			{Name: "nocommit", Spec: "https://github.com/acme/nocommit", Origin: originRemote, Info: &AppInfo{Name: "nocommit"}},
		},
	}
}

type lookupCall struct{ repo, sha, token string }

func fixedLookup(status map[string]int, err error, calls *[]lookupCall) commitStatus {
	return func(_ context.Context, repo, sha, token string) (int, error) {
		*calls = append(*calls, lookupCall{repo, sha, token})
		if err != nil {
			return 0, err
		}
		if s, ok := status[repo]; ok {
			return s, nil
		}
		return http.StatusOK, nil
	}
}

func TestCheckPinnedCommitsAsksOnlyGitHubForPinnedApps(t *testing.T) {
	var calls []lookupCall
	problems := checkPinnedCommits(pinCheckPlan(), "tok", fixedLookup(nil, nil, &calls), func(string) { t.Error("unexpected warning") })
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	want := []lookupCall{
		{"acme/kb_frappe", commitA, "tok"},
		{"acme/kb_pro", commitB, "tok"},
		{"acme/emire", commitB, "tok"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %v, want %v", i, calls[i], want[i])
		}
	}
}

func TestCheckPinnedCommitsUpstreamFrappe(t *testing.T) {
	p := pinCheckPlan()
	p.FrappeRepo = ""
	p.Apps = nil
	var calls []lookupCall
	checkPinnedCommits(p, "", fixedLookup(nil, nil, &calls), func(string) {})
	if len(calls) != 1 || calls[0].repo != "frappe/frappe" {
		t.Errorf("calls = %v", calls)
	}
}

func TestCheckPinnedCommitsRefusals(t *testing.T) {
	cases := []struct {
		name     string
		status   map[string]int
		token    string
		wantSub  []string
		noRefuse bool
	}{
		{name: "commit gone", status: map[string]int{"acme/kb_pro": 422}, token: "tok",
			wantSub: []string{"kb_pro", shortCommit(commitB), "no longer on github.com/acme/kb_pro", "--latest-apps"}},
		{name: "private https, no token", status: map[string]int{"acme/kb_frappe": 404},
			wantSub: []string{"frappe", "private or does not exist", "FFM_GITHUB_TOKEN", "gh auth token"}},
		{name: "token without access", status: map[string]int{"acme/kb_pro": 404}, token: "tok",
			wantSub: []string{"token cannot read github.com/acme/kb_pro"}},
		{name: "ssh, no token, 404: the agent may get in", status: map[string]int{"acme/emire": 404}, noRefuse: true},
	}
	for _, c := range cases {
		var calls []lookupCall
		problems := checkPinnedCommits(pinCheckPlan(), c.token, fixedLookup(c.status, nil, &calls), func(string) {})
		if c.noRefuse {
			if len(problems) != 0 {
				t.Errorf("%s: refused: %v", c.name, problems)
			}
			continue
		}
		if len(problems) != 1 {
			t.Errorf("%s: problems = %v, want one", c.name, problems)
			continue
		}
		text := problems[0].String()
		for _, w := range c.wantSub {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", c.name, text, w)
			}
		}
	}
}

func TestCheckPinnedCommitsBadTokenReportedOnce(t *testing.T) {
	var calls []lookupCall
	all := map[string]int{"acme/kb_frappe": 401, "acme/kb_pro": 401, "acme/emire": 401}
	problems := checkPinnedCommits(pinCheckPlan(), "bad", fixedLookup(all, nil, &calls), func(string) {})
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "401") {
		t.Errorf("problems = %v", problems)
	}
	if len(calls) != 1 {
		t.Errorf("kept asking after a 401: %d calls", len(calls))
	}
}

func TestCheckPinnedCommitsWarnsWhenItCannotTell(t *testing.T) {
	for _, c := range []struct {
		name   string
		status map[string]int
		err    error
	}{
		{"offline", nil, errors.New("dial tcp: no route")},
		{"rate limited", map[string]int{"acme/kb_frappe": 403, "acme/kb_pro": 403, "acme/emire": 403}, nil},
	} {
		var calls []lookupCall
		var warnings []string
		problems := checkPinnedCommits(pinCheckPlan(), "", fixedLookup(c.status, c.err, &calls), func(m string) { warnings = append(warnings, m) })
		if len(problems) != 0 {
			t.Errorf("%s: refused on an answer it cannot interpret: %v", c.name, problems)
		}
		// One warning, and no further calls: offline, each would wait out its
		// timeout; rate-limited, each would be refused the same way.
		if len(warnings) != 1 || !strings.Contains(warnings[0], "trying anyway") {
			t.Errorf("%s: warnings = %v", c.name, warnings)
		}
		if len(calls) != 1 {
			t.Errorf("%s: %d calls, want 1", c.name, len(calls))
		}
	}
}

func TestCheckPinnedCommitsSkipsMalformedCommit(t *testing.T) {
	// checkManifestValues refuses it; it must never reach a URL.
	p := pinCheckPlan()
	p.frappeInfo = &AppInfo{Name: "frappe", Commit: "$(id)"}
	p.Apps = nil
	var calls []lookupCall
	checkPinnedCommits(p, "", fixedLookup(nil, nil, &calls), func(string) {})
	if len(calls) != 0 {
		t.Errorf("looked up a malformed commit: %v", calls)
	}
}

func TestPinFailureSuffix(t *testing.T) {
	if s := pinFailureSuffix("kb_pro", "fatal: remote error: upload-pack: not our ref "+commitB, true); !strings.Contains(s, "--latest-apps") ||
		!strings.Contains(s, "--app kb_pro=") {
		t.Errorf("gone commit: %q", s)
	}
	if s := pinFailureSuffix("kb_pro", "fatal: could not read Username for 'https://github.com'", false); !strings.Contains(s, "FFM_GITHUB_TOKEN") {
		t.Errorf("auth: %q", s)
	}
}

func TestLatestAppsSkipsTheCommitGate(t *testing.T) {
	m := devManifest()
	m.Apps = []AppInfo{{Name: "erpnext", Commit: "$(id)"}}
	if !hasProblem(checkManifestValues(m, RestoreInput{}), "app commit") {
		t.Error("a pinned restore accepted a malformed commit")
	}
	if hasProblem(checkManifestValues(m, RestoreInput{LatestApps: true}), "app commit") {
		t.Error("--latest-apps was refused over a commit it never uses")
	}
}

func TestOverrideOntoAnotherBranchIsNotPinned(t *testing.T) {
	m := Manifest{
		Bench: state.Bench{FrappeBranch: "version-15", FrappeRepo: "https://github.com/acme/kb_frappe.git@main",
			Apps: []string{"https://github.com/acme/kb_pro@dev"}},
		Site: SiteInfo{InstalledApps: []string{"frappe", "kb_pro"}},
		Apps: []AppInfo{
			{Name: "frappe", Remote: "https://github.com/acme/kb_frappe.git", RemoteBranch: "main", Commit: commitA, Source: AppSourceGit},
			{Name: "kb_pro", Remote: "https://github.com/acme/kb_pro", RemoteBranch: "dev", Commit: commitB, Source: AppSourceGit},
		},
	}
	names := func(p restorePlan) []string {
		var out []string
		for _, t := range pinTargets(p) {
			out = append(out, t.Name+"@"+t.Source)
		}
		if len(pinnable(p)) != len(out) {
			t.Errorf("pinnable and pinTargets disagree: %v vs %v", pinnable(p), out)
		}
		return out
	}
	for _, c := range []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{"no override", nil, "frappe@https://github.com/acme/kb_frappe.git kb_pro@https://github.com/acme/kb_pro"},
		{"moved repo, no branch: pinned", map[string]string{"kb_pro": "https://github.com/new/kb_pro"},
			"frappe@https://github.com/acme/kb_frappe.git kb_pro@https://github.com/new/kb_pro"},
		{"same branch: pinned", map[string]string{"kb_pro": "https://github.com/new/kb_pro@dev"},
			"frappe@https://github.com/acme/kb_frappe.git kb_pro@https://github.com/new/kb_pro"},
		{"another branch: not pinned", map[string]string{"kb_pro": "https://github.com/acme/kb_pro@develop"},
			"frappe@https://github.com/acme/kb_frappe.git"},
		{"frappe onto another branch: not pinned", map[string]string{"frappe": "https://github.com/acme/kb_frappe.git@version-16"},
			"kb_pro@https://github.com/acme/kb_pro"},
		{"frappe moved, same branch: pinned", map[string]string{"frappe": "https://github.com/new/kb_frappe.git@main"},
			"frappe@https://github.com/new/kb_frappe.git kb_pro@https://github.com/acme/kb_pro"},
	} {
		got := strings.Join(names(planRestoreApps(m, c.overrides)), " ")
		if got != c.want {
			t.Errorf("%s: pinned %q, want %q", c.name, got, c.want)
		}
	}
}

func pinnable(p restorePlan) []AppInfo { return p.pinnable() }
