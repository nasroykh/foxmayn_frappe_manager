package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type jsonApps struct {
	Schema string             `json:"schema"`
	Bench  string             `json:"bench"`
	Apps   []manager.BenchApp `json:"apps"`
}

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Add, remove and list a bench's apps",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	var asJSON bool
	list := &cobra.Command{
		Use:   "list [bench]",
		Short: "List the bench's apps: version, branch, commit, installed on the site",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			apps, err := manager.New(verbose).ListApps(name)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonApps{Schema: "ffm.apps/v1", Bench: name, Apps: apps})
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "APP\tVERSION\tBRANCH\tCOMMIT\tINSTALLED")
			for _, a := range apps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\n", a.Name, a.Version, a.Branch, a.Commit, onOffYes(a.Installed))
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.apps/v1)")

	var add manager.AppAddInput
	addCmd := &cobra.Command{
		Use:   "add <app-spec> [bench]",
		Short: "Get an app, install it on the site, build it and restart the bench",
		Long: `Add an app to a running bench: bench get-app, install-app on the site,
bench build --app, then a restart of the bench's processes. The spec is what
'ffm create --apps' takes: a name (erpnext, hrms), name@branch, or a git URL
with an optional @branch. It is recorded on the bench, so recreate and
backups know about it.`,
		Example: `  ffm app add hrms mybench
  ffm app add https://github.com/org/my_app@main mybench
  ffm app add git@github.com:org/private_app.git mybench`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			add.Name, add.Spec = name, args[0]
			app, err := manager.New(verbose).AppAdd(add, manager.CLIProgress{})
			if err != nil {
				return err
			}
			if add.NoInstall {
				fmt.Printf("Added %s to %q (not installed on the site).\n", app, name)
			} else {
				fmt.Printf("Added and installed %s on %q.\n", app, name)
			}
			return nil
		},
	}
	addCmd.Flags().StringVar(&add.GithubToken, "github-token", "", "GitHub token for a private HTTPS repository")
	addCmd.Flags().BoolVar(&add.NoInstall, "no-install", false, "Only fetch the app into the bench")

	var rm manager.AppRemoveInput
	var yes bool
	removeCmd := &cobra.Command{
		Use:   "remove <app> [bench]",
		Short: "Uninstall an app from the site (its DocTypes and data go too) and remove its code",
		Long: `Uninstall an app from the site, which deletes its DocTypes and their data,
then remove its code from the bench (--keep-code leaves the code). A snapshot
is taken first ('ffm snapshot restore' brings the data back; the code comes
back with 'ffm app add').`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if yes && len(args) < 2 {
				return usageError{fmt.Errorf("--yes requires the bench name: ffm app remove %s <bench> --yes", args[0])}
			}
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			if !yes {
				if !isInteractive() {
					return mustNotPrompt("app removal confirmation", "pass --yes")
				}
				ok, err := confirm(fmt.Sprintf("Uninstall %s from %q? Its DocTypes and data are deleted (a snapshot is taken first).", args[0], name))
				if err != nil || !ok {
					return err
				}
			}
			rm.Name, rm.App = name, args[0]
			snap, err := manager.New(verbose).AppRemove(rm, manager.CLIProgress{})
			if err != nil {
				return err
			}
			fmt.Printf("Removed %s from %q.", rm.App, name)
			if snap != "" {
				fmt.Printf(" Undo the data side with: ffm snapshot restore %s --name %s", name, snap)
			}
			fmt.Println()
			return nil
		},
	}
	removeCmd.Flags().BoolVar(&rm.KeepCode, "keep-code", false, "Uninstall from the site but keep the code in apps/")
	removeCmd.Flags().BoolVar(&rm.NoSnapshot, "no-snapshot", false, "Do not take a snapshot first")
	removeCmd.Flags().BoolVar(&yes, "yes", false, "Do not ask first (the bench name is then required)")

	var up manager.UpdateInput
	var upYes bool
	updateCmd := &cobra.Command{
		Use:   "update [bench]",
		Short: "Update the bench's apps (or move it to another Frappe branch), rolling back on failure",
		Long: `Update a running bench's apps in one locked pipeline:

  1. refuse uncommitted changes in any app (ffm's own frappe patches excepted);
  2. back the site up (database) and take a snapshot;
  3. maintenance mode on, scheduler paused;
  4. pull the apps (bench update --pull), install requirements, migrate,
     build, re-apply ffm's realtime patches;
  5. restart, check /api/method/ping, maintenance off, scheduler resumed.

If a step after 3 fails, the update is rolled back: every app back on its
previous commit and branch, the previous toolchain, the requirements, the
snapshot restored and the assets rebuilt. --no-rollback leaves it in
maintenance mode for inspection instead.

--to-branch version-16 is a major upgrade: frappe and every app on the bench's
Frappe branch switch to the new one, the image is rebuilt for its Node and the
virtualenv for its Python (version-16: Python 3.14, Node 24), then migrate.
Custom apps on other branches are left on theirs.

This is not 'ffm update', which updates ffm itself.`,
		Example: `  ffm app update mybench --dry-run
  ffm app update mybench --apps erpnext
  ffm app update mybench --to-branch version-16`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if upYes && len(args) == 0 {
				return usageError{fmt.Errorf("--yes requires the bench name")}
			}
			name, err := resolveBenchName(args, "Select a bench to update")
			if err != nil {
				return err
			}
			up.Name, up.Out = name, os.Stdout
			if !up.DryRun && !upYes {
				if !isInteractive() {
					return mustNotPrompt("update confirmation", "pass --yes (or --dry-run to only see the plan)")
				}
				ok, err := confirm(fmt.Sprintf("Update %q? Its site is in maintenance mode while the update runs.", name))
				if err != nil || !ok {
					return err
				}
			}
			svc := manager.New(verbose)
			if err := svc.Update(up, manager.CLIProgress{}); err != nil || up.DryRun {
				return err
			}
			return runPostUpdateHooks(svc, name)
		},
	}
	updateCmd.Flags().StringSliceVar(&up.Apps, "apps", nil, "Only pull these apps (default: all)")
	updateCmd.Flags().StringVar(&up.ToBranch, "to-branch", "", "Major upgrade: move frappe and the apps on its branch to this branch")
	updateCmd.Flags().BoolVar(&up.DryRun, "dry-run", false, "Show the apps and the plan, change nothing")
	updateCmd.Flags().BoolVar(&up.NoRollback, "no-rollback", false, "On failure, leave the bench as it failed (in maintenance mode)")
	updateCmd.Flags().BoolVar(&upYes, "yes", false, "Do not ask first (the bench name is then required)")

	cmd.AddCommand(list, addCmd, removeCmd, updateCmd)
	return cmd
}

func onOffYes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
