package manager

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// appProbeScript prints one key=value line per git fact about the app
// directory passed as $1. One exec per app instead of one per question: each
// `docker compose exec` costs a process spawn and a round trip to the daemon.
//
// The show-toplevel comparison is what tells an app that IS a repository from
// one that merely sits inside one — a bench directory someone put under git
// would otherwise make every non-git app look like a clean checkout of it.
//
// The remote is looked up as "upstream" first because that is the name bench
// gives it: `bench init` and `bench get-app` both clone with --origin upstream.
// "origin" covers apps cloned by hand, and the first remote of any name covers
// the rest.
const appProbeScript = `cd "$1" || exit 1
top=$(git rev-parse --show-toplevel 2>/dev/null)
if [ -z "$top" ] || [ "$top" != "$(pwd -P)" ]; then echo git=no; exit 0; fi
echo git=yes
echo commit=$(git rev-parse --verify -q HEAD)
echo branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null)
echo tag=$(git describe --tags --exact-match HEAD 2>/dev/null)
name=; url=
for r in upstream origin $(git remote); do
  url=$(git remote get-url "$r" 2>/dev/null) && [ -n "$url" ] && { name=$r; break; }
  url=
done
echo remote_name=$name
echo remote=$url
echo tracking=$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null)
echo containing=$(git for-each-ref --contains HEAD --format='%(refname)' refs/remotes refs/tags 2>/dev/null | head -50 | tr '\n' ' ')
`

// appProbe is the parsed output of appProbeScript.
type appProbe struct {
	git        bool
	commit     string
	branch     string
	tag        string
	remoteName string
	remote     string
	tracking   string
	containing []string
}

// parseAppProbe reads appProbeScript's key=value output.
func parseAppProbe(out string) appProbe {
	var p appProbe
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "git":
			p.git = value == "yes"
		case "commit":
			p.commit = value
		case "branch":
			p.branch = value
		case "tag":
			p.tag = value
		case "remote_name":
			p.remoteName = value
		case "remote":
			p.remote = value
		case "tracking":
			p.tracking = value
		case "containing":
			p.containing = strings.Fields(value)
		}
	}
	return p
}

// applyProbe fills an AppInfo's git provenance from a probe.
//
// It decides two things a restore depends on. First, which branch of the
// remote to clone: the local branch is not good enough, because a branch that
// was renamed locally or never pushed makes `git clone --branch` fail. Second,
// whether the commit exists on the remote at all — if it does not, cloning
// succeeds and quietly produces different code, which is worse than failing.
func applyProbe(info *AppInfo, p appProbe) {
	if !p.git {
		info.NoGit = true
		return
	}
	info.Commit = p.commit
	info.Branch = p.branch
	info.Tag = p.tag
	if remote, ok := cloneableRemote(p.remote); ok {
		info.Remote = remote
	}
	if info.Remote == "" || info.Commit == "" {
		return
	}

	prefix := "refs/remotes/" + p.remoteName + "/"
	var onRemote []string
	published := false
	for _, ref := range p.containing {
		switch {
		case strings.HasPrefix(ref, "refs/tags/"):
			published = true
		case strings.HasPrefix(ref, prefix):
			published = true
			if b := strings.TrimPrefix(ref, prefix); b != "HEAD" && b != "" {
				onRemote = append(onRemote, b)
			}
		}
	}
	info.Unpushed = !published

	// The tracking branch wins when it holds the commit, then a remote branch
	// with the local branch's name, then any remote branch that holds it.
	tracked := strings.TrimPrefix(p.tracking, p.remoteName+"/")
	candidates := []string{}
	if p.tracking != "" && tracked != p.tracking {
		candidates = append(candidates, tracked)
	}
	candidates = append(candidates, p.branch)
	for _, want := range candidates {
		for _, b := range onRemote {
			if b == want {
				info.RemoteBranch = b
				return
			}
		}
	}
	if len(onRemote) > 0 {
		sort.Strings(onRemote)
		info.RemoteBranch = onRemote[0]
	}
}

// scpRemoteRe matches git's scp-like SSH syntax: user@host:path.
var scpRemoteRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:.+$`)

// cloneableRemote returns a remote URL a different machine can clone from, or
// false when there is none.
//
// Rejected: local paths and file:// URLs (an app fetched with
// `bench get-app /some/dir` records that directory, which does not exist on
// the restoring host), and anything restore's own validation would refuse.
// Credentials embedded in an HTTPS URL are stripped rather than archived: they
// would be printed in the clone step's output, and --github-token already
// exists to supply them at restore time.
func cloneableRemote(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if scpRemoteRe.MatchString(raw) {
		return raw, gitURLRe.MatchString(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git":
	default:
		return "", false
	}
	if u.Scheme == "https" || u.Scheme == "http" {
		u.User = nil
	}
	clean := u.String()
	return clean, gitURLRe.MatchString(clean)
}

// vendorReason says why an app's source has to travel inside the archive, or
// returns empty when a clone will reproduce it.
func vendorReason(info AppInfo, forced bool) string {
	switch {
	case info.NoGit:
		return VendorNoGit
	case info.Remote == "":
		return VendorNoRemote
	case info.Commit == "":
		return VendorNoCommit
	case info.Unpushed:
		return VendorUnpushed
	case forced:
		return VendorRequested
	}
	return ""
}

// describeVendorReason renders a VendorReason for human output.
func describeVendorReason(reason string) string {
	switch reason {
	case VendorNoGit:
		return "not a git repository"
	case VendorNoRemote:
		return "no git remote another machine can clone from"
	case VendorNoCommit:
		return "a git repository with no commits"
	case VendorUnpushed:
		// Deliberately "that this clone knows of": a shallow or stale clone
		// has no remote-tracking ref for a branch that does hold the commit,
		// and asking the remote would need network and credentials at backup
		// time. Archiving the tree is the safe answer either way.
		return "no remote branch or tag this clone knows of contains its commit"
	case VendorRequested:
		return "requested with --vendor-apps"
	}
	return reason
}

// appSourceMember is the archive member holding an app's source.
func appSourceMember(app string) string {
	return archive.Prefix + "/apps/" + app + ".tar.gz"
}

// appSourceExcludes are left out of an archived app. Every one is rebuilt at
// restore time — node_modules by `bench setup requirements`, public/dist by
// `bench build`, the rest by pip — and node_modules alone is routinely larger
// than the database.
var appSourceExcludes = []string{"node_modules", "__pycache__", "*.pyc", "*.egg-info"}

// appSourceTarCmd returns the script that archives one app into $2, with the
// app name as $1. GNU tar exits 1 when a file changed while it was read — a
// log or a .pyc on a live bench — which is not worth failing the backup over;
// 2 and above are real errors.
func appSourceTarCmd() string {
	var b strings.Builder
	b.WriteString(`cd /workspace/frappe-bench/apps && tar`)
	for _, ex := range appSourceExcludes {
		b.WriteString(" --exclude=" + bench.ShellQuote(ex))
	}
	b.WriteString(` --exclude="$1/$1/public/dist" -czf "$2" "$1"; rc=$?; [ "$rc" -le 1 ]`)
	return b.String()
}

// resolveVendorRequest validates --vendor-apps against the apps on the bench.
// "all" selects every app.
func resolveVendorRequest(requested, present []string) (map[string]bool, error) {
	out := make(map[string]bool, len(requested))
	have := make(map[string]bool, len(present))
	for _, name := range present {
		have[name] = true
	}
	var unknown []string
	for _, name := range requested {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
			continue
		case name == "all":
			for _, p := range present {
				out[p] = true
			}
		case have[name]:
			out[name] = true
		default:
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(present)
		return nil, fmt.Errorf("--vendor-apps: no app named %s on this bench (apps: %s)",
			strings.Join(unknown, ", "), strings.Join(present, ", "))
	}
	return out, nil
}

// vendorCandidates is the set of apps whose source is worth archiving: the ones
// a restore will actually need. An app lying in apps/ that the site never
// installed and the bench record never asked for is skipped — a restore does
// not fetch it either, and its source would only inflate every archive.
func vendorCandidates(installed, recordApps []string, forced map[string]bool) map[string]bool {
	out := map[string]bool{"frappe": true}
	for _, a := range installed {
		out[a] = true
	}
	for _, raw := range recordApps {
		out[bench.ParseAppSpec(raw, "").DisplayName()] = true
	}
	for a := range forced {
		out[a] = true
	}
	return out
}
