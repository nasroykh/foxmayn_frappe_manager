package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newTrimCmd() *cobra.Command {
	var (
		tables, database, dryRun, yes, noSnapshot bool
	)
	cmd := &cobra.Command{
		Use:   "trim [bench]",
		Short: "Drop database columns and tables that Frappe no longer uses (dry run unless --yes)",
		Long: `Run Frappe's own cleanup of a running bench's database:

  --tables    bench trim-tables: drops columns that no field of the DocType owns
              any more (a field removed from a DocType leaves its column behind)
  --database  bench trim-database: drops whole tables that no DocType owns
              (left by uninstalled apps or deleted DocTypes), with their data

With neither flag both run. Without --yes ffm only lists what would go (Frappe's
--dry-run) and, at a terminal, asks whether to go on. --yes applies it without
asking and takes the same plan. Before anything is dropped ffm takes a snapshot
named pre-trim-<time> ('ffm snapshot restore <bench> --name <it>' undoes the
trim); --no-snapshot skips it, and Frappe's own pre-trim backup runs instead.

The bench has to be running. trim-database supports MariaDB only: on a PostgreSQL
bench, --tables works and --database is refused.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && yes {
				return usageError{errors.New("--dry-run and --yes contradict each other")}
			}
			name, err := resolveBenchName(args, "Select a bench to trim")
			if err != nil {
				return err
			}
			svc := manager.New(verbose)
			in := manager.TrimInput{Name: name, Tables: tables, Database: database, Apply: yes, NoSnapshot: noSnapshot}
			res, err := svc.Trim(in, manager.CLIProgress{})
			if err != nil || res.Empty() || res.Applied {
				return err
			}
			if dryRun || !isInteractive() {
				fmt.Printf("\nNothing was changed. Run 'ffm trim %s --yes' to apply this.\n", name)
				return nil
			}
			cols, tabs, ghosts := res.Counts()
			safety := " (a snapshot is taken first)"
			if noSnapshot {
				safety = ""
			}
			ok, err := confirm(fmt.Sprintf("Drop %d columns in %d tables and %d tables from %q? This deletes their data%s.",
				cols, tabs, ghosts, name, safety))
			if err != nil || !ok {
				return err
			}
			in.Apply = true
			_, err = svc.Trim(in, manager.CLIProgress{})
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&tables, "tables", false, "Only drop columns no field owns any more (bench trim-tables)")
	f.BoolVar(&database, "database", false, "Only drop tables no DocType owns (bench trim-database; MariaDB only)")
	f.BoolVar(&dryRun, "dry-run", false, "Only list what would be dropped (the default without --yes)")
	f.BoolVar(&yes, "yes", false, "Apply the trim without asking")
	f.BoolVar(&noSnapshot, "no-snapshot", false, "Do not take an ffm snapshot first (Frappe's own backup runs instead)")
	return cmd
}
