package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// logTableColumns maps Frappe log table names to their timestamp column.
// tabSessions uses lastupdate instead of the standard creation column.
var logTableColumns = [][2]string{
	{"tabError Log", "creation"},
	{"tabVersion", "creation"},
	{"tabAccess Log", "creation"},
	{"tabRoute History", "creation"},
	{"tabScheduled Job Log", "creation"},
	{"tabActivity Log", "creation"},
	{"tabSessions", "lastupdate"},
}

func newCleanLogsCmd() *cobra.Command {
	var (
		days   int
		dryRun bool
		yes    bool
	)

	cmd := &cobra.Command{
		Use:   "clean-logs [name]",
		Short: "Delete old log table rows to reclaim database space",
		Long: `Removes rows older than --days from Frappe log tables:
  tabError Log, tabVersion, tabAccess Log, tabRoute History,
  tabScheduled Job Log, tabActivity Log, tabSessions.

Use --dry-run to preview row counts without deleting.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to clean logs")
			if err != nil {
				return err
			}
			return runCleanLogs(name, days, dryRun, yes)
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Delete rows older than this many days")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print row counts without deleting anything")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

func runCleanLogs(name string, days int, dryRun, yes bool) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	if b.IsPostgres() {
		return fmt.Errorf("clean-logs does not yet support PostgreSQL benches")
	}

	runner := bench.NewRunner(b.Name, b.Dir, verbose)

	// Read the actual DB name from site_config.json inside the frappe container.
	// Frappe stores db_name there; it may differ from the site name.
	dbNameScript := fmt.Sprintf(
		`import json,sys; print(json.load(open('/workspace/frappe-bench/sites/%s/site_config.json'))['db_name'])`,
		b.SiteName,
	)
	dbName, err := runner.ExecSilent("frappe", "python3", "-c", dbNameScript)
	if err != nil {
		return fmt.Errorf("read site_config.json: %w\n%s", err, dbName)
	}
	dbName = strings.TrimSpace(dbName)
	if dbName == "" {
		return fmt.Errorf("could not determine db_name from site_config.json")
	}

	if !dryRun && !yes {
		tableNames := make([]string, len(logTableColumns))
		for i, pair := range logTableColumns {
			tableNames[i] = pair[0]
		}
		confirmed := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Delete log rows older than %d days from %q?", days, name)).
					Description("Tables: "+strings.Join(tableNames, ", ")+"\nThis cannot be undone.").
					Affirmative("Yes, delete").
					Negative("Cancel").
					Value(&confirmed),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	total := 0
	for _, pair := range logTableColumns {
		table, col := pair[0], pair[1]

		countSQL := fmt.Sprintf(
			"SELECT COUNT(*) FROM `%s`.`%s` WHERE `%s` < NOW() - INTERVAL %d DAY;",
			dbName, table, col, days,
		)
		countOut, err := runner.ExecSilent("mariadb",
			"mariadb", "-u", "root", "-p"+b.DBPassword, "-N", "-e", countSQL)
		if err != nil {
			fmt.Printf("  %s: skipped (%s)\n", table, strings.TrimSpace(countOut))
			continue
		}

		count := 0
		fmt.Sscanf(strings.TrimSpace(countOut), "%d", &count)

		if dryRun {
			fmt.Printf("  %-30s %d rows would be deleted\n", table+":", count)
			total += count
			continue
		}

		deleteSQL := fmt.Sprintf(
			"DELETE FROM `%s`.`%s` WHERE `%s` < NOW() - INTERVAL %d DAY;",
			dbName, table, col, days,
		)
		if out, err := runner.ExecSilent("mariadb",
			"mariadb", "-u", "root", "-p"+b.DBPassword, "-e", deleteSQL); err != nil {
			fmt.Printf("  %s: delete failed (%s)\n", table, strings.TrimSpace(out))
			continue
		}
		fmt.Printf("  %-30s deleted %d rows\n", table+":", count)
		total += count
	}

	action := "would be deleted"
	if !dryRun {
		action = "deleted"
	}
	fmt.Printf("\nTotal: %d rows %s  (older than %d days)\n", total, action, days)
	return nil
}
