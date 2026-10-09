package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/project"
)

// loadProject finds and reads ffm.yaml: the given path, or the nearest one
// from the working directory up to the repository root.
func loadProject(path string) (project.File, error) {
	if path == "" {
		var err error
		if path, err = project.Find("."); err != nil {
			return project.File{}, err
		}
	}
	return project.Load(path)
}

// ensureTrusted decides whether the file's hooks and tooling may run. A file
// without commands needs nothing. Otherwise it must be trusted as it is now;
// interactively the user is shown every command and asked.
func ensureTrusted(f project.File, assumeYes bool) (bool, error) {
	if !f.HasCommands() {
		return true, nil
	}
	ok, err := project.Trusted(f)
	if err != nil || ok {
		return ok, err
	}
	if !assumeYes {
		if !isInteractive() {
			return false, usageError{fmt.Errorf("%s runs commands in the bench and is not trusted in its current form: "+
				"review it, then run 'ffm trust' (or pass --no-hooks)", f.Path)}
		}
		printCommands(f)
		yes, err := confirm("Trust these commands? They run inside the bench whenever ffm needs them.")
		if err != nil || !yes {
			return false, err
		}
	}
	return true, project.Trust(f)
}

func printCommands(f project.File) {
	fmt.Fprintf(os.Stderr, "%s runs these commands inside the bench:\n", f.Path)
	for _, c := range f.Commands() {
		fmt.Fprintf(os.Stderr, "  %s\n", sanitizeTerminal(c))
	}
}

func newUpCmd() *cobra.Command {
	var (
		file         string
		noHooks, yes bool
	)
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create or start the development bench this project's ffm.yaml describes",
		Long: `Read ffm.yaml (in this directory or a parent, up to the repository root) and
create the development bench it describes, or start it when it exists and is
stopped. An existing bench is not changed to match the file; apps the file
lists and the bench lacks are reported.

ffm.yaml:
  version: 1
  name: myproject            # bench name; default: the directory's name
  frappe:
    branch: version-16       # default version-16
    repo: https://github.com/frappe/frappe   # optional fork
  python: "3.14"             # optional; default per branch
  node: "24"                 # optional
  db: mariadb                # or postgres
  apps: [erpnext, hrms@version-16]
  hooks:                     # run inside the bench, from /workspace/frappe-bench
    post_create: ["bench --site $SITE set-config developer_mode 1"]
    post_update: ["bench --site $SITE clear-cache"]
  tooling:                   # ffm run <name> [args...]
    lint: ruff check apps/myapp
    tests: { cmd: "bench --site $SITE run-tests --app myapp", description: "App tests" }

Hooks and tooling run only from a file you trusted in its current form
(ffm trust); any change to the file asks again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := loadProject(file)
			if err != nil {
				return err
			}
			runHooks := false
			if !noHooks {
				if runHooks, err = ensureTrusted(f, yes); err != nil {
					return err
				}
				if !runHooks && f.HasCommands() {
					fmt.Println("Not trusted; nothing was done. ffm up --no-hooks brings the bench up without running them.")
					return nil
				}
			}
			res, err := manager.New(verbose).Up(manager.UpInput{File: f, RunHooks: runHooks, Out: os.Stdout}, manager.CLIProgress{})
			if err != nil {
				return err
			}
			switch res.Action {
			case "created":
				fmt.Printf("Created %q from %s.\n", res.Bench, f.Path)
			case "started":
				fmt.Printf("Started %q.\n", res.Bench)
			default:
				fmt.Printf("%q is already running.\n", res.Bench)
			}
			if len(res.MissingApps) > 0 {
				fmt.Printf("ffm.yaml lists apps the bench does not have: %s\nAdd them with: ffm app add <app> %s\n",
					strings.Join(res.MissingApps, ", "), res.Bench)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path of the project file (default: find ffm.yaml)")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Do not run the file's hooks")
	cmd.Flags().BoolVar(&yes, "trust", false, "Trust the file's commands without asking")
	return cmd
}

func newTrustCmd() *cobra.Command {
	var (
		file        string
		yes, revoke bool
	)
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Allow this project's ffm.yaml hooks and tooling to run",
		Long: `Show every command ffm.yaml can run (hooks and tooling) and record the file's
current content as trusted. Any later change to the file, including one pulled
from git, needs 'ffm trust' again. --revoke forgets the file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := loadProject(file)
			if err != nil {
				return err
			}
			if revoke {
				had, err := project.Revoke(f.Path)
				if err != nil {
					return err
				}
				if had {
					fmt.Printf("%s is no longer trusted.\n", f.Path)
				} else {
					fmt.Printf("%s was not trusted.\n", f.Path)
				}
				return nil
			}
			if !f.HasCommands() {
				fmt.Printf("%s runs no commands; nothing to trust.\n", f.Path)
				return nil
			}
			if ok, err := project.Trusted(f); err != nil {
				return err
			} else if ok {
				fmt.Printf("%s is already trusted in its current form.\n", f.Path)
				return nil
			}
			printCommands(f)
			if !yes {
				if !isInteractive() {
					return mustNotPrompt("trust", "review the commands above and pass --yes")
				}
				ok, err := confirm("Trust these commands?")
				if err != nil || !ok {
					return err
				}
			}
			if err := project.Trust(f); err != nil {
				return err
			}
			fmt.Printf("Trusted %s.\n", f.Path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path of the project file (default: find ffm.yaml)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Trust without asking (the commands are still printed)")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "Forget the file")
	return cmd
}

func newRunCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "run [tool] [args...]",
		Short: "Run a tooling command from this project's ffm.yaml in its bench",
		Long: `Run a named command from the tooling section of ffm.yaml inside the project's
bench, from /workspace/frappe-bench, with any further arguments appended.
Without a tool name, list the tools. Flags after the tool name are passed on.`,
		Example: `  ffm run
  ffm run lint
  ffm run tests --failfast`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := loadProject(file)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if len(f.Tooling) == 0 {
					fmt.Printf("%s has no tooling.\n", f.Path)
					return nil
				}
				for _, n := range f.ToolNames() {
					t := f.Tooling[n]
					desc := t.Description
					if desc == "" {
						desc = t.Cmd
					}
					fmt.Printf("  %-16s %s\n", n, sanitizeTerminal(desc))
				}
				return nil
			}
			tool, ok := f.Tooling[args[0]]
			if !ok {
				return usageError{fmt.Errorf("%s has no tool %q (ffm run lists them)", f.Path, args[0])}
			}
			if ok, err := project.Trusted(f); err != nil {
				return err
			} else if !ok {
				return usageError{errors.New("ffm.yaml is not trusted in its current form: review it, then run 'ffm trust'")}
			}
			svc := manager.New(verbose)
			b, err := svc.GetBench(f.Name)
			if err != nil {
				return fmt.Errorf("%w (create it with ffm up)", err)
			}
			if b.ProjectFile != f.Path {
				return fmt.Errorf("bench %q was not created from %s", b.Name, f.Path)
			}
			return svc.RunTool(b, tool.Cmd, args[1:], os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path of the project file (default: find ffm.yaml)")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// runPostUpdateHooks runs the post_update hooks of the ffm.yaml a bench was
// created from, when it still exists and is trusted.
func runPostUpdateHooks(svc *manager.Service, name string) error {
	b, err := svc.GetBench(name)
	if err != nil || b.ProjectFile == "" {
		return err
	}
	f, err := project.Load(b.ProjectFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: post_update hooks not run: %v\n", err)
		return nil
	}
	if len(f.Hooks.PostUpdate) == 0 {
		return nil
	}
	if ok, err := project.Trusted(f); err != nil || !ok {
		fmt.Fprintf(os.Stderr, "warning: post_update hooks not run: %s is not trusted in its current form (ffm trust)\n", f.Path)
		return err
	}
	if err := svc.RunHooks(b, "post_update", f.Hooks.PostUpdate, os.Stdout, manager.CLIProgress{}); err != nil {
		return fmt.Errorf("the update succeeded, but %w", err)
	}
	return nil
}
