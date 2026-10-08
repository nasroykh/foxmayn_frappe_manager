package manager

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
)

func gzipBytes(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// fullArchive writes a header, a gzip dump and a manifest that lists it.
func fullArchive(t *testing.T, dir string, dump []byte, tamper bool) string {
	t.Helper()
	h := NewHeader(fullBench(), "s.localhost", "16", "", []string{TierCore}, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(dir, archiveFileName("alpha", TriggerManual, h.CreatedAt))
	w, err := archive.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	hraw, _ := json.Marshal(h)
	w.AddBytes(archive.HeaderName, hraw, archive.EncodingNone)
	// Real archives record the dump as gzip; only gpg means "unreadable".
	mem, err := w.AddBytes(archive.Prefix+"/db/database.sql.gz", dump, archive.EncodingGzip)
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		mem.SHA256 = strings.Repeat("0", 64)
	}
	m := Manifest{Header: h, Bench: fullBench(), Members: []archive.Member{mem}}
	mraw, _ := json.Marshal(m)
	w.AddBytes(archive.ManifestName, mraw, archive.EncodingNone)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyBackup(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	t.Setenv("FFM_BACKUPS_DIR", t.TempDir())
	t.Setenv("FFM_AGE_IDENTITY_FILE", "")
	s := New(false)
	dir := t.TempDir()

	good := fullArchive(t, dir, gzipBytes(strings.Repeat("x", 3<<20)+"CREATE TABLE `__Auth` (...);"), false)
	res, err := s.VerifyBackup(VerifyInput{Archive: good}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DumpChecked || res.Members != 1 || res.Header.BenchName == "" {
		t.Errorf("result %+v", res)
	}

	noAuth := filepath.Join(t.TempDir(), "x")
	os.MkdirAll(noAuth, 0o700)
	if _, err := s.VerifyBackup(VerifyInput{Archive: fullArchive(t, noAuth, gzipBytes("CREATE TABLE tabUser"), false)}, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "__Auth") {
		t.Errorf("dump without __Auth: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "y")
	os.MkdirAll(bad, 0o700)
	if _, err := s.VerifyBackup(VerifyInput{Archive: fullArchive(t, bad, gzipBytes("__Auth"), true)}, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("tampered member: %v", err)
	}

	// Encrypted: refused without the identity, verified with it.
	idFile := filepath.Join(t.TempDir(), "id")
	if _, _, err := InitBackupKey(idFile, false); err != nil {
		t.Fatal(err)
	}
	hraw, _ := json.Marshal(res.Header)
	enc, err := encryptArchive(good, hraw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyBackup(VerifyInput{Archive: enc}, DiscardProgress{}); err == nil {
		t.Error("an encrypted archive verified without the identity")
	}
	res, err = s.VerifyBackup(VerifyInput{Archive: enc, Identity: idFile}, DiscardProgress{})
	if err != nil || !res.Encrypted {
		t.Errorf("encrypted: %+v %v", res, err)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("FFM_BACKUPS_DIR"), ".verify-*")); len(left) != 0 {
		t.Errorf("work directories left: %v", left)
	}
}

func TestStreamContainsAcrossChunks(t *testing.T) {
	data := strings.Repeat("a", (1<<20)-3) + "__Auth" + "zz"
	ok, err := streamContains(strings.NewReader(data), []byte("__Auth"))
	if err != nil || !ok {
		t.Errorf("needle across a chunk boundary: %v %v", ok, err)
	}
}
