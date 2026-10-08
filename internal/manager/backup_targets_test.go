package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/backuptarget"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestTargetsUploadPrunePull(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	t.Setenv("FFM_BENCHES_DIR", t.TempDir())
	root := t.TempDir()
	t.Setenv("FFM_BACKUPS_DIR", root)
	ctx := context.Background()
	idFile := filepath.Join(t.TempDir(), "id")
	if _, _, err := InitBackupKey(idFile, false); err != nil {
		t.Fatal(err)
	}

	remoteDir := t.TempDir()
	nas := backuptarget.Target{Name: "nas", Type: backuptarget.TypeLocal, Path: remoteDir}
	if err := AddTarget(ctx, nas, false, false); err != nil {
		t.Fatal(err)
	}
	if err := AddTarget(ctx, nas, false, false); err == nil {
		t.Error("a duplicate target was added without --replace")
	}
	if err := AddTarget(ctx, backuptarget.Target{Name: "bad", Type: backuptarget.TypeLocal, Path: "/proc/ffm-nope/x"}, false, false); err == nil {
		t.Error("an unusable target was saved")
	}
	if st, _ := os.Stat(filepath.Join(os.Getenv("FFM_CONFIG_DIR"), "backup-targets.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("targets file mode %v", st.Mode().Perm())
	}
	if objs, _ := os.ReadDir(filepath.Join(remoteDir, ".ffm-test")); len(objs) != 0 {
		t.Error("the target test left its object behind")
	}

	// Five daily scheduled archives, encrypted and uploaded.
	dir := filepath.Join(root, "alpha")
	os.MkdirAll(dir, 0o700)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h := NewHeader(fullBench(), "s", "16", "", []string{TierCore}, base.Add(time.Duration(i)*24*time.Hour))
		h.BenchName, h.Trigger = "alpha", TriggerScheduled
		plain := writeArchive(t, dir, archiveFileName("alpha", TriggerScheduled, h.CreatedAt), h)
		raw, _ := json.Marshal(h)
		enc, err := encryptArchive(plain, raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := uploadToTargets([]string{"nas"}, "alpha", enc, DiscardProgress{}); err != nil {
			t.Fatal(err)
		}
	}
	remote, err := ListRemoteArchives(ctx, "nas", "alpha")
	if err != nil || len(remote) != 5 || remote[0].Header.CreatedAt.Before(remote[4].Header.CreatedAt) {
		t.Fatalf("remote listing: %d archives, %v", len(remote), err)
	}
	// The floor keeps 3 whatever the policy says.
	n, err := pruneRemote(ctx, "nas", "alpha", state.BackupPolicy{KeepDaily: 1}, time.UTC)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v; want 2 (5 minus the floor of 3)", n, err)
	}
	if left, _ := filepath.Glob(filepath.Join(remoteDir, "alpha", "*.header.json")); len(left) != 3 {
		t.Errorf("%d sidecars left, want 3", len(left))
	}

	// Pull the newest into an empty local directory and decrypt it.
	for _, f := range mustGlob(t, filepath.Join(dir, "*")) {
		os.Remove(f)
	}
	local, err := PullArchive(ctx, "nas", "alpha", "", DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(local, "T000000Z.auto.ffm.tar.age") || !strings.Contains(local, "20260105") {
		t.Errorf("pulled %s, want the newest", local)
	}
	if _, cleanup, err := decryptForRestore(local, idFile); err != nil {
		t.Fatalf("the pulled archive does not decrypt: %v", err)
	} else {
		cleanup()
	}
	if _, err := PullArchive(ctx, "nas", "alpha", "", DiscardProgress{}); err == nil {
		t.Error("an existing local archive was overwritten by a pull")
	}

	// A target a schedule uses cannot be removed.
	s := New(false)
	if err := s.AddBench(state.Bench{Name: "alpha", WebPort: 8000, SocketIOPort: 9000,
		BackupSchedule: &state.BackupPolicy{Enabled: true, EveryHours: 24, Targets: []string{"nas"}}}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTarget("nas", s); err == nil {
		t.Error("a target in use by a schedule was removed")
	}
}

func TestBackupToUnknownTargetRefusedEarly(t *testing.T) {
	fake := fakeExecForBench(t)
	s, _ := newTestBench(t, state.Bench{Name: "bt", Mode: "dev", SiteName: "bt.localhost", WebPort: 8000, SocketIOPort: 9000})
	if err := s.Backup(BackupInput{BenchName: "bt", To: []string{"nowhere"}}, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "no backup target") {
		t.Errorf("err = %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("work started: %v", fake.Calls())
	}
}

func mustGlob(t *testing.T, p string) []string {
	t.Helper()
	m, err := filepath.Glob(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fakeExecForBench(t *testing.T) *fakeexec.Fake {
	t.Helper()
	return fakeexec.Install(t)
}

func TestUploadRefusesPlaintext(t *testing.T) {
	d, err := backuptarget.Open(context.Background(), backuptarget.Target{Name: "nas", Type: backuptarget.TypeLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "a.ffm.tar")
	os.WriteFile(plain, []byte("secrets"), 0o600)
	if err := uploadArchive(context.Background(), d, "a", plain); err == nil || !strings.Contains(err.Error(), "only age-encrypted") {
		t.Errorf("err = %v", err)
	}
}

// The scheduled path enters backupLocked directly; an upload there must
// still force encryption (it did not, and a plaintext archive reached S3 in a
// live test).
func TestScheduledUploadForcesEncryption(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	in := BackupInput{BenchName: "x", To: []string{"nas"}}
	if err := prepareEncryption(&in); err == nil {
		t.Fatal("an unknown target passed")
	}
	if err := AddTarget(context.Background(), backuptarget.Target{Name: "nas", Type: backuptarget.TypeLocal, Path: t.TempDir()}, false, true); err != nil {
		t.Fatal(err)
	}
	if err := prepareEncryption(&in); err != ErrNoRecipients {
		t.Fatalf("no key: err = %v, want ErrNoRecipients", err)
	}
	if _, _, err := InitBackupKey(filepath.Join(t.TempDir(), "id"), false); err != nil {
		t.Fatal(err)
	}
	if err := prepareEncryption(&in); err != nil || !in.Encrypt {
		t.Errorf("encrypt=%v err=%v; an upload must imply encryption", in.Encrypt, err)
	}
}
