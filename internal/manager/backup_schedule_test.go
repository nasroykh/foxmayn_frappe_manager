package manager

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/lock"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestDueHonoursIntervalWithSlack(t *testing.T) {
	last := time.Date(2026, 9, 24, 10, 17, 0, 0, time.UTC)
	st := ScheduleStatus{Policy: state.BackupPolicy{EveryHours: 1}, LastSuccess: last,
		NextDue: last.Add(time.Hour)}
	cases := []struct {
		now  time.Time
		want bool
	}{
		{last.Add(30 * time.Minute), false},
		// A backup that finished at :17 must still be due at the next :14 tick.
		{last.Add(57 * time.Minute), true},
		{last.Add(3 * time.Hour), true}, // missed ticks catch up
	}
	for _, c := range cases {
		if got := st.due(c.now); got != c.want {
			t.Errorf("due at +%v = %v, want %v", c.now.Sub(last), got, c.want)
		}
	}
	if !(ScheduleStatus{}).due(last) {
		t.Error("a bench with no scheduled archive must be due")
	}
}

func TestIncludeFilesCadence(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		files     string
		lastFiles time.Duration // ago; 0 = never
		want      bool
	}{
		{FilesEveryRun, time.Hour, true},
		{FilesNever, 0, false},
		{FilesDaily, 0, true},
		{FilesDaily, 3 * time.Hour, false},
		{FilesDaily, 24 * time.Hour, true},
		{FilesWeekly, 3 * 24 * time.Hour, false},
		{FilesWeekly, 7 * 24 * time.Hour, true},
	} {
		st := ScheduleStatus{Policy: state.BackupPolicy{Files: c.files}}
		if c.lastFiles > 0 {
			st.LastFiles = now.Add(-c.lastFiles)
		}
		if got := st.includeFiles(now); got != c.want {
			t.Errorf("files=%s last=%v ago: include = %v, want %v", c.files, c.lastFiles, got, c.want)
		}
	}
}

func setupScheduleEnv(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "cfg"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "bk"))
	// No docker: LiveStatus reports "unknown", exactly as under a cron PATH
	// that lacks it.
	t.Setenv("PATH", root)
	return New(false), root
}

func TestRunDueFailsLoudlyWhenDockerIsUnreachable(t *testing.T) {
	s, root := setupScheduleEnv(t)
	benchDir := filepath.Join(root, "benches", "alpha")
	siteDir := filepath.Join(benchDir, "workspace", "frappe-bench", "sites", "s")
	os.MkdirAll(siteDir, 0o755)
	os.WriteFile(filepath.Join(siteDir, "site_config.json"), []byte(`{}`), 0o644)
	p := PresetPolicy(1)
	s.AddBench(state.Bench{Name: "alpha", Dir: benchDir, SiteName: "s", BackupSchedule: &p})
	s.AddBench(state.Bench{Name: "unscheduled", Dir: benchDir, SiteName: "s"})

	// A scheduled archive that pruning would remove if it (wrongly) ran.
	dir := filepath.Join(root, "bk", "alpha")
	os.MkdirAll(dir, 0o700)
	var paths []string
	for i := 0; i < 6; i++ {
		h := NewHeader(fullBench(), "s", "16", "", []string{TierCore},
			time.Now().UTC().Add(-time.Duration(100+i)*24*time.Hour))
		h.BenchName, h.Trigger = "alpha", TriggerScheduled
		paths = append(paths, writeArchive(t, dir, archiveFileName("alpha", TriggerScheduled, h.CreatedAt), h))
	}

	var log bytes.Buffer
	results, err := s.RunDue(false, &log)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Bench != "alpha" {
		t.Fatalf("results = %+v, want one entry for alpha", results)
	}
	if results[0].Result != RunFailed || !strings.Contains(results[0].Err.Error(), "Docker") {
		t.Fatalf("result = %s %v, want a failure naming Docker", results[0].Result, results[0].Err)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a failed run pruned %s", filepath.Base(p))
		}
	}
	if rs := ReadRunState("alpha"); rs.Result != RunFailed || rs.Error == "" {
		t.Fatalf("run state = %+v, want a recorded failure", rs)
	}
}

func TestRunDueSkipsBenchesNotDue(t *testing.T) {
	s, root := setupScheduleEnv(t)
	p := PresetPolicy(24)
	s.AddBench(state.Bench{Name: "alpha", Dir: root, BackupSchedule: &p})
	dir := filepath.Join(root, "bk", "alpha")
	os.MkdirAll(dir, 0o700)
	h := NewHeader(fullBench(), "s", "16", "", []string{TierCore}, time.Now().UTC().Add(-2*time.Hour))
	h.BenchName, h.Trigger = "alpha", TriggerScheduled
	writeArchive(t, dir, "a.auto.ffm.tar", h)

	results, err := s.RunDue(false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Result != "not-due" {
		t.Fatalf("results = %+v, want not-due", results)
	}
}

func TestRunDueRefusesToOverlap(t *testing.T) {
	s, _ := setupScheduleEnv(t)
	held, err := lock.TryAcquire(config.BackupRunLockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := s.RunDue(false, &bytes.Buffer{}); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("RunDue while another holds the lock = %v, want ErrRunInProgress", err)
	}
}

func TestPolicyValidationAndShorthand(t *testing.T) {
	p := PresetPolicy(168)
	ApplyKeepShorthand(&p, 3)
	if p.KeepWeekly != 3 || p.KeepDaily != 0 || p.KeepHourly != 0 {
		t.Fatalf("weekly --keep 3 = %+v", p)
	}
	if err := ValidatePolicy(p); err != nil {
		t.Fatal(err)
	}
	bad := PresetPolicy(24)
	bad.Files = "sometimes"
	if ValidatePolicy(bad) == nil {
		t.Error("invalid --files accepted")
	}
	empty := state.BackupPolicy{EveryHours: 24, Files: FilesNever}
	if ValidatePolicy(empty) == nil {
		t.Error("a policy that keeps nothing was accepted")
	}
	for in, want := range map[string]int{"1h": 1, "24": 24, "daily": 24, "Weekly": 168} {
		if got, err := ParseEvery(in); err != nil || got != want {
			t.Errorf("ParseEvery(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"0h", "30m", "soon", "-1"} {
		if _, err := ParseEvery(in); err == nil {
			t.Errorf("ParseEvery(%q) accepted", in)
		}
	}
}
