package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// BenchApp is one app of a bench, as ffm app list reports it.
type BenchApp struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Installed bool   `json:"installed"` // on the site, not only in apps/
}

// listAppsScript prints, for every app in apps.txt, its version, branch and
// commit, and whether the site has it installed.
const listAppsScript = `import sys, json, subprocess, frappe
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
installed = set(frappe.get_installed_apps())
out = []
for app in frappe.get_all_apps(with_internal_apps=False, sites_path="."):
    path = frappe.get_app_path(app, "..")
    def git(*a):
        try:
            return subprocess.check_output(["git", "-C", path] + list(a), stderr=subprocess.DEVNULL, text=True).strip()
        except Exception:
            return ""
    out.append({"name": app, "version": getattr(frappe.get_module(app), "__version__", ""),
        "branch": git("rev-parse", "--abbrev-ref", "HEAD"), "commit": git("rev-parse", "--short", "HEAD"),
        "installed": app in installed})
print("FFM_APPS=" + json.dumps(out))
`

// ListApps lists the bench's apps and whether the site has them installed.
func (s *Service) ListApps(name string) ([]BenchApp, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return nil, err
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return nil, fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}
	out, err := runSiteScript(s.quietRunnerFor(b), b.SiteName, listAppsScript)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w\n%s", err, lastLines(out, 5))
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "FFM_APPS="); ok {
			var apps []BenchApp
			if err := json.Unmarshal([]byte(v), &apps); err != nil {
				return nil, err
			}
			return apps, nil
		}
	}
	return nil, fmt.Errorf("list apps: no result in the output")
}

func readAppsTxt(b state.Bench) []string {
	raw, _ := os.ReadFile(filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", "apps.txt"))
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// AppAddInput adds an app to a bench and installs it on the site.
type AppAddInput struct {
	Name        string
	Spec        string // as --apps takes it: erpnext, erpnext@version-16, a git URL[@branch]
	GithubToken string
	// NoInstall only fetches the app into the bench.
	NoInstall bool
	Out       io.Writer
}

// AppAdd fetches an app (bench get-app), installs it on the site, builds its
// assets and restarts the bench's processes, then records it so recreate and
// backups know about it.
func (s *Service) AppAdd(in AppAddInput, pw ProgressWriter) (string, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return "", err
	}
	defer release()
	b, err := s.GetBench(in.Name)
	if err != nil {
		return "", err
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return "", fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}
	spec := bench.ParseAppSpec(in.Spec, b.FrappeBranch)
	runner := s.runnerFor(b)
	before := readAppsTxt(b)

	if in.GithubToken != "" {
		if err := runner.ConfigureGitHubToken(in.GithubToken); err != nil {
			return "", fmt.Errorf("configure GitHub token: %w", err)
		}
		defer runner.CleanupGitHubToken()
	}
	pw.Step(fmt.Sprintf("Getting %s", spec.DisplayName()))
	if out, err := runner.ExecSilent("frappe", "bash", "-c", "cd /workspace/frappe-bench && "+spec.GetAppCmd()); err != nil {
		return "", fmt.Errorf("bench get-app: %w\n%s", err, lastLines(out, 10))
	}
	// The app's name is what get-app added to apps.txt; a repository is often
	// named differently from the app it holds.
	app := ""
	for _, a := range readAppsTxt(b) {
		if !slices.Contains(before, a) {
			app = a
		}
	}
	if app == "" {
		app = spec.DisplayName()
	}
	if !in.NoInstall {
		pw.Step(fmt.Sprintf("Installing %s on %s", app, b.SiteName))
		if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd(b, "install-app "+bench.ShellQuote(app))); err != nil {
			return app, fmt.Errorf("bench install-app %s: %w\n%s", app, err, lastLines(scrubSecrets(out, benchSecrets(b)...), 10))
		}
	}
	pw.Step("Building " + app + "'s assets")
	if out, err := runner.ExecSilent("frappe", "bash", "-c", "cd /workspace/frappe-bench && bench build --app "+bench.ShellQuote(app)); err != nil {
		return app, fmt.Errorf("bench build --app %s: %w\n%s", app, err, lastLines(out, 10))
	}
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.Apps = append(withoutApp(rec.Apps, app, b.FrappeBranch), in.Spec)
	}); err != nil {
		return app, fmt.Errorf("update state: %w", err)
	}
	pw.Step("Restarting the bench's processes")
	if err := s.restartAppProcesses(b, pw); err != nil {
		return app, err
	}
	return app, nil
}

// AppRemoveInput uninstalls an app from the site and removes it from the bench.
type AppRemoveInput struct {
	Name string
	App  string
	// KeepCode uninstalls from the site but leaves the code in apps/.
	KeepCode bool
	// NoSnapshot skips the snapshot taken first.
	NoSnapshot bool
}

// AppRemove uninstalls an app (its DocTypes and data go with it) after taking
// a snapshot, then removes its code unless KeepCode.
func (s *Service) AppRemove(in AppRemoveInput, pw ProgressWriter) (string, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	if in.App == "frappe" {
		return "", fmt.Errorf("frappe cannot be removed")
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return "", err
	}
	if !slices.Contains(readAppsTxt(b), in.App) {
		return "", fmt.Errorf("bench %q has no app %q (see: ffm app list %s)", b.Name, in.App, b.Name)
	}
	snap := ""
	if !in.NoSnapshot {
		snap, err = s.CreateSnapshot(SnapshotInput{Bench: b.Name, Name: "before-remove-" + in.App + "-" + s.clock().UTC().Format("20060102-150405")}, pw)
		if err != nil {
			return "", fmt.Errorf("snapshot before removing %s: %w", in.App, err)
		}
	}
	release, err := s.lockBench(b.Name)
	if err != nil {
		return snap, err
	}
	defer release()
	runner := s.runnerFor(b)
	pw.Step(fmt.Sprintf("Uninstalling %s from %s", in.App, b.SiteName))
	if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd(b, "uninstall-app "+bench.ShellQuote(in.App)+" --yes --no-backup --force")); err != nil {
		return snap, fmt.Errorf("bench uninstall-app %s: %w\n%s", in.App, err, lastLines(out, 10))
	}
	if !in.KeepCode {
		pw.Step("Removing " + in.App + " from the bench")
		if out, err := runner.ExecSilent("frappe", "bash", "-c", "cd /workspace/frappe-bench && bench remove-app "+bench.ShellQuote(in.App)+" --force"); err != nil {
			return snap, fmt.Errorf("bench remove-app %s: %w\n%s", in.App, err, lastLines(out, 10))
		}
	}
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) { rec.Apps = withoutApp(rec.Apps, in.App, b.FrappeBranch) }); err != nil {
		return snap, fmt.Errorf("update state: %w", err)
	}
	pw.Step("Restarting the bench's processes")
	return snap, s.restartAppProcesses(b, pw)
}

// withoutApp drops every spec that names app from a bench record's app list.
func withoutApp(specs []string, app, frappeBranch string) []string {
	var out []string
	for _, sp := range specs {
		if bench.ParseAppSpec(sp, frappeBranch).DisplayName() == app {
			continue
		}
		out = append(out, sp)
	}
	return out
}

// restartAppProcesses makes the bench's processes load changed apps: honcho
// on dev, the app containers on prod. It returns once the site answers ping.
func (s *Service) restartAppProcesses(b state.Bench, pw ProgressWriter) error {
	return s.restartAndWait(b, false)
}

// restartAndWait restarts the processes and waits for the site. In
// maintenance mode Frappe answers every request with 503, so then a 503 is
// what "up" looks like; the real check comes once maintenance is off.
func (s *Service) restartAndWait(b state.Bench, maintenance bool) error {
	runner := s.runnerFor(b)
	if b.IsDev() {
		if out, err := runner.ExecSilent("frappe", "bash", "-c", bench.DevServerRestartCmd); err != nil {
			return fmt.Errorf("restart the dev server: %w\n%s", err, lastLines(out, 5))
		}
	} else {
		for _, svc := range []string{"frappe", "socketio", "worker-long", "worker-short", "scheduler"} {
			if err := runner.RestartService(svc); err != nil {
				return fmt.Errorf("restart %s: %w", svc, err)
			}
		}
	}
	return waitForSite(b, maintenance, runner)
}

// waitForSite waits until the site answers /api/method/ping (or, in
// maintenance mode, its 503).
func waitForSite(b state.Bench, maintenance bool, runner *bench.Runner) error {
	host := ""
	if b.IsProd() {
		host = b.Domain
	}
	deadline := time.Now().Add(devServerWait)
	for {
		err := httpPing(fmt.Sprintf("http://localhost:%d/api/method/ping", b.WebPort), host)
		if err == nil || (maintenance && err.Error() == "HTTP 503") {
			return nil
		}
		if time.Now().After(deadline) {
			if runner == nil {
				return err
			}
			return webServerUnreachable(runner, b.IsDev(), err)
		}
		time.Sleep(2 * time.Second)
	}
}
