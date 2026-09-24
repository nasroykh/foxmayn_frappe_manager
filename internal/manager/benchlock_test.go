package manager

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestLockBenchIsReentrantWithinAService(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	s := New(false)

	outer, err := s.lockBench("alpha")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.lockBench("alpha")
	if err != nil {
		t.Fatalf("nested lock in the same Service: %v", err)
	}
	inner()

	// Still held after the inner release: another Service (another handle,
	// as another process would have) must be refused.
	other := New(false)
	if _, err := other.lockBench("alpha"); !errors.Is(err, ErrBenchBusy) {
		t.Fatalf("lock from another Service while held = %v, want ErrBenchBusy", err)
	}
	outer()

	release, err := other.lockBench("alpha")
	if err != nil {
		t.Fatalf("lock after full release: %v", err)
	}
	release()
}

func TestLockBenchIsPerBench(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	a, err := New(false).lockBench("alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := New(false).lockBench("beta")
	if err != nil {
		t.Fatalf("a different bench must not be blocked: %v", err)
	}
	b()
}

func TestBackupRefusesBusyBench(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", dir)
	t.Setenv("FFM_BENCHES_DIR", dir+"/benches")
	s := New(false)
	if err := s.AddBench(state.Bench{Name: "alpha", Dir: dir + "/benches/alpha"}); err != nil {
		t.Fatal(err)
	}
	held, err := New(false).lockBench("alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer held()

	err = s.Backup(BackupInput{BenchName: "alpha"}, DiscardProgress{})
	if !errors.Is(err, ErrBenchBusy) {
		t.Fatalf("Backup of a locked bench = %v, want ErrBenchBusy", err)
	}
	err = s.Delete("alpha", DiscardProgress{})
	if !errors.Is(err, ErrBenchBusy) {
		t.Fatalf("Delete of a locked bench = %v, want ErrBenchBusy", err)
	}
}

func TestBackupRejectsUnknownTrigger(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", dir)
	s := New(false)
	if err := s.AddBench(state.Bench{Name: "alpha", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(BackupInput{BenchName: "alpha", Trigger: "cron"}, DiscardProgress{}); err == nil {
		t.Fatal("unknown trigger accepted")
	}
}

func TestArchiveFileName(t *testing.T) {
	now := time.Date(2026, 9, 24, 13, 5, 9, 0, time.FixedZone("CET", 3600))
	if got, want := archiveFileName("mybench", TriggerManual, now), "mybench_20260924T120509Z.ffm.tar"; got != want {
		t.Errorf("manual = %q, want %q", got, want)
	}
	if got, want := archiveFileName("mybench", TriggerScheduled, now), "mybench_20260924T120509Z.auto.ffm.tar"; got != want {
		t.Errorf("scheduled = %q, want %q", got, want)
	}
}

func TestScheduledBackupIgnoresOut(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FFM_BACKUPS_DIR", dir)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	got, err := backupDestination(BackupInput{Out: t.TempDir()}, "mybench", TriggerScheduled, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := dir + "/mybench/mybench_20260924T120000Z.auto.ffm.tar"; got != want {
		t.Fatalf("scheduled destination = %q, want %q", got, want)
	}
}

func TestHeaderTriggerRoundTrip(t *testing.T) {
	h := NewHeader(fullBench(), "erp.example.com", "16.0.0", "", []string{TierCore}, time.Now().UTC())
	raw, _ := json.Marshal(h)
	parsed, err := ParseHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IsScheduled() {
		t.Fatal("a header without trigger (older ffm) must read as not scheduled")
	}
	h.Trigger = TriggerScheduled
	raw, _ = json.Marshal(h)
	if parsed, err = ParseHeader(raw); err != nil || !parsed.IsScheduled() {
		t.Fatalf("scheduled header round-trip = %+v, %v", parsed, err)
	}
}
