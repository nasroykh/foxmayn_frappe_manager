package state

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
		// benches.json.lock is the writers' lock file: it stays, only the OS
		// lock on it means anything. Temp files must not.
		if e.Name() != "benches.json" && e.Name() != "benches" && e.Name() != "benches.json.lock" {
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

// Concurrent writers must not lose updates. Before the lock, each Update read
// the whole file, changed one record and wrote everything back, so writers
// racing each other overwrote one another's changes.
func TestConcurrentUpdatesAreNotLost(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "benches.json")}
	const n = 20
	for i := 0; i < n; i++ {
		if err := s.Add(Bench{Name: fmt.Sprintf("b%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A second store on the same file stands in for another process.
			other := &Store{path: s.path}
			if err := other.Update(fmt.Sprintf("b%d", i), func(b *Bench) { b.Domain = "done" }); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	benches, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range benches {
		if b.Domain != "done" {
			t.Errorf("update to %s was lost", b.Name)
		}
	}
}
