package manager

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

func TestSeedKeyCoversEveryInput(t *testing.T) {
	base := seedKeyFor("", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, []string{"erpnext"})
	if base.id() != seedKeyFor("", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, []string{"erpnext"}).id() {
		t.Fatal("same inputs, different keys")
	}
	for name, k := range map[string]seedKey{
		"repo":   seedKeyFor("https://example.com/f.git", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, []string{"erpnext"}),
		"branch": seedKeyFor("", "version-16", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, []string{"erpnext"}),
		"python": seedKeyFor("", "version-15", bench.Toolchain{Python: "3.14", Node: "22"}, 0, 0, []string{"erpnext"}),
		"uid":    seedKeyFor("", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 1001, 1001, []string{"erpnext"}),
		"apps":   seedKeyFor("", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, []string{"erpnext", "hrms"}),
		"none":   seedKeyFor("", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, nil),
	} {
		if k.id() == base.id() {
			t.Errorf("changing %s keeps the key", name)
		}
	}
}

// fakeBenchTree builds a frappe-bench as create leaves it.
func fakeBenchTree(t *testing.T, root string) string {
	t.Helper()
	fb := filepath.Join(root, "frappe-bench")
	for _, d := range []string{"apps/frappe/frappe", "env/bin", "sites/assets/frappe", "sites/x.localhost/private", "logs", "config/pids", ".claude/skills", ".agents"} {
		os.MkdirAll(filepath.Join(fb, d), 0o755)
	}
	files := map[string]string{
		"apps/frappe/frappe/__init__.py":     "v",
		"sites/apps.txt":                     "frappe\n",
		"sites/common_site_config.json":      `{"db_type":"postgres","socketio_port":9030}`,
		seedCommonConfig:                     `{"socketio_port":9000}`,
		"sites/currentsite.txt":              "x.localhost",
		"sites/x.localhost/site_config.json": `{"db_password":"secret"}`,
		"logs/web.log":                       "log",
		".mcp.json":                          "{}",
		"Procfile":                           "web: bench serve\n",
	}
	for p, c := range files {
		os.WriteFile(filepath.Join(fb, p), []byte(c), 0o644)
	}
	os.Symlink("/workspace/frappe-bench/apps/frappe/frappe/public", filepath.Join(fb, "sites/assets/frappe/public"))
	return fb
}

func TestCaptureAndUseSeed(t *testing.T) {
	t.Setenv("FFM_SEEDS_DIR", t.TempDir())
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	k := seedKeyFor("", "version-15", bench.Toolchain{Python: "3.12", Node: "22"}, 0, 0, []string{"erpnext"})
	fb := fakeBenchTree(t, t.TempDir())

	if err := captureSeed(k, fb, "t01", now); err != nil {
		t.Fatal(err)
	}
	tree, meta := freshSeed(k, now.Add(time.Hour))
	if meta == nil || meta.From != "t01" {
		t.Fatalf("no fresh seed: %v", meta)
	}
	for _, gone := range []string{"sites/x.localhost", "sites/currentsite.txt", "logs/web.log", ".claude", ".agents", ".mcp.json", seedCommonConfig} {
		if _, err := os.Lstat(filepath.Join(tree, gone)); err == nil {
			t.Errorf("seed carries %s", gone)
		}
	}
	// apps, sites, config, logs and config/pids make a directory a bench.
	for _, kept := range []string{"apps/frappe/frappe/__init__.py", "sites/apps.txt", "Procfile", "env/bin", "logs", "config/pids"} {
		if _, err := os.Stat(filepath.Join(tree, kept)); err != nil {
			t.Errorf("seed lacks %s", kept)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(tree, "sites", "common_site_config.json")); string(raw) != `{"socketio_port":9000}` {
		t.Errorf("seed has the bench's own common_site_config: %s", raw)
	}
	if target, err := os.Readlink(filepath.Join(tree, "sites/assets/frappe/public")); err != nil || target != "/workspace/frappe-bench/apps/frappe/frappe/public" {
		t.Errorf("symlink not kept: %q %v", target, err)
	}

	// Copying it out gives a usable tree.
	dst := filepath.Join(t.TempDir(), "frappe-bench")
	if err := copyTree(tree, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "apps/frappe/frappe/__init__.py")); err != nil {
		t.Error("copied tree lacks the apps")
	}

	if _, m := freshSeed(k, now.Add(seedMaxAge+time.Hour)); m != nil {
		t.Error("a seed older than seedMaxAge was used")
	}
	other := seedKeyFor("", "version-16", bench.Toolchain{Python: "3.14", Node: "24"}, 0, 0, nil)
	if _, m := freshSeed(other, now); m != nil {
		t.Error("a seed was used for other inputs")
	}

	s := New(false)
	s.now = func() time.Time { return now }
	seeds, err := s.ListSeeds()
	if err != nil || len(seeds) != 1 || seeds[0].Branch != "version-15" || seeds[0].Stale {
		t.Errorf("ListSeeds = %+v, %v", seeds, err)
	}
}

func TestSeedsDisabledByEnv(t *testing.T) {
	t.Setenv("FFM_NO_SEED", "1")
	if seedsEnabled() {
		t.Error("FFM_NO_SEED=1 left seeds on")
	}
}
