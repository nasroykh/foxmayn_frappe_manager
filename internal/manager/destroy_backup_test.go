package manager

import (
	"os"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// A delete whose backup fails must change nothing: the bench record and its
// directory both survive. The fixture bench has a directory but no site, so
// the backup fails before touching Docker.
func TestDeleteRefusesWhenBackupFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", dir)
	t.Setenv("FFM_BENCHES_DIR", dir+"/benches")
	t.Setenv("FFM_BACKUPS_DIR", dir+"/backups")
	benchDir := dir + "/benches/alpha"
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(false)
	if err := s.AddBench(state.Bench{Name: "alpha", Dir: benchDir, SiteName: "alpha.localhost"}); err != nil {
		t.Fatal(err)
	}

	err := s.Delete(DeleteInput{Name: "alpha"}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "backup before delete failed") {
		t.Fatalf("Delete = %v, want a refused backup", err)
	}
	if _, err := s.GetBench("alpha"); err != nil {
		t.Fatalf("bench record removed although the backup failed: %v", err)
	}
	if _, err := os.Stat(benchDir); err != nil {
		t.Fatalf("bench directory removed although the backup failed: %v", err)
	}
}

func TestBackupBeforeDestroySkips(t *testing.T) {
	s := New(false)
	path, err := s.backupBeforeDestroy(state.Bench{Name: "x", Dir: t.TempDir()}, "delete", true, DiscardProgress{})
	if err != nil || path != "" {
		t.Fatalf("--no-backup: path %q, err %v", path, err)
	}
	// A bench whose directory is already gone has nothing to save.
	path, err = s.backupBeforeDestroy(state.Bench{Name: "x", Dir: t.TempDir() + "/missing"}, "delete", false, DiscardProgress{})
	if err != nil || path != "" {
		t.Fatalf("missing directory: path %q, err %v", path, err)
	}
}
