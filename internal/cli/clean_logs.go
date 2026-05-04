package cli

import (
	"encoding/base64"
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

	runner := bench.NewRunner(b.Name, b.Dir, verbose)

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

	pyScript := buildCleanLogsScript(b.SiteName, days, dryRun)
	encoded := base64.StdEncoding.EncodeToString([]byte(pyScript))
	shellCmd := fmt.Sprintf("cd /workspace/frappe-bench && echo '%s' | base64 -d | python3", encoded)

	return runner.ExecOutputInDir("frappe", "/workspace/frappe-bench", "bash", "-c", shellCmd)
}

// buildCleanLogsScript returns a Python script that runs inside the frappe
// container to count (dry-run) or delete log rows older than the given days.
// Table names are backtick-quoted via chr(96) to avoid terminating Go raw strings.
func buildCleanLogsScript(siteName string, days int, dryRun bool) string {
	pairs := make([]string, len(logTableColumns))
	for i, pair := range logTableColumns {
		pairs[i] = fmt.Sprintf("(%q, %q)", pair[0], pair[1])
	}
	tableList := strings.Join(pairs, ", ")

	header := fmt.Sprintf(
		"import frappe\nfrom frappe.utils import add_days, today\n\n"+
			"frappe.init(site=%q)\nfrappe.connect()\n\n"+
			"bt = chr(96)\ntables = [%s]\ncutoff = add_days(today(), -%d)\ntotal = 0\n",
		siteName, tableList, days,
	)

	var body string
	if dryRun {
		body = "for table, col in tables:\n" +
			"    try:\n" +
			"        count = frappe.db.sql(\n" +
			"            \"SELECT COUNT(*) FROM \" + bt + table + bt + \" WHERE \" + bt + col + bt + \" < %s\",\n" +
			"            [cutoff]\n" +
			"        )[0][0]\n" +
			"        print(f\"  {table}: {count} rows would be deleted\")\n" +
			"        total += count\n" +
			"    except Exception as e:\n" +
			"        print(f\"  {table}: skipped ({e})\")\n" +
			"\nprint(f\"\\nTotal: {total} rows  (cutoff date: {cutoff})\")\n"
	} else {
		body = "for table, col in tables:\n" +
			"    try:\n" +
			"        count = frappe.db.sql(\n" +
			"            \"SELECT COUNT(*) FROM \" + bt + table + bt + \" WHERE \" + bt + col + bt + \" < %s\",\n" +
			"            [cutoff]\n" +
			"        )[0][0]\n" +
			"        frappe.db.sql(\n" +
			"            \"DELETE FROM \" + bt + table + bt + \" WHERE \" + bt + col + bt + \" < %s\",\n" +
			"            [cutoff]\n" +
			"        )\n" +
			"        frappe.db.commit()\n" +
			"        print(f\"  {table}: deleted {count} rows\")\n" +
			"        total += count\n" +
			"    except Exception as e:\n" +
			"        print(f\"  {table}: skipped ({e})\")\n" +
			"\nprint(f\"\\nTotal: {total} rows deleted  (cutoff date: {cutoff})\")\n"
	}

	return header + body + "frappe.destroy()\n"
}
