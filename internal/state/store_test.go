package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", dir)
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(dir, "benches"))
	path := filepath.Join(dir, "benches.json")

	// An older ffm wrote the file 0644; saving must tighten it.
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path}
	if err := s.Add(Bench{Name: "one", AdminPassword: "secret"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("one")
	if err != nil || got.AdminPassword != "secret" {
		t.Fatalf("Get after Add = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("state file mode = %o, want 600", mode)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "benches.json" && e.Name() != "benches" {
			t.Errorf("leftover file after save: %s", e.Name())
		}
	}
}

func TestPublishHost(t *testing.T) {
	cases := []struct {
		b    Bench
		want string
	}{
		{Bench{Mode: "prod"}, "127.0.0.1"},
		{Bench{Mode: "dev"}, ""},
		{Bench{}, ""},
		{Bench{Mode: "dev", Bind: BindLoopback}, "127.0.0.1"},
		{Bench{Mode: "prod", Bind: BindLAN}, ""},
	}
	for _, c := range cases {
		if got := c.b.PublishHost(); got != c.want {
			t.Errorf("%+v: PublishHost() = %q, want %q", c.b, got, c.want)
		}
	}
}
