package manager

import (
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

var logTableColumns = [][2]string{
	{"tabError Log", "creation"},
	{"tabVersion", "creation"},
	{"tabAccess Log", "creation"},
	{"tabRoute History", "creation"},
	{"tabScheduled Job Log", "creation"},
	{"tabActivity Log", "creation"},
	{"tabSessions", "lastupdate"},
}

// CleanLogs deletes old rows from Frappe log tables.
func (s *Service) CleanLogs(in CleanLogsInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.BenchName)
	if err != nil {
		return err
	}
	if b.IsPostgres() {
		return fmt.Errorf("clean-logs does not yet support PostgreSQL benches")
	}

	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
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

	total := 0
	for _, pair := range logTableColumns {
		table, col := pair[0], pair[1]
		countSQL := fmt.Sprintf(
			"SELECT COUNT(*) FROM `%s`.`%s` WHERE `%s` < NOW() - INTERVAL %d DAY;",
			dbName, table, col, in.Days,
		)
		countOut, err := runner.ExecSilent("mariadb",
			"mariadb", "-u", "root", "-p"+b.DBPassword, "-N", "-e", countSQL)
		if err != nil {
			pw.Printf("  %s: skipped (%s)\n", table, strings.TrimSpace(countOut))
			continue
		}
		count := 0
		fmt.Sscanf(strings.TrimSpace(countOut), "%d", &count)

		if in.DryRun {
			pw.Printf("  %-30s %d rows would be deleted\n", table+":", count)
			total += count
			continue
		}

		deleteSQL := fmt.Sprintf(
			"DELETE FROM `%s`.`%s` WHERE `%s` < NOW() - INTERVAL %d DAY;",
			dbName, table, col, in.Days,
		)
		if out, err := runner.ExecSilent("mariadb",
			"mariadb", "-u", "root", "-p"+b.DBPassword, "-e", deleteSQL); err != nil {
			pw.Printf("  %s: delete failed (%s)\n", table, strings.TrimSpace(out))
			continue
		}
		pw.Printf("  %-30s deleted %d rows\n", table+":", count)
		total += count
	}

	action := "would be deleted"
	if !in.DryRun {
		action = "deleted"
	}
	pw.Printf("\nTotal: %d rows %s  (older than %d days)\n", total, action, in.Days)
	return nil
}
