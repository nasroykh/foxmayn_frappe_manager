package tunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func srv(name string) Server {
	return Server{Name: name, Host: "vps.example.com", Port: 7000, Token: "tok-" + name, BaseDomain: "t.example.com", TLS: true}
}

func TestConcurrentPutServerLosesNothing(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := PutServer(srv(fmt.Sprintf("s%d", i)), false); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 20 {
		t.Fatalf("%d servers saved, want 20", len(cfg.Servers))
	}
	st, err := os.Stat(filepath.Join(os.Getenv("FFM_CONFIG_DIR"), "tunnel.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("tunnel.json mode %v, want 0600", st.Mode().Perm())
	}
}

func TestServerProfileOps(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	if err := PutServer(Server{Name: "x"}, false); err == nil {
		t.Fatal("empty profile accepted")
	}
	for _, n := range []string{"b", "a", "c"} {
		if err := PutServer(srv(n), false); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := Load()
	if cfg.Default != "b" {
		t.Fatalf("default = %q, want the first profile b", cfg.Default)
	}
	if err := UseServer("c"); err != nil {
		t.Fatal(err)
	}
	if err := UseServer("missing"); err == nil {
		t.Fatal("unknown profile became default")
	}
	next, err := RemoveServer("c")
	if err != nil || next != "a" {
		t.Fatalf("RemoveServer(default) = %q, %v; want the alphabetically first remaining, a", next, err)
	}
	if _, err := RemoveServer("c"); err == nil {
		t.Fatal("removing a missing profile succeeded")
	}
}
