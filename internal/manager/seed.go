package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// A seed is a finished bench tree (frappe-bench without its site) that a later
// create with the same inputs copies instead of running bench init, get-app
// and bench build: minutes become seconds. It is taken at the end of a create
// that started from scratch. Restore, recreate and clone go through Create,
// so they use seeds too, but only a plain create takes one: a restore pins
// commits and unpacks archived source, so its tree is not the canonical one.

// seedFormat changes when the seed layout does; older seeds are then unused.
const seedFormat = 1

// seedMaxAge bounds how stale a seed's code may be. A seed freezes each app
// at the commit it was taken at, while bench init would fetch the branch head.
const seedMaxAge = 7 * 24 * time.Hour

// seedCommonConfig is where create keeps common_site_config.json as bench init
// wrote it. The bench's own copy gains database, Redis and Socket.IO settings
// of that bench, which must not travel to the next one.
const seedCommonConfig = ".ffm-seed-common_site_config.json"

// seedKey is everything that decides what a bench tree contains.
type seedKey struct {
	Format    int      `json:"format"`
	Image     string   `json:"image"`
	FrappeURL string   `json:"frappe_url,omitempty"`
	Branch    string   `json:"branch"`
	Python    string   `json:"python"`
	Node      string   `json:"node"`
	UID       int      `json:"uid,omitempty"`
	GID       int      `json:"gid,omitempty"`
	Apps      []string `json:"apps,omitempty"`
}

func (k seedKey) id() string {
	raw, _ := json.Marshal(k)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// seedMeta is <seed>/seed.json.
type seedMeta struct {
	Key       seedKey   `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	From      string    `json:"from_bench"`
}

// SeedInfo is one seed as listed.
type SeedInfo struct {
	ID        string
	Path      string
	Branch    string
	Apps      []string
	Python    string
	Node      string
	CreatedAt time.Time
	Size      int64
	Stale     bool
}

// seedsEnabled is false on Windows (the copy relies on cp and symlinks) and
// when FFM_NO_SEED is set.
func seedsEnabled() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	v := strings.ToLower(os.Getenv("FFM_NO_SEED"))
	return v == "" || v == "0" || v == "false"
}

func seedDir(k seedKey) string { return filepath.Join(config.SeedsDir(), k.id()) }

// freshSeed returns the seed's bench tree when a usable seed exists.
func freshSeed(k seedKey, now time.Time) (string, *seedMeta) {
	dir := seedDir(k)
	raw, err := os.ReadFile(filepath.Join(dir, "seed.json"))
	if err != nil {
		return "", nil
	}
	var m seedMeta
	if json.Unmarshal(raw, &m) != nil || m.Key.Format != seedFormat || now.Sub(m.CreatedAt) > seedMaxAge {
		return "", nil
	}
	tree := filepath.Join(dir, "bench")
	if _, err := os.Stat(filepath.Join(tree, "apps", "frappe")); err != nil {
		return "", nil
	}
	return tree, &m
}

// cpArgs copies a tree with modes, times and symlinks kept, cloning blocks
// where the filesystem can (APFS, Btrfs, XFS).
func cpArgs(src, dst string) []string {
	if runtime.GOOS == "darwin" {
		return []string{"-c", "-a", src, dst}
	}
	return []string{"-a", "--reflink=auto", src, dst}
}

func copyTree(src, dst string) error {
	out, err := execx.Command("cp", cpArgs(src, dst)...).CombinedOutput()
	if err != nil && runtime.GOOS == "darwin" {
		// -c needs APFS; fall back to a plain copy elsewhere.
		out, err = execx.Command("cp", "-a", src, dst).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("cp: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// seedKeepInSites are the only entries of sites/ a seed keeps: the app list
// and the built assets. Site directories hold data and secrets.
var seedKeepInSites = []string{"apps.txt", "apps.json", "assets", "common_site_config.json"}

// seedDrop are paths under frappe-bench a seed never carries.
var seedDrop = []string{"logs", "config/pids", ".agents", ".claude", ".mcp.json", ".vscode"}

// pruneSeedTree removes from a copied bench tree what belongs to one bench.
func pruneSeedTree(tree string) error {
	entries, err := os.ReadDir(filepath.Join(tree, "sites"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !slices.Contains(seedKeepInSites, e.Name()) {
			if err := os.RemoveAll(filepath.Join(tree, "sites", e.Name())); err != nil {
				return err
			}
		}
	}
	for _, p := range seedDrop {
		if err := os.RemoveAll(filepath.Join(tree, filepath.FromSlash(p))); err != nil {
			return err
		}
	}
	// bench only treats a directory as a bench when these exist, and refuses
	// set-config and friends otherwise (bench.utils.is_bench_directory).
	for _, p := range []string{"logs", "config/pids"} {
		if err := os.MkdirAll(filepath.Join(tree, filepath.FromSlash(p)), 0o755); err != nil {
			return err
		}
	}
	top, err := os.ReadDir(tree)
	if err != nil {
		return err
	}
	for _, e := range top {
		if strings.HasPrefix(e.Name(), ".ffm-") {
			if err := os.RemoveAll(filepath.Join(tree, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// captureSeed saves a finished bench tree as the seed for k.
func captureSeed(k seedKey, frappeBench, from string, now time.Time) error {
	pristine := filepath.Join(frappeBench, seedCommonConfig)
	if _, err := os.Stat(pristine); err != nil {
		return fmt.Errorf("no pristine common_site_config.json to seed with")
	}
	if err := os.MkdirAll(config.SeedsDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(config.SeedsDir(), ".partial-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tree := filepath.Join(tmp, "bench")
	if err := copyTree(frappeBench, tree); err != nil {
		return err
	}
	raw, err := os.ReadFile(pristine)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tree, "sites", "common_site_config.json"), raw, 0o644); err != nil {
		return err
	}
	if err := pruneSeedTree(tree); err != nil {
		return err
	}
	meta, _ := json.MarshalIndent(seedMeta{Key: k, CreatedAt: now.UTC(), From: from}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "seed.json"), append(meta, '\n'), 0o644); err != nil {
		return err
	}
	dst := seedDir(k)
	// A stale seed is replaced; a concurrent create that finished first wins.
	if _, fresh := freshSeed(k, now); fresh != nil {
		return nil
	}
	_ = os.RemoveAll(dst)
	return os.Rename(tmp, dst)
}

// ListSeeds returns the seeds on this host, newest first.
func (s *Service) ListSeeds() ([]SeedInfo, error) {
	entries, err := os.ReadDir(config.SeedsDir())
	if os.IsNotExist(err) {
		return []SeedInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []SeedInfo{}
	for _, e := range entries {
		dir := filepath.Join(config.SeedsDir(), e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "seed.json"))
		if err != nil || !e.IsDir() {
			continue
		}
		var m seedMeta
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		out = append(out, SeedInfo{ID: e.Name(), Path: dir, Branch: m.Key.Branch, Apps: m.Key.Apps,
			Python: m.Key.Python, Node: m.Key.Node, CreatedAt: m.CreatedAt, Size: dirSize(dir),
			Stale: m.Key.Format != seedFormat || s.clock().Sub(m.CreatedAt) > seedMaxAge})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// seedKeyFor builds the key of a create's bench tree. appBranch is the branch
// a short app name defaults to (the bench's Frappe branch).
func seedKeyFor(frappeURL, branch string, tc bench.Toolchain, uid, gid int, apps []string, appBranch string) seedKey {
	canon := make([]string, 0, len(apps))
	for _, a := range apps {
		canon = append(canon, canonicalApp(a, appBranch))
	}
	return seedKey{Format: seedFormat, Image: bench.BenchImageTag, FrappeURL: canonicalRepo(frappeURL), Branch: branch,
		Python: tc.Python, Node: tc.Node, UID: uid, GID: gid, Apps: canon}
}

// canonicalApp spells an --apps value the way bench resolves it, so that
// "erpnext" on a version-15 bench and the
// "https://github.com/frappe/erpnext.git@version-15" a restore passes share a
// seed: bench get-app clones a short name from github.com/frappe.
func canonicalApp(raw, frappeBranch string) string {
	spec := bench.ParseAppSpec(raw, frappeBranch)
	src := spec.Source
	if !spec.IsURL {
		src = "github.com/frappe/" + src
	}
	return canonicalRepo(src) + "@" + spec.Branch
}

// canonicalRepo reduces a git URL to host/path: no scheme, user, ".git" or
// trailing slash, and SSH's host:path written as host/path.
func canonicalRepo(u string) string {
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.IndexAny(u+"/", "/:") {
		u = u[at+1:]
	}
	if i := strings.Index(u, ":"); i >= 0 && i < strings.Index(u+"/", "/") {
		u = u[:i] + "/" + u[i+1:]
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	if i := strings.Index(u, "/"); i >= 0 {
		u = strings.ToLower(u[:i]) + u[i:]
	}
	return u
}
