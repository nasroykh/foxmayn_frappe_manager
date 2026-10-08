package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestCreateSnapshot(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "rev-parse HEAD", Stdout: "frappe abc123\nerpnext def456\n"})
	s, b := newTestBench(t, state.Bench{Name: "sn", Mode: "dev", SiteName: "sn.localhost", WebPort: 8000, SocketIOPort: 9000})
	s.now = func() time.Time { return time.Date(2026, 10, 8, 10, 45, 0, 0, time.UTC) }

	name, err := s.CreateSnapshot(SnapshotInput{Bench: "sn", Files: true}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "20261008-104500" {
		t.Errorf("default name = %q", name)
	}
	if !fake.Called("umask 077", "bench --site 'sn.localhost' backup", "--backup-path-db /workspace/.ffm-snapshots/20261008-104500/database.sql.gz", "--with-files") {
		t.Errorf("calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	root := filepath.Join(b.Dir, "workspace", snapshotsDir)
	if st, _ := os.Stat(root); st.Mode().Perm() != 0o700 {
		t.Errorf("snapshot root mode %v, want 0700", st.Mode().Perm())
	}
	snaps, err := s.ListSnapshots("sn")
	if err != nil || len(snaps) != 1 || !snaps[0].Files || snaps[0].Commits["erpnext"] != "def456" {
		t.Errorf("list = %+v, %v", snaps, err)
	}
	if _, err := s.CreateSnapshot(SnapshotInput{Bench: "sn", Name: "20261008-104500"}, DiscardProgress{}); err == nil {
		t.Error("a second snapshot with the same name was taken")
	}
	for _, bad := range []string{"../x", "a b", ".hidden", strings.Repeat("a", 65)} {
		if _, err := s.CreateSnapshot(SnapshotInput{Bench: "sn", Name: bad}, DiscardProgress{}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func writeSnapshot(t *testing.T, b state.Bench, name string, at time.Time, files bool) {
	t.Helper()
	dir := filepath.Join(b.Dir, "workspace", snapshotsDir, name)
	os.MkdirAll(dir, 0o700)
	meta := `{"name":"` + name + `","created_at":"` + at.Format(time.RFC3339) + `","files":` + map[bool]string{true: "true", false: "false"}[files] +
		`,"commits":{"frappe":"old"}}`
	os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(meta), 0o600)
	os.WriteFile(filepath.Join(dir, "database.sql.gz"), []byte("x"), 0o600)
}

func TestRestoreSnapshotNewestAndPasswordOffArgv(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "rev-parse HEAD", Stdout: "frappe new\n"})
	s, b := newTestBench(t, state.Bench{Name: "rs", Mode: "dev", SiteName: "rs.localhost", WebPort: 8000, SocketIOPort: 9000, DBPassword: "s3cret-db1"})
	t0 := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	writeSnapshot(t, b, "old", t0, false)
	writeSnapshot(t, b, "new", t0.Add(time.Hour), true)
	// No snapshot.json: an interrupted snapshot, never listed or restored.
	os.MkdirAll(filepath.Join(b.Dir, "workspace", snapshotsDir, "partial"), 0o700)

	var out BufferProgress
	got, err := s.RestoreSnapshot(RestoreSnapshotInput{Bench: "rs"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Errorf("restored %q, want the newest", got)
	}
	if !fake.Called("restore /workspace/.ffm-snapshots/new/database.sql.gz", `--db-root-password "$p"`, "--with-public-files /workspace/.ffm-snapshots/new/public-files.tgz") {
		t.Errorf("calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if !fake.Called("bench --site 'rs.localhost' clear-cache") {
		t.Error("cache not cleared after the swap")
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "s3cret") {
			t.Errorf("password on a command line: %s", c)
		}
	}
	if !strings.Contains(strings.Join(out.Lines, "\n"), "The code of frappe changed") {
		t.Errorf("no warning about moved code: %q", out.Lines)
	}

	if _, err := s.RestoreSnapshot(RestoreSnapshotInput{Bench: "rs", Name: "partial"}, DiscardProgress{}); err == nil {
		t.Error("an incomplete snapshot was restored")
	}
	if err := s.DeleteSnapshot("rs", "old"); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := s.ListSnapshots("rs"); len(snaps) != 1 || snaps[0].Name != "new" {
		t.Errorf("after delete: %+v", snaps)
	}
	if err := s.DeleteSnapshot("rs", "../../x"); err == nil {
		t.Error("path traversal accepted")
	}
}

func TestCloneRefusesBeforeWork(t *testing.T) {
	fake := fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "src", Mode: "prod", SiteName: "src.example.com", Domain: "src.example.com", WebPort: 8000, SocketIOPort: 9000})
	if err := s.AddBench(state.Bench{Name: "taken", Mode: "dev", WebPort: 8010, SocketIOPort: 9010}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []CloneInput{
		{Source: "src", Target: "src"},
		{Source: "src", Target: "taken"},
		{Source: "src", Target: "copy"}, // prod without --domain
		{Source: "nope", Target: "copy"},
	} {
		if err := s.Clone(in, DiscardProgress{}); err == nil {
			t.Errorf("Clone(%+v) accepted", in)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("work started before the refusal: %v", fake.Calls())
	}
}
