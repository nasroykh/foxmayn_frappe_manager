package manager

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// TestInput runs an app's tests on a dev bench's site.
type TestInput struct {
	Name string
	App  string
	// Module, Doctype and Test narrow the run (run-tests --module, --doctype,
	// --test). Test needs Module or Doctype.
	Module  string
	Doctype string
	Test    string
	// JUnit, when set, is a host path for a JUnit XML report.
	JUnit    string
	Failfast bool
	// Out receives the test output as it runs.
	Out io.Writer
}

// ErrTestsFailed is returned when the tests ran and at least one failed.
var ErrTestsFailed = fmt.Errorf("tests failed")

// Test runs `bench --site <site> run-tests --app <app>` on a dev bench.
//
// It runs on the bench's own site: Frappe's tests create and delete records,
// which is why production benches are refused. The site needs allow_tests,
// which Test sets the first time.
func (s *Service) Test(in TestInput) error {
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	if !b.IsDev() {
		return fmt.Errorf("ffm test runs on dev benches only: Frappe's tests write to the site's database")
	}
	if in.App == "" {
		return fmt.Errorf("an app is required")
	}
	if in.Test != "" && in.Module == "" && in.Doctype == "" {
		return fmt.Errorf("--test needs --module or --doctype")
	}
	out := in.Out
	if out == nil {
		out = io.Discard
	}
	enabled := false
	if err := patchSiteConfig(b, func(cfg map[string]any) {
		if v, ok := cfg["allow_tests"]; !ok || v == false || v == 0.0 {
			cfg["allow_tests"] = true
			enabled = true
		}
	}); err != nil {
		return fmt.Errorf("enable tests in site_config.json: %w", err)
	}
	if enabled {
		fmt.Fprintf(out, "Enabled allow_tests on %s (tests create and delete records in this site).\n", b.SiteName)
	}

	runner := s.runnerFor(b)
	// Frappe's test helpers (and --junit) import its dev dependencies, which
	// bench init does not install.
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && env/bin/python -c "+bench.ShellQuote(devDepsProbe)); err != nil {
		fmt.Fprintln(out, "Installing the apps' test dependencies (bench setup requirements --dev, once)...")
		if o, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && bench setup requirements --dev"); err != nil {
			return fmt.Errorf("install test dependencies: %w\n%s", err, lastLines(o, 10))
		}
	}
	before, _ := runner.ExecSilent("frappe", "bash", "-c", appChangesCmd)

	q := bench.ShellQuote
	cmd := "bench --site " + q(b.SiteName) + " run-tests --app " + q(in.App)
	if in.Module != "" {
		cmd += " --module " + q(in.Module)
	}
	if in.Doctype != "" {
		cmd += " --doctype " + q(in.Doctype)
	}
	if in.Test != "" {
		cmd += " --test " + q(in.Test)
	}
	if in.Failfast {
		cmd += " --failfast"
	}
	// The report is written inside the workspace, which the host sees
	// through the bind mount, then moved to where the caller asked.
	var reportName string
	if in.JUnit != "" {
		reportName = fmt.Sprintf(".ffm-junit-%d.xml", time.Now().UnixNano())
		cmd += " --junit-xml-output /workspace/frappe-bench/" + reportName
	}
	runErr := runner.ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", cmd)

	// In developer mode a test that saves a DocType exports it to the app's
	// JSON, so a run can leave the source tree modified. Say so.
	if after, err := runner.ExecSilent("frappe", "bash", "-c", appChangesCmd); err == nil {
		if changed := newLines(before, after); len(changed) > 0 {
			fmt.Fprintf(out, "\nThe run modified %d file(s) under apps/ (developer mode exports DocTypes that tests save):\n", len(changed))
			for _, l := range changed {
				fmt.Fprintln(out, "  "+l)
			}
			fmt.Fprintln(out, "Review them with git diff before committing.")
		}
	}

	if reportName != "" {
		src := filepath.Join(b.Dir, "workspace", "frappe-bench", reportName)
		if st, err := os.Stat(src); err == nil && st.Size() == 0 {
			// The runner opens the report before it can fail; an empty
			// file is no report.
			os.Remove(src)
		}
		if err := moveFile(src, in.JUnit); err != nil {
			if runErr == nil {
				return fmt.Errorf("JUnit report: %w", err)
			}
			fmt.Fprintf(out, "warning: no JUnit report: %v\n", err)
		}
	}
	if runErr != nil {
		return fmt.Errorf("%w (%v)", ErrTestsFailed, runErr)
	}
	return nil
}

// moveFile moves src to dst, copying when they are on different filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// devDepsProbe imports the dev dependencies Frappe's tests and --junit use.
const devDepsProbe = "import xmlrunner, freezegun, faker, hypothesis, responses"

// appChangesCmd lists the uncommitted changes of every app, one per line.
const appChangesCmd = `cd /workspace/frappe-bench/apps && for d in */; do git -C "$d" status --porcelain 2>/dev/null | sed "s|^...|${d}|"; done`

// newLines returns the lines of after that are not in before.
func newLines(before, after string) []string {
	seen := map[string]bool{}
	for _, l := range strings.Split(before, "\n") {
		seen[strings.TrimSpace(l)] = true
	}
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			out = append(out, l)
		}
	}
	return out
}
