//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSaveKeepsOwnerWhenRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root on Unix")
	}
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", dir)
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(dir, "benches"))
	path := filepath.Join(dir, "benches.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 4242, 4343); err != nil {
		t.Fatal(err)
	}
	if err := (&Store{path: path}).Add(Bench{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 4242 || st.Gid != 4343 {
		t.Fatalf("owner after a root save = %d:%d, want 4242:4343", st.Uid, st.Gid)
	}
}
