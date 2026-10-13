package manager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// TrimInput selects what `ffm trim` looks at and whether it changes anything.
type TrimInput struct {
	Name string
	// Tables looks for columns that no field of the DocType owns any more
	// (bench trim-tables); Database for tables that no DocType owns
	// (bench trim-database). Neither set means both.
	Tables   bool
	Database bool
	// Apply drops what the plan lists. Without it Trim only reports.
	Apply bool
	// NoSnapshot skips the ffm snapshot taken before a change. Frappe's own
	// pre-trim backup then still runs.
	NoSnapshot bool
}

// TrimResult is what Trim found, and did when Apply was set.
type TrimResult struct {
	// Columns maps a DocType to the columns trim-tables lists for removal.
	Columns map[string][]string
	// GhostTables are the tables trim-database lists for dropping.
	GhostTables []string
	// Applied is true when the changes were made.
	Applied bool
	// Snapshot names the snapshot taken before them, "" when none was.
	Snapshot string
}

// Empty reports whether there is nothing to trim.
func (r TrimResult) Empty() bool { return len(r.Columns) == 0 && len(r.GhostTables) == 0 }

// Counts returns the number of columns, the tables they are in, and the
// ghost tables.
func (r TrimResult) Counts() (columns, tables, ghosts int) {
	for _, c := range r.Columns {
		columns += len(c)
	}
	return columns, len(r.Columns), len(r.GhostTables)
}

// Trim runs Frappe's trim-tables and trim-database on a running bench's site.
//
// Both commands are destructive: trim-tables DROPs columns, trim-database
// DROPs whole tables that no DocType owns. So Trim always starts with their
// own --dry-run, and changes something only when Apply is set; it then takes
// an ffm snapshot first (unless NoSnapshot), so that `ffm snapshot restore`
// undoes it, and runs the commands with their own backup off, which the
// snapshot replaces. The output of each command goes through pw.
//
// trim-database is MariaDB only: it asks information_schema for the tables of
// the schema named like the database, but a PostgreSQL site keeps its tables in
// the "public" schema, so the command would report "No ghost tables" for any
// site. Trim refuses it there rather than print that. trim-tables is portable
// SQL and runs on both.
func (s *Service) Trim(in TrimInput, pw ProgressWriter) (TrimResult, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return TrimResult{}, err
	}
	defer release()

	b, err := s.GetBench(in.Name)
	if err != nil {
		return TrimResult{}, err
	}
	explicit := in.Tables || in.Database
	doTables, doDatabase := in.Tables || !explicit, in.Database || !explicit
	if doDatabase && b.IsPostgres() {
		if explicit {
			return TrimResult{}, &PreflightError{msg: fmt.Sprintf("bench %q uses PostgreSQL, and Frappe's trim-database only works on MariaDB "+
				"(it looks for the tables in a schema named like the database, which PostgreSQL does not have), so it would report nothing to drop "+
				"on a site that has ghost tables. Use --tables, which works on both.", b.Name)}
		}
		pw.Println("Skipping trim-database: it supports MariaDB only (bench is PostgreSQL).")
		doDatabase = false
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return TrimResult{}, fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}

	runner := s.runnerFor(b)
	res := TrimResult{}
	pw.Step(fmt.Sprintf("Looking for what can be trimmed on %q (dry run)", b.Name))
	if doTables {
		out, err := s.runTrimCapture(runner, b, "trim-tables --dry-run --format json", pw)
		if err != nil {
			return res, fmt.Errorf("bench trim-tables --dry-run: %w", err)
		}
		if res.Columns, err = parseTrimTables(out); err != nil {
			return res, fmt.Errorf("read trim-tables output: %w", err)
		}
	}
	if doDatabase {
		out, err := s.runTrimCapture(runner, b, "trim-database --dry-run", pw)
		if err != nil {
			return res, fmt.Errorf("bench trim-database --dry-run: %w", err)
		}
		res.GhostTables = parseGhostTables(out)
	}
	printTrimPlan(pw, res, doTables, doDatabase)
	if res.Empty() || !in.Apply {
		return res, nil
	}

	flags := ""
	if in.NoSnapshot {
		pw.Println("No ffm snapshot (--no-snapshot): Frappe's own backup runs instead.")
	} else {
		name := "pre-trim-" + s.clock().UTC().Format("20060102-150405")
		if _, err := s.createSnapshotLocked(SnapshotInput{Bench: b.Name}, name, pw); err != nil {
			return res, fmt.Errorf("snapshot before trimming: %w (nothing was changed)", err)
		}
		res.Snapshot = name
		flags = " --no-backup"
	}
	undo := ""
	if res.Snapshot != "" {
		undo = fmt.Sprintf(" (undo with: ffm snapshot restore %s --name %s)", b.Name, res.Snapshot)
	}
	if len(res.Columns) > 0 {
		pw.Step("Dropping unused columns (bench trim-tables)")
		if err := s.runTrim(runner, b, "trim-tables"+flags, pw); err != nil {
			return res, fmt.Errorf("bench trim-tables: %w%s", err, undo)
		}
	}
	if len(res.GhostTables) > 0 {
		pw.Step("Dropping tables no DocType owns (bench trim-database)")
		if err := s.runTrim(runner, b, "trim-database --yes"+flags, pw); err != nil {
			return res, fmt.Errorf("bench trim-database: %w%s", err, undo)
		}
	}
	res.Applied = true
	pw.Printf("Trimmed bench %q.%s\n", b.Name, undo)
	return res, nil
}

// runTrimCapture runs one trim command and returns its stdout; its stderr goes
// to pw.
func (s *Service) runTrimCapture(r *bench.Runner, b state.Bench, args string, pw ProgressWriter) (string, error) {
	var out bytes.Buffer
	errw := &lineWriter{emit: func(l string) { fmt.Fprintln(pw.Stderr(), l) }}
	err := r.ExecTo("frappe", "/workspace/frappe-bench", &out, errw, "bash", "-c", siteCmd(b, args))
	errw.Flush()
	return out.String(), err
}

// runTrim runs one trim command, streaming its output through pw.
func (s *Service) runTrim(r *bench.Runner, b state.Bench, args string, pw ProgressWriter) error {
	w := &lineWriter{emit: func(l string) { pw.Println("  " + l) }}
	err := r.ExecTo("frappe", "/workspace/frappe-bench", w, w, "bash", "-c", siteCmd(b, args))
	w.Flush()
	return err
}

// parseTrimTables reads `trim-tables --format json`: {"<site>": {"<DocType>": [columns]}}.
// Anything printed before the JSON object (a warning on stdout) is skipped.
func parseTrimTables(out string) (map[string][]string, error) {
	i := strings.Index(out, "{")
	if i < 0 {
		return nil, fmt.Errorf("no JSON in %q", strings.TrimSpace(out))
	}
	var doc map[string]map[string][]string
	if err := json.NewDecoder(strings.NewReader(out[i:])).Decode(&doc); err != nil {
		return nil, err
	}
	cols := map[string][]string{}
	for _, tables := range doc {
		for dt, c := range tables {
			if len(c) > 0 {
				cols[dt] = append(cols[dt], c...)
			}
		}
	}
	for _, c := range cols {
		sort.Strings(c)
	}
	return cols, nil
}

// parseGhostTables reads the bullet list of `trim-database --dry-run`.
func parseGhostTables(out string) []string {
	var tables []string
	for _, l := range strings.Split(out, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "* "); ok && t != "" {
			tables = append(tables, t)
		}
	}
	return tables
}

func printTrimPlan(pw ProgressWriter, r TrimResult, tables, database bool) {
	if r.Empty() {
		pw.Println("Nothing to trim.")
		return
	}
	if tables {
		if len(r.Columns) == 0 {
			pw.Println("Columns: none to drop.")
		} else {
			cols, tabs, _ := r.Counts()
			pw.Printf("Columns that no field owns any more (%d columns in %d tables):\n", cols, tabs)
			names := make([]string, 0, len(r.Columns))
			for dt := range r.Columns {
				names = append(names, dt)
			}
			sort.Strings(names)
			for _, dt := range names {
				pw.Printf("  tab%s: %s\n", dt, strings.Join(r.Columns[dt], ", "))
			}
		}
	}
	if database {
		if len(r.GhostTables) == 0 {
			pw.Println("Tables: none to drop.")
		} else {
			pw.Printf("Tables that no DocType owns (%d, dropped with their data):\n", len(r.GhostTables))
			for _, t := range r.GhostTables {
				pw.Println("  " + t)
			}
		}
	}
}
