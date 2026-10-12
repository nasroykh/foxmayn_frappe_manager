package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveStaleBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ffm.exe")
	old := exe + ".old"
	if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if removeStaleBinaryOf("linux", exe) {
		t.Fatal("removed a file off Windows")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("the file is gone off Windows")
	}
	if !removeStaleBinaryOf("windows", exe) {
		t.Fatal("did not remove ffm.exe.old")
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("ffm.exe.old survived")
	}
	if removeStaleBinaryOf("windows", exe) {
		t.Fatal("reported a removal when nothing was there")
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("archive bytes")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	sums := []byte(strings.Join([]string{
		"0000000000000000000000000000000000000000000000000000000000000000  ffm_0.9.0_linux_arm64.tar.gz",
		good + "  ffm_0.9.0_linux_amd64.tar.gz",
	}, "\n"))

	if err := verifyChecksum(sums, "ffm_0.9.0_linux_amd64.tar.gz", data); err != nil {
		t.Fatalf("valid archive rejected: %v", err)
	}
	if err := verifyChecksum(sums, "ffm_0.9.0_linux_arm64.tar.gz", data); err == nil {
		t.Fatal("mismatching checksum accepted")
	}
	if err := verifyChecksum(sums, "ffm_0.9.0_darwin_arm64.tar.gz", data); err == nil {
		t.Fatal("archive with no checksum entry accepted")
	}
	// A name that is only a suffix of another entry must not match it.
	if err := verifyChecksum(sums, "amd64.tar.gz", data); err == nil {
		t.Fatal("partial name matched a checksum entry")
	}
}
