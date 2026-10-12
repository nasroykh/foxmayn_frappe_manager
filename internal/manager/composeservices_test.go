package manager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func countCalls(f *fakeexec.Fake, part string) int {
	n := 0
	for _, c := range f.Calls() {
		if strings.Contains(c, part) {
			n++
		}
	}
	return n
}

func TestValidateServiceUsesComposeAndCaches(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " config --services", Stdout: "mariadb\nfrappe\n"})
	s, b := newTestBench(t, state.Bench{Name: "svc1", Mode: "dev", SiteName: "svc1.localhost", WebPort: 8000, SocketIOPort: 9000})

	if err := s.ValidateService("svc1", "frappe"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"nope", "frappe;id", "", "--rm"} {
		err := s.ValidateService("svc1", bad)
		if !errors.Is(err, ErrUnknownService) {
			t.Errorf("ValidateService(%q) = %v, want ErrUnknownService", bad, err)
		}
	}
	if n := countCalls(fake, " config --services"); n != 1 {
		t.Fatalf("docker compose config ran %d times for 5 checks, want 1 (cached)", n)
	}

	// Rewriting the compose file (reconcile, domain change) invalidates it.
	compose := filepath.Join(b.Dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services: {a: {}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(compose, future, future); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateService("svc1", "mariadb"); err != nil {
		t.Fatal(err)
	}
	if n := countCalls(fake, " config --services"); n != 2 {
		t.Fatalf("compose config ran %d times after the file changed, want 2", n)
	}
}

func TestComposeServicesUnknownBench(t *testing.T) {
	fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "svc2", Mode: "dev"})
	if _, err := s.ComposeServices("ghost"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("err = %v, want state.ErrNotFound", err)
	}
}
