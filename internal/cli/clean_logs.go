package cli

import (
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

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
	if !dryRun && !yes {
		confirmed := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Delete log rows older than %d days from %q?", days, name)).
					Description("Tables: tabError Log, tabVersion, tabAccess Log, …\nThis cannot be undone.").
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
	return manager.New(verbose).CleanLogs(manager.CleanLogsInput{
		BenchName: name, Days: days, DryRun: dryRun,
	}, manager.CLIProgress{})
}
