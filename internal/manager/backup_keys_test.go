package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/agecrypt"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestBackupKeyInit(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	if _, err := BackupRecipients(); err != ErrNoRecipients {
		t.Fatalf("no key yet: err = %v", err)
	}
	out := filepath.Join(t.TempDir(), "id.txt")
	id, rec, err := InitBackupKey(out, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "AGE-SECRET-KEY-") || !strings.HasPrefix(rec, "age1") {
		t.Errorf("identity %q… recipient %q", id[:15], rec)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Errorf("identity file mode %v", st.Mode().Perm())
	}
	// ffm keeps only the public key.
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("FFM_CONFIG_DIR"), "backup-recipients.txt"))
	if strings.Contains(string(raw), "AGE-SECRET-KEY") || !strings.Contains(string(raw), rec) {
		t.Errorf("recipients file: %q", raw)
	}
	if _, _, err := InitBackupKey("", false); err == nil {
		t.Error("a second key was created without --add")
	}
	if _, _, err := InitBackupKey(out, true); err == nil {
		t.Error("an existing identity file was overwritten")
	}
	if _, _, err := InitBackupKey("", true); err != nil {
		t.Fatal(err)
	}
	if err := AddBackupRecipient("age1nope"); err == nil {
		t.Error("an invalid recipient was accepted")
	}
	if err := AddBackupRecipient(rec); err != nil {
		t.Fatal(err)
	}
	if lines, _ := RecipientLines(); len(lines) != 2 {
		t.Errorf("recipients = %v, want 2 (duplicates skipped)", lines)
	}
}

func TestEncryptedArchivesListPruneAndRestoreGate(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	t.Setenv("FFM_BACKUPS_DIR", root)
	t.Setenv("FFM_AGE_IDENTITY_FILE", "")
	idFile := filepath.Join(t.TempDir(), "id.txt")
	if _, _, err := InitBackupKey(idFile, false); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "alpha")
	os.MkdirAll(dir, 0o700)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var paths []string
	for i := 0; i < 5; i++ {
		h := NewHeader(fullBench(), "s", "16", "", []string{TierCore}, base.Add(time.Duration(i)*24*time.Hour))
		h.BenchName, h.Trigger = "alpha", TriggerScheduled
		plain := writeArchive(t, dir, archiveFileName("alpha", TriggerScheduled, h.CreatedAt), h)
		raw, _ := json.Marshal(h)
		enc, err := encryptArchive(plain, raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(plain); !os.IsNotExist(err) {
			t.Fatal("the plaintext archive was kept")
		}
		paths = append(paths, enc)
	}
	got, err := ScanArchives("alpha")
	if err != nil || len(got) != 5 {
		t.Fatalf("scan: %d archives, %v", len(got), err)
	}
	for _, a := range got {
		if !a.Encrypted || a.Err != nil || a.Header.BenchName != "alpha" {
			t.Errorf("archive %s: encrypted=%v err=%v", filepath.Base(a.Path), a.Encrypted, a.Err)
		}
	}
	res, err := pruneWithPolicy("alpha", state.BackupPolicy{KeepDaily: 1}, false, base.Add(10*24*time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Removed {
		if _, err := os.Stat(sidecarPath(a.Path)); !os.IsNotExist(err) {
			t.Errorf("sidecar of pruned %s kept", filepath.Base(a.Path))
		}
	}

	// Restore refuses without an identity and decrypts with one.
	if _, _, err := decryptForRestore(paths[4], ""); err == nil || !strings.Contains(err.Error(), "--identity") {
		t.Errorf("no identity: err = %v", err)
	}
	plain, cleanup, err := decryptForRestore(paths[4], idFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plain, ".ffm.tar") || agecrypt.IsEncrypted(plain) {
		t.Errorf("decrypted to %s", plain)
	}
	if st, _ := os.Stat(filepath.Dir(plain)); st.Mode().Perm() != 0o700 {
		t.Errorf("decrypt dir mode %v", st.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Error("the decrypted copy outlived the restore")
	}
}
