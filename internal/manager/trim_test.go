package manager

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	trimTablesJSON = `{"tr.localhost": {"Item": ["old_b", "old_a"], "ToDo": ["legacy"]}}`
	trimGhostText  = "tr.localhost: Following tables will be dropped:\n* tabOld Thing\n* tabGhost\n"
)

func trimRules(extra ...fakeexec.Rule) []fakeexec.Rule {
	return append(extra,
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
		fakeexec.Rule{Match: "trim-tables --dry-run --format json", Stdout: trimTablesJSON},
		fakeexec.Rule{Match: "trim-database --dry-run", Stdout: trimGhostText},
	)
}

func trimBench(t *testing.T, mod func(*state.Bench)) *Service {
	t.Helper()
	b := state.Bench{Name: "tr", Mode: "dev", SiteName: "tr.localhost", WebPort: 8000, SocketIOPort: 9000}
	if mod != nil {
		mod(&b)
	}
	s, _ := newTestBench(t, b)
	s.now = func() time.Time { return time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC) }
	return s
}

func callIndex(calls []string, parts ...string) int {
	for i, c := range calls {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(c, p)
		}
		if ok {
			return i
		}
	}
	return -1
}

func TestTrimDefaultsToADryRun(t *testing.T) {
	fake := fakeexec.Install(t, trimRules()...)
	s := trimBench(t, nil)

	res, err := s.Trim(TrimInput{Name: "tr"}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.Snapshot != "" {
		t.Errorf("applied without being asked: %+v", res)
	}
	if want := []string{"old_a", "old_b"}; !reflect.DeepEqual(res.Columns["Item"], want) {
		t.Errorf("Item columns = %v, want %v", res.Columns["Item"], want)
	}
	if want := []string{"tabOld Thing", "tabGhost"}; !reflect.DeepEqual(res.GhostTables, want) {
		t.Errorf("ghost tables = %v, want %v", res.GhostTables, want)
	}
	cols, tabs, ghosts := res.Counts()
	if cols != 3 || tabs != 2 || ghosts != 2 {
		t.Errorf("counts = %d/%d/%d", cols, tabs, ghosts)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "trim-") && !strings.Contains(c, "--dry-run") {
			t.Errorf("a trim command ran for real: %s", c)
		}
		if strings.Contains(c, " backup ") {
			t.Errorf("a snapshot was taken by a dry run: %s", c)
		}
	}
}

func TestTrimApplyTakesASnapshotFirst(t *testing.T) {
	fake := fakeexec.Install(t, trimRules()...)
	s := trimBench(t, nil)

	res, err := s.Trim(TrimInput{Name: "tr", Apply: true}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.Snapshot != "pre-trim-20261013-093000" {
		t.Errorf("result = %+v", res)
	}
	calls := fake.Calls()
	snap := callIndex(calls, "backup", "pre-trim-20261013-093000")
	tables := callIndex(calls, "trim-tables --no-backup")
	database := callIndex(calls, "trim-database --yes --no-backup")
	if snap < 0 || tables < 0 || database < 0 {
		t.Fatalf("missing step (snapshot %d, tables %d, database %d):\n%s", snap, tables, database, strings.Join(calls, "\n"))
	}
	if !(snap < tables && tables < database) {
		t.Errorf("order wrong: snapshot %d, tables %d, database %d", snap, tables, database)
	}
	if snaps, err := s.ListSnapshots("tr"); err != nil || len(snaps) != 1 {
		t.Errorf("snapshots = %+v, %v", snaps, err)
	}
}

func TestTrimNoSnapshotLeavesFrappesBackupOn(t *testing.T) {
	fake := fakeexec.Install(t, trimRules()...)
	s := trimBench(t, nil)

	if _, err := s.Trim(TrimInput{Name: "tr", Apply: true, NoSnapshot: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "--no-backup") {
			t.Errorf("Frappe's backup was switched off without a snapshot: %s", c)
		}
		if strings.Contains(c, " backup ") {
			t.Errorf("a snapshot was taken despite --no-snapshot: %s", c)
		}
	}
	if !fake.Called("trim-tables ") || !fake.Called("trim-database --yes") {
		t.Errorf("trim did not run:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestTrimOnlyWhatWasAsked(t *testing.T) {
	fake := fakeexec.Install(t, trimRules()...)
	s := trimBench(t, nil)
	if _, err := s.Trim(TrimInput{Name: "tr", Tables: true, Apply: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if fake.Called("trim-database") {
		t.Error("--tables ran trim-database")
	}
}

func TestTrimNothingToDoTakesNoSnapshot(t *testing.T) {
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
		fakeexec.Rule{Match: "trim-tables --dry-run", Stdout: `{}`},
		fakeexec.Rule{Match: "trim-database --dry-run", Stdout: "tr.localhost: No ghost tables\n"},
	)
	s := trimBench(t, nil)
	res, err := s.Trim(TrimInput{Name: "tr", Apply: true}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Empty() || res.Applied || res.Snapshot != "" {
		t.Errorf("result = %+v", res)
	}
	if fake.Called(" backup ") || fake.Called("--no-backup") {
		t.Errorf("work was done with nothing to trim:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestTrimNeedsARunningBench(t *testing.T) {
	fake := fakeexec.Install(t) // ps prints nothing
	s := trimBench(t, nil)
	if _, err := s.Trim(TrimInput{Name: "tr", Apply: true}, DiscardProgress{}); !errors.Is(err, ErrBenchStopped) {
		t.Errorf("err = %v, want ErrBenchStopped", err)
	}
	if fake.Called("trim-") {
		t.Error("trim ran on a stopped bench")
	}
}

func TestTrimPostgres(t *testing.T) {
	fake := fakeexec.Install(t, trimRules()...)
	s := trimBench(t, func(b *state.Bench) { b.DBType = "postgres" })

	var pre *PreflightError
	if _, err := s.Trim(TrimInput{Name: "tr", Database: true, Apply: true}, DiscardProgress{}); !errors.As(err, &pre) {
		t.Fatalf("--database on postgres: err = %v, want a PreflightError", err)
	}
	if fake.Called("trim-") {
		t.Error("trim ran before the refusal")
	}

	// Without a selection only the part postgres supports runs.
	res, err := s.Trim(TrimInput{Name: "tr", Apply: true}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || len(res.GhostTables) != 0 || len(res.Columns) == 0 {
		t.Errorf("result = %+v", res)
	}
	if fake.Called("trim-database") {
		t.Error("trim-database ran on postgres")
	}
}

func TestTrimFailureNamesTheSnapshot(t *testing.T) {
	fakeexec.Install(t, trimRules(fakeexec.Rule{Match: "trim-tables --no-backup", Exit: 1})...)
	s := trimBench(t, nil)
	_, err := s.Trim(TrimInput{Name: "tr", Apply: true}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "ffm snapshot restore tr --name pre-trim-20261013-093000") {
		t.Errorf("err = %v, want the snapshot restore command", err)
	}
}

func TestParseTrimOutput(t *testing.T) {
	cols, err := parseTrimTables("Warning: something\n" + trimTablesJSON + "\n")
	if err != nil || len(cols) != 2 {
		t.Errorf("cols = %v, err = %v", cols, err)
	}
	if _, err := parseTrimTables("no json here"); err == nil {
		t.Error("output without JSON accepted")
	}
	if got := parseGhostTables("tr.localhost: No ghost tables\n"); len(got) != 0 {
		t.Errorf("ghost tables = %v", got)
	}
}
