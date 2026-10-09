package manager

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/project"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// UpInput brings up the bench an ffm.yaml describes.
type UpInput struct {
	File project.File
	// RunHooks runs the file's post_create hooks after a create. The caller
	// decides it: only for a file the user trusted.
	RunHooks bool
	// Out receives hook output.
	Out io.Writer
}

// UpResult says what ffm up did.
type UpResult struct {
	Bench string
	// Action is "created", "started" or "running".
	Action string
	// MissingApps are apps the file lists that the bench does not have.
	MissingApps []string
}

// Up creates the bench an ffm.yaml describes, or starts it when it exists
// and is stopped. An existing bench is not changed to match the file: apps
// the file lists and the bench lacks are reported, for 'ffm app add'.
func (s *Service) Up(in UpInput, pw ProgressWriter) (UpResult, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	f := in.File
	res := UpResult{Bench: f.Name}
	b, err := s.GetBench(f.Name)
	switch {
	case errors.Is(err, state.ErrNotFound):
		ci := CreateInput{
			Name: f.Name, Mode: "dev", ProjectFile: f.Path,
			FrappeBranch: f.Frappe.Branch, FrappeRepo: f.Frappe.Repo,
			Python: f.Python, Node: f.Node, DBType: f.DB, Apps: f.Apps,
			AdminPassword: "admin", DBPassword: "ffm123456",
			Bind: BindFor(false),
		}
		if ci.FrappeBranch == "" {
			ci.FrappeBranch = bench.DefaultFrappeBranch
		}
		if ci.DBType == "" {
			ci.DBType = "mariadb"
		}
		if err := s.Create(ci, pw); err != nil {
			return res, err
		}
		res.Action = "created"
		if in.RunHooks && len(f.Hooks.PostCreate) > 0 {
			b, err := s.GetBench(f.Name)
			if err != nil {
				return res, err
			}
			if err := s.RunHooks(b, "post_create", f.Hooks.PostCreate, in.Out, pw); err != nil {
				return res, err
			}
		}
		return res, nil
	case err != nil:
		return res, err
	}

	if b.ProjectFile != f.Path {
		owner := "was not created from an ffm.yaml"
		if b.ProjectFile != "" {
			owner = "belongs to " + b.ProjectFile
		}
		return res, fmt.Errorf("a bench named %q already exists and %s: set another name: in %s, or delete that bench", b.Name, owner, f.Path)
	}
	if b.IsProd() {
		return res, fmt.Errorf("%q is a production bench; ffm up manages development benches only", b.Name)
	}
	if st := s.LiveStatus(b); st == StatusRunning {
		res.Action = "running"
	} else {
		if err := s.Start(b.Name, pw); err != nil {
			return res, err
		}
		res.Action = "started"
	}
	res.MissingApps = missingApps(f.Apps, b.Apps)
	return res, nil
}

// missingApps returns the specs in want whose app the bench does not have,
// comparing app names (see appSpecName).
func missingApps(want, have []string) []string {
	got := map[string]bool{}
	for _, a := range have {
		got[appSpecName(a)] = true
	}
	var out []string
	for _, a := range want {
		if !got[appSpecName(a)] {
			out = append(out, a)
		}
	}
	return out
}

// appSpecName is the app a spec names: the last path element of its source,
// without @branch or .git, lowercase, hyphens as underscores.
func appSpecName(spec string) string {
	name := strings.TrimSuffix(strings.TrimRight(bench.ParseAppSpec(spec, "").Source, "/"), ".git")
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
}

// RunHooks runs each command in the bench's frappe container from
// /workspace/frappe-bench, stopping at the first failure. SITE and BENCH are
// set for the commands.
func (s *Service) RunHooks(b state.Bench, phase string, cmds []string, out io.Writer, pw ProgressWriter) error {
	if out == nil {
		out = io.Discard
	}
	if pw == nil {
		pw = CLIProgress{}
	}
	runner := s.runnerFor(b)
	for i, c := range cmds {
		pw.Step(fmt.Sprintf("Running %s hook %d of %d: %s", phase, i+1, len(cmds), oneLine(c)))
		if err := runner.ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", hookEnv(b)+c); err != nil {
			return fmt.Errorf("%s hook %q: %w", phase, oneLine(c), err)
		}
	}
	return nil
}

// RunTool runs an ffm.yaml tooling command in the bench, with args appended
// (each shell-quoted), its output going to out and errOut.
func (s *Service) RunTool(b state.Bench, cmd string, args []string, out, errOut io.Writer) error {
	if st := s.LiveStatus(b); st != StatusRunning {
		return fmt.Errorf("%w: start it first (ffm up)", ErrBenchStopped)
	}
	script := hookEnv(b) + cmd
	for _, a := range args {
		script += " " + bench.ShellQuote(a)
	}
	return s.runnerFor(b).ExecTo("frappe", "/workspace/frappe-bench", out, errOut, "bash", "-c", script)
}

func hookEnv(b state.Bench) string {
	return "export SITE=" + bench.ShellQuote(b.SiteName) + " BENCH=" + bench.ShellQuote(b.Name) + "; "
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 80 {
		s = s[:77] + "…"
	}
	return s
}
