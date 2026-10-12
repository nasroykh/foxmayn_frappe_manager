package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// A project mount bind-mounts the host checkout of an app over
// apps/<app> of a dev bench (ffm up inside an app repository), so host edits
// are live. The checkout belongs to the user and must survive every ffm
// operation; the rules that keep it safe:
//
//   - The mount exists only in the container. Host-side RemoveAll of the bench
//     directory sees an empty mountpoint, never the checkout — provided the
//     checkout is not itself inside, or around, a directory ffm deletes from
//     (checkMountOutsideFFM).
//   - It is NOT in the compose file while bench init and the seed copy run
//     (`compose run` uses the same service definition, and bench init does
//     rm -rf /workspace/frappe-bench). Create renders it afterwards.
//   - `bench remove-app` deletes apps/<app>, so ffm app remove refuses it.
//   - ffm update leaves the app's git alone (no pull, switch, reset, prune).
//   - Docker creates a missing mountpoint root-owned, so ffm creates it first,
//     and the compose entry has create_host_path: false so a moved checkout
//     fails instead of Docker making a root-owned directory at the source.

// projectMountOf returns the app and checkout of a dev bench's project mount.
func projectMountOf(b state.Bench) (app, path string, ok bool) {
	if b.IsDev() && b.ProjectApp != "" && b.ProjectAppPath != "" {
		return b.ProjectApp, b.ProjectAppPath, true
	}
	return "", "", false
}

// checkProjectMount verifies the checkout is where it was mounted from and
// still holds the app, so a start fails with a clear message instead of
// Docker's.
func checkProjectMount(app, path string) error {
	if err := bench.ValidateProjectMount(app, path); err != nil {
		return err
	}
	if err := checkMountOutsideFFM(path); err != nil {
		return err
	}
	hooks := filepath.Join(path, app, "hooks.py")
	if _, err := os.Stat(hooks); err != nil {
		return fmt.Errorf("the app %s is a checkout mounted from %s, which is missing or no longer holds %s "+
			"(moved or deleted?): put it back at that path, or delete this bench and run ffm up from the checkout's new location",
			app, path, filepath.Join(app, "hooks.py"))
	}
	return nil
}

// checkMountOutsideFFM refuses a checkout that overlaps, in either direction,
// a directory ffm removes trees from: the benches (create's rollback and
// delete run RemoveAll on a bench directory), the backups and the seeds. A
// checkout cloned to ~/frappe/<app> would otherwise become the bench
// directory of `ffm up` with the default name, and the first rollback would
// delete it.
func checkMountOutsideFFM(path string) error {
	for _, d := range []struct{ what, dir string }{
		{"the benches directory", config.BenchesDir()},
		{"the backups directory", config.BackupsDir()},
		{"the seeds directory", config.SeedsDir()},
	} {
		dir, err := canonicalDir(d.dir)
		if err != nil {
			// Not created yet: compare the absolute path.
			if dir, err = filepath.Abs(d.dir); err != nil {
				continue
			}
		}
		if pathWithin(path, dir) || pathWithin(dir, path) {
			return fmt.Errorf("the checkout %s overlaps %s (%s), where ffm deletes bench files: "+
				"move the checkout elsewhere (for example ~/src/<app>) and run ffm up there", path, d.what, dir)
		}
	}
	return nil
}

// pathWithin reports whether path is dir or below it.
func pathWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalDir returns dir as an absolute path with symlinks resolved.
func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// readOSRelease returns the kernel's release string; tests replace it.
var readOSRelease = func() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return string(raw), err
}

var wslDriveRe = regexp.MustCompile(`^/mnt/[A-Za-z]/`)

// wslMountWarning returns a warning when path is a Windows drive seen from
// WSL (/mnt/<letter>/…), or "" otherwise.
func wslMountWarning(path string) string {
	rel, err := readOSRelease()
	if err != nil || !strings.Contains(strings.ToLower(rel), "microsoft") || !wslDriveRe.MatchString(path+"/") {
		return ""
	}
	return fmt.Sprintf("%s is on a Windows drive (WSL2 /mnt). The mount goes through 9p, which is slow for "+
		"Python and node_modules; edits made from Windows may not trigger reloads; and file modes are ignored. "+
		"Clone the project into the Linux filesystem (for example under your home directory) and run ffm up there.", path)
}

// registerProjectApp makes the mounted checkout a working app of the bench:
// in apps.txt, its Python and Node requirements installed, installed on the
// site. The caller builds its assets (the whole-bench build, or --app).
func registerProjectApp(runner *bench.Runner, benchDir, siteName, app string, pw ProgressWriter) error {
	if err := setupProjectAppRequirements(runner, benchDir, app, pw); err != nil {
		return err
	}
	pw.Step(fmt.Sprintf("Installing %q on %q", app, siteName))
	installCmd := "cd /workspace/frappe-bench && bench --site " + bench.ShellQuote(siteName) + " install-app " + bench.ShellQuote(app)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", installCmd); err != nil {
		return fmt.Errorf("bench install-app %s: %w\n%s", app, err, lastLines(out, 10))
	}
	return nil
}

// setupProjectAppRequirements is registerProjectApp without the site install:
// the app in apps.txt and its Python and Node requirements installed. A
// worktree bench restored from its source already has the app installed.
//
// It follows installArchivedApps: bare `bench setup requirements` opens the
// app as a git repository, so the --python and --node halves run alone.
func setupProjectAppRequirements(runner *bench.Runner, benchDir, app string, pw ProgressWriter) error {
	pw.Step(fmt.Sprintf("Registering the mounted checkout as the app %q", app))
	if err := syncAppsTxt(state.Bench{Dir: benchDir}, []string{app}); err != nil {
		return fmt.Errorf("register %s in sites/apps.txt: %w", app, err)
	}
	pw.Step(fmt.Sprintf("Installing the requirements of %q — this may take a few minutes", app))
	for _, part := range []string{"--python", "--node"} {
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && bench setup requirements "+part+" "+bench.ShellQuote(app)); err != nil {
			return fmt.Errorf("bench setup requirements %s %s: %w\n%s", part, app, err, lastLines(out, 10))
		}
	}
	return nil
}

// worktreeLink is what attachProjectMount records on a bench it mounts a git
// worktree into: the bench the worktree was made from, its branch, whether
// ffm created that branch, and the extra host that routes to the bench.
type worktreeLink struct {
	Of            string
	Branch        string
	CreatedBranch bool
	Alias         string
}

// attachProjectMount mounts the checkout at path over apps/<app> of an
// existing dev bench, which a clone just restored with the app as an ordinary
// clone. The caller holds the bench's lock.
//
// The only thing it deletes is apps/<app> inside the bench directory, ffm's
// own clone of the app: never the checkout. It refuses a bench that already
// mounts something and an apps/<app> that is not a plain directory (a symlink
// there could lead anywhere).
func (s *Service) attachProjectMount(name, app, path string, wt worktreeLink, pw ProgressWriter) error {
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	if !b.IsDev() {
		return fmt.Errorf("bench %q is not a dev bench: a project checkout mounts into dev benches only", name)
	}
	if err := checkProjectMount(app, path); err != nil {
		return err
	}
	if b.ProjectApp != "" || b.ProjectAppPath != "" {
		return fmt.Errorf("bench %q already mounts %s at apps/%s; refusing to replace it", name, b.ProjectAppPath, b.ProjectApp)
	}

	appDir := filepath.Join(b.Dir, "workspace", "frappe-bench", "apps", app)
	if !pathWithin(appDir, b.Dir) || appDir == b.Dir {
		return fmt.Errorf("refusing to touch %s: it is not inside the bench directory %s", appDir, b.Dir)
	}
	switch fi, err := os.Lstat(appDir); {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%s is not a plain directory (a symlink or a file): refusing to replace it", appDir)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("inspect %s: %w", appDir, err)
	}

	pw.Step(fmt.Sprintf("Mounting %s as apps/%s", path, app))
	if err := os.RemoveAll(appDir); err != nil {
		return fmt.Errorf("remove the bench's own copy of %s: %w", app, err)
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return fmt.Errorf("create the mountpoint for %s: %w", app, err)
	}
	if err := s.UpdateBench(name, func(rec *state.Bench) {
		rec.ProjectApp, rec.ProjectAppPath = app, path
		rec.WorktreeOf, rec.WorktreeBranch, rec.WorktreeCreatedBranch = wt.Of, wt.Branch, wt.CreatedBranch
		// Not in Apps: recreate would get-app it over the mount.
		rec.Apps = slices.DeleteFunc(append([]string(nil), rec.Apps...), func(a string) bool { return appSpecName(a) == app })
		if wt.Alias != "" && !slices.Contains(rec.DomainAliases, wt.Alias) {
			rec.DomainAliases = append(append([]string(nil), rec.DomainAliases...), wt.Alias)
		}
	}); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	if b, err = s.GetBench(name); err != nil {
		return err
	}
	if err := s.applyDomainChange(b, pw); err != nil {
		return err
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return fmt.Errorf("bench %q is %s after the mount was applied; it must be running to register the app", name, st)
	}

	runner := s.runnerFor(b)
	if err := setupProjectAppRequirements(runner, b.Dir, app, pw); err != nil {
		return err
	}
	pw.Step("Migrating the site to the worktree's code")
	if err := s.migrateLocked(b, pw.Stderr()); err != nil {
		return err
	}
	pw.Step(fmt.Sprintf("Building the assets of %q", app))
	if out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && bench build --app "+bench.ShellQuote(app)); err != nil {
		return fmt.Errorf("bench build --app %s: %w\n%s", app, err, lastLines(out, 10))
	}
	return nil
}
