package manager

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Where a restored app's code comes from, for the plan and for error messages.
const (
	originOverride = "--app"
	originArchive  = "archive"
	originRemote   = "recorded remote"
	originRecord   = "bench record"
	originBareName = "bare name"
)

// restoreApp is how one app will be brought back.
type restoreApp struct {
	Name string
	// Spec is what Create passes to `bench get-app`. Empty for an app whose
	// source comes out of the archive.
	Spec   string
	Origin string
	// Info is the provenance the archive recorded, when it has any.
	Info *AppInfo
}

// Archived reports whether the app is unpacked from the archive rather than cloned.
func (a restoreApp) Archived() bool { return a.Origin == originArchive }

// restorePlan is where every piece of code the restored site needs comes from.
type restorePlan struct {
	// FrappeRepo and FrappeBranch go to bench init. An empty repo means
	// upstream frappe.
	FrappeRepo   string
	FrappeBranch string
	FrappeOrigin string
	// FrappeArchive is set when frappe's own source is in the archive. bench
	// init still runs against a clonable frappe first — it needs one to build
	// the bench at all — and the archived tree then replaces it.
	FrappeArchive *AppInfo
	Apps          []restoreApp
	// frappeInfo is frappe's recorded provenance, if any.
	frappeInfo *AppInfo
}

// createSpecs returns the specs Create clones, in order.
func (p restorePlan) createSpecs() []string {
	var out []string
	for _, a := range p.Apps {
		if !a.Archived() {
			out = append(out, a.Spec)
		}
	}
	return out
}

// specsByName maps each app to the spec the bench record should keep for it:
// what a cloned app was cloned from, and for an archived app the remote it was
// recorded with, if any. The latter cannot reproduce the archived tree — only
// the archive can — but it gives `ffm recreate` something to clone, where a
// bare name fails outright for any app outside the frappe and erpnext orgs.
func (p restorePlan) specsByName() map[string]string {
	out := make(map[string]string, len(p.Apps))
	for _, a := range p.Apps {
		switch {
		case !a.Archived():
			out[a.Name] = a.Spec
		case a.Info != nil && a.Info.Remote != "":
			branch := a.Info.RemoteBranch
			if branch == "" && a.Info.Branch != "HEAD" {
				branch = a.Info.Branch
			}
			out[a.Name] = withBranch(a.Info.Remote, branch)
		}
	}
	return out
}

// archivedApps returns every app unpacked from the archive, frappe first.
func (p restorePlan) archivedApps() []AppInfo {
	var out []AppInfo
	if p.FrappeArchive != nil {
		out = append(out, *p.FrappeArchive)
	}
	for _, a := range p.Apps {
		if a.Archived() && a.Info != nil {
			out = append(out, *a.Info)
		}
	}
	return out
}

// pinnable returns the recorded provenance of every cloned app --pin-apps can
// check out at its commit, frappe included. Archived apps are excluded: they
// already are that exact tree, and have no remote to fetch from.
func (p restorePlan) pinnable() []AppInfo {
	var out []AppInfo
	if p.FrappeArchive == nil && p.frappeInfo != nil {
		out = append(out, *p.frappeInfo)
	}
	for _, a := range p.Apps {
		if !a.Archived() && a.Info != nil && appNameRe.MatchString(a.Name) {
			out = append(out, *a.Info)
		}
	}
	return out
}

// parseAppOverrides parses repeated --app name=source[@branch] values.
func parseAppOverrides(raw []string) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for _, r := range raw {
		name, spec, ok := strings.Cut(strings.TrimSpace(r), "=")
		name, spec = strings.TrimSpace(name), strings.TrimSpace(spec)
		if !ok || name == "" || spec == "" {
			return nil, fmt.Errorf("--app %q: expected <app>=<git-url>[@branch]", r)
		}
		if !appNameRe.MatchString(name) {
			return nil, fmt.Errorf("--app %q: %q is not a Frappe app name", r, name)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--app: %s is given more than once", name)
		}
		// Held to the same rules as a source read from the archive: it ends up
		// in the same `bash -c` string.
		s := bench.ParseAppSpec(spec, "")
		if !(appNameRe.MatchString(s.Source) || gitURLRe.MatchString(s.Source)) ||
			(s.Branch != "" && !gitRefRe.MatchString(s.Branch)) {
			return nil, fmt.Errorf("--app %s: %q is not a git URL or app name ffm will pass to a shell", name, spec)
		}
		out[name] = spec
	}
	return out, nil
}

// planRestoreApps decides where each app comes from.
//
// The app list is the bench record's specs followed by every installed app the
// record does not mention, so nothing the database expects is dropped. For
// each app, in order of precedence:
//
//  1. an --app override — the user knows something the archive does not;
//  2. the source stored in the archive;
//  3. the remote recorded at backup time, at the branch that held the commit —
//     that is what was actually on disk, where the bench record is only what
//     was asked for at create time;
//  4. the bench record's spec;
//  5. the bare name, which bench resolves against github.com/frappe and
//     github.com/erpnext only.
func planRestoreApps(m Manifest, overrides map[string]string) restorePlan {
	byName := make(map[string]*AppInfo, len(m.Apps))
	for i := range m.Apps {
		byName[m.Apps[i].Name] = &m.Apps[i]
	}

	plan := planFrappe(m, byName["frappe"], overrides["frappe"])
	plan.frappeInfo = byName["frappe"]

	recordSpec := make(map[string]string, len(m.Bench.Apps))
	var names []string
	seen := map[string]bool{"frappe": true}
	for _, raw := range m.Bench.Apps {
		name := bench.ParseAppSpec(raw, "").DisplayName()
		if seen[name] {
			continue
		}
		seen[name] = true
		recordSpec[name] = raw
		names = append(names, name)
	}
	for _, name := range m.Site.InstalledApps {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	for _, name := range names {
		info := byName[name]
		app := restoreApp{Name: name, Info: info}
		switch {
		case overrides[name] != "":
			app.Spec, app.Origin = overrides[name], originOverride
		case info != nil && info.Archived():
			app.Origin = originArchive
		case info != nil && info.Remote != "" && (info.CloneBranch() != "" || recordSpec[name] == ""):
			app.Spec, app.Origin = withBranch(info.Remote, info.CloneBranch()), originRemote
		case recordSpec[name] != "":
			app.Spec, app.Origin = recordSpec[name], originRecord
		default:
			app.Spec, app.Origin = name, originBareName
		}
		plan.Apps = append(plan.Apps, app)
	}
	return plan
}

// planFrappe decides the frappe repository and branch bench init uses.
func planFrappe(m Manifest, info *AppInfo, override string) restorePlan {
	plan := restorePlan{FrappeBranch: m.Bench.FrappeBranch, FrappeOrigin: originRecord}
	if m.Bench.FrappeRepo != "" {
		spec := bench.ParseAppSpec(m.Bench.FrappeRepo, "")
		plan.FrappeRepo = spec.Source
		if spec.Branch != "" {
			plan.FrappeBranch = spec.Branch
		}
	}

	switch {
	case override != "":
		spec := bench.ParseAppSpec(override, "")
		plan.FrappeOrigin = originOverride
		if spec.IsURL {
			plan.FrappeRepo = spec.Source
		} else {
			// "frappe@version-16": upstream frappe at another branch.
			plan.FrappeRepo = ""
		}
		if spec.Branch != "" {
			plan.FrappeBranch = spec.Branch
		}
	case info != nil && info.Archived():
		plan.FrappeArchive = info
	case info != nil && info.Remote != "":
		// What was checked out beats what the bench was created with: a fork
		// swapped in after create, or `bench switch-to-branch`, is recorded only
		// here.
		plan.FrappeOrigin = originRemote
		plan.FrappeRepo = ""
		if !isUpstreamFrappe(info.Remote) {
			plan.FrappeRepo = info.Remote
		}
		if b := info.CloneBranch(); b != "" {
			plan.FrappeBranch = b
		}
	}
	return plan
}

// githubSSHRe matches the two SSH spellings of a github.com repository.
var githubSSHRe = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9._-]+/[A-Za-z0-9._-]+?)(?:\.git)?$`)

// githubHTTPS rewrites a github.com SSH source to HTTPS, keeping any @branch.
// Anything else is returned unchanged.
func githubHTTPS(spec string) string {
	s := bench.ParseAppSpec(spec, "")
	m := githubSSHRe.FindStringSubmatch(s.Source)
	if m == nil {
		return spec
	}
	return withBranch("https://github.com/"+m[1], s.Branch)
}

// withGitHubHTTPS rewrites every github.com SSH source in the plan to HTTPS.
//
// Applied when --github-token is given, because the token only reaches HTTPS
// clones: git hands an SSH URL to ssh, which never consults the credential
// store. The remote a backup records is whatever the source bench cloned
// with — usually SSH on a developer's machine — and the restoring host may
// have no working agent inside the container, so honouring the token is what
// lets such an archive restore with nothing but a token.
func (p restorePlan) withGitHubHTTPS() restorePlan {
	p.FrappeRepo = githubHTTPS(p.FrappeRepo)
	apps := make([]restoreApp, len(p.Apps))
	for i, a := range p.Apps {
		if !a.Archived() {
			a.Spec = githubHTTPS(a.Spec)
		}
		apps[i] = a
	}
	p.Apps = apps
	return p
}

// describePlan renders one line per app saying where its code comes from.
func describePlan(p restorePlan) []string {
	frappe := "upstream frappe"
	if p.FrappeRepo != "" {
		frappe = p.FrappeRepo
	}
	lines := []string{fmt.Sprintf("frappe       %s@%s (%s)", frappe, p.FrappeBranch, p.FrappeOrigin)}
	if p.FrappeArchive != nil {
		lines[0] = fmt.Sprintf("frappe       archived source (%s), replacing %s@%s after bench init",
			describeVendorReason(p.FrappeArchive.SourceReason), frappe, p.FrappeBranch)
	}
	for _, a := range p.Apps {
		if a.Archived() {
			reason := ""
			if a.Info != nil {
				reason = describeVendorReason(a.Info.SourceReason)
			}
			lines = append(lines, fmt.Sprintf("%-12s archived source (%s)", a.Name, reason))
			continue
		}
		lines = append(lines, fmt.Sprintf("%-12s %s (%s)", a.Name, a.Spec, a.Origin))
	}
	return lines
}

// planWarnings reports what the plan may get wrong without being able to know.
//
// The one case today: an archive from before remotes were recorded, whose
// frappe was checked out on a different branch than the bench was created
// with. That is what a fork or a `bench switch-to-branch` looks like, and the
// archive has no way to say which repository the branch lives in.
func planWarnings(p restorePlan) []string {
	info := p.frappeInfo
	if info == nil || p.FrappeOrigin != originRecord || info.Archived() || info.Remote != "" {
		return nil
	}
	if info.Branch == "" || info.Branch == "HEAD" || info.Branch == p.FrappeBranch {
		return nil
	}
	return []string{fmt.Sprintf(
		"frappe was on branch %q when this archive was taken, but the archive records no frappe "+
			"remote, so bench init uses %q from the bench record. If that bench ran a fork, pass "+
			"--app frappe=<git-url>@%s", info.Branch, p.FrappeBranch, info.Branch)}
}

// withBranch appends an @branch suffix when there is a branch.
func withBranch(source, branch string) string {
	if branch == "" {
		return source
	}
	return source + "@" + branch
}

// isUpstreamFrappe reports whether a remote is github.com/frappe/frappe in any
// of the spellings git accepts.
func isUpstreamFrappe(remote string) bool {
	r := strings.ToLower(strings.TrimSpace(remote))
	r = strings.TrimSuffix(strings.TrimSuffix(r, "/"), ".git")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		r = strings.TrimPrefix(r, p)
	}
	if at := strings.Index(r, "@"); at >= 0 {
		r = r[at+1:]
	}
	r = strings.Replace(r, ":", "/", 1)
	return r == "github.com/frappe/frappe"
}

// checkRestorePlan validates the plan against the archive: every archived
// source must be a member, and every value headed for a shell must be one ffm
// is willing to pass.
func checkRestorePlan(m Manifest, plan restorePlan, overrides map[string]string) []Problem {
	var problems []Problem
	bad := func(field, value string) {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("the archive's %s contains characters ffm will not pass to a shell: %q "+
				"— the archive is corrupt or was not written by ffm", field, value),
		})
	}

	if plan.FrappeBranch != "" && !gitRefRe.MatchString(plan.FrappeBranch) {
		bad("frappe branch", plan.FrappeBranch)
	}
	if plan.FrappeRepo != "" && !gitURLRe.MatchString(plan.FrappeRepo) {
		bad("frappe repo", plan.FrappeRepo)
	}

	members := make(map[string]bool, len(m.Members))
	for _, mem := range m.Members {
		members[mem.Path] = true
	}
	for _, a := range plan.archivedApps() {
		if !appNameRe.MatchString(a.Name) {
			bad("app name", a.Name)
			continue
		}
		if a.Member != appSourceMember(a.Name) || !members[a.Member] {
			problems = append(problems, Problem{Message: fmt.Sprintf(
				"the archive says app %s's source is stored in it, but it has no member %s",
				a.Name, appSourceMember(a.Name))})
		}
	}

	for _, a := range plan.Apps {
		if !appNameRe.MatchString(a.Name) {
			bad("app name", a.Name)
		}
		if a.Archived() {
			continue
		}
		spec := bench.ParseAppSpec(a.Spec, "")
		okSource := appNameRe.MatchString(spec.Source) || gitURLRe.MatchString(spec.Source)
		if !okSource || (spec.Branch != "" && !gitRefRe.MatchString(spec.Branch)) {
			bad("app source", a.Spec)
		}
	}

	known := map[string]bool{"frappe": true}
	for _, a := range plan.Apps {
		known[a.Name] = true
	}
	var unknown []string
	for name := range overrides {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		var have []string
		for _, a := range plan.Apps {
			have = append(have, a.Name)
		}
		problems = append(problems, Problem{Message: fmt.Sprintf(
			"--app names %s, which this archive neither installs nor records (its apps: frappe, %s)",
			strings.Join(unknown, ", "), strings.Join(have, ", "))})
	}
	return problems
}

// registryLookup reports whether bench can resolve a bare app name: bench
// get-app looks it up as github.com/frappe/<name> and github.com/erpnext/<name>
// and nowhere else. The error is for "could not tell".
type registryLookup func(ctx context.Context, name, token string) (bool, error)

// githubRegistryLookup asks the GitHub API the same question bench will.
var githubRegistryLookup registryLookup = func(ctx context.Context, name, token string) (bool, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, org := range []string{"frappe", "erpnext"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://api.github.com/repos/"+org+"/"+url.PathEscape(name), nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			return true, nil
		case http.StatusNotFound:
			continue
		default:
			return false, fmt.Errorf("GitHub answered %s", resp.Status)
		}
	}
	return false, nil
}

// checkBareNames catches, before anything is built, the app that bench would
// only fail to find several minutes into provisioning.
//
// Only bare names are looked up — every other source is a URL or the archive.
// A lookup that cannot complete (offline, rate-limited) warns and lets the
// restore proceed, because an unreachable GitHub API does not mean the clone
// will fail.
func checkBareNames(plan restorePlan, token string, lookup registryLookup, warn func(string)) []Problem {
	var problems []Problem
	for _, a := range plan.Apps {
		if a.Archived() {
			continue
		}
		spec := bench.ParseAppSpec(a.Spec, "")
		if spec.IsURL {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		found, err := lookup(ctx, spec.Source, token)
		cancel()
		if err != nil {
			warn(fmt.Sprintf("could not check whether bench can find app %q by name (%v) — trying anyway", spec.Source, err))
			continue
		}
		if found {
			continue
		}
		problems = append(problems, Problem{
			Message: fmt.Sprintf("app %q has no source: the archive records no git remote for it, and "+
				"%q is not an app under github.com/frappe or github.com/erpnext, the only places bench "+
				"looks a bare name up", a.Name, spec.Source),
			Override: fmt.Sprintf("--app %s=<git-url>[@branch]", a.Name),
		})
	}
	return problems
}

// validateArchivedApps checks every archived app tarball before it is unpacked.
func validateArchivedApps(plan restorePlan, staging string) error {
	for _, a := range plan.archivedApps() {
		local := filepath.Join(staging, filepath.FromSlash(a.Member))
		if err := archive.ValidateAppSource(local, a.Name, archive.AppSourceLimits{}); err != nil {
			return err
		}
	}
	return nil
}

// appExtractScript unpacks an app's tarball ($2) into apps/ as $1, replacing
// whatever Create put there. --no-same-owner because the tarball records the
// uid of the machine it came from.
const appExtractScript = `cd /workspace/frappe-bench/apps && rm -rf "$1" && ` +
	`tar -xzf "$2" --no-same-owner && test -d "$1"`

// installArchivedApps unpacks each app whose source travelled in the archive
// and installs its dependencies.
//
// `bench setup requirements <app>` cannot do this: without --python or --node
// it builds a bench App object, which opens the directory as a git repository
// and crashes on an app that is not one. The two halves run separately
// instead, which is what those flags exist for.
func (s *Service) installArchivedApps(runner *bench.Runner, b state.Bench, plan restorePlan,
	staging string, pw ProgressWriter) error {

	apps := plan.archivedApps()
	if len(apps) == 0 {
		return nil
	}
	remote := fmt.Sprintf("%s-apps-%d", restoreStaging, time.Now().UnixNano())
	if out, err := runner.ExecSilent("frappe", "mkdir", "-p", remote); err != nil {
		return fmt.Errorf("prepare the app source directory in the container: %w\n%s", err, out)
	}
	defer func() {
		if _, err := runner.ExecSilent("frappe", "rm", "-rf", remote); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not clean up %s: %v\n", remote, err)
		}
	}()

	var names []string
	for _, a := range apps {
		pw.Step(fmt.Sprintf("Unpacking the archived source of %s", a.Name))
		local := filepath.Join(staging, filepath.FromSlash(a.Member))
		if err := os.Chmod(local, 0o644); err != nil {
			return err
		}
		dest := remote + "/" + a.Name + ".tar.gz"
		if err := runner.CopyTo("frappe", local, dest); err != nil {
			return fmt.Errorf("copy the source of %s into the container: %w", a.Name, err)
		}
		if out, err := runner.ExecSilent("frappe", "bash", "-c", appExtractScript, "ffm-extract", a.Name, dest); err != nil {
			return fmt.Errorf("unpack the source of %s: %w\n%s", a.Name, err, out)
		}
		names = append(names, a.Name)
	}

	if err := syncAppsTxt(b, names); err != nil {
		return fmt.Errorf("register the archived apps in sites/apps.txt: %w", err)
	}

	pw.Step("Installing dependencies of the archived apps — this may take a few minutes")
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = bench.ShellQuote(n)
	}
	list := strings.Join(quoted, " ")
	for _, part := range []string{"--python", "--node"} {
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && bench setup requirements "+part+" "+list); err != nil {
			return fmt.Errorf("bench setup requirements %s: %w\n%s", part, err, out)
		}
	}
	return nil
}

// syncAppsTxt adds the archived apps to sites/apps.txt, which is what makes
// Frappe consider an app present on the bench at all. Order is left alone: hook
// order comes from the database's installed_apps, and apps.txt only filters
// that list (frappe.get_installed_apps).
func syncAppsTxt(b state.Bench, add []string) error {
	path := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", "apps.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := mergeApps(strings.Split(string(raw), "\n"), add)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// mergeApps appends each app in add that apps.txt does not list yet. bench
// writes the file without a trailing newline, so blank and padded lines are
// normalised rather than trusted.
func mergeApps(existing, add []string) []string {
	var apps []string
	seen := map[string]bool{}
	for _, l := range append(append([]string{}, existing...), add...) {
		l = strings.TrimSpace(l)
		if l != "" && !seen[l] {
			seen[l] = true
			apps = append(apps, l)
		}
	}
	return apps
}
