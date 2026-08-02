package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Temporary verification of the --match-host-user template branch.
func renderDockerfile(t *testing.T, d ComposeData) string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteDockerfile(dir, d); err != nil {
		t.Fatalf("WriteDockerfile: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestDockerfileRemapRendering(t *testing.T) {
	for _, mode := range []string{"dev", "prod"} {
		off := renderDockerfile(t, ComposeData{Mode: mode, DBType: "mariadb"})
		if strings.Contains(off, "usermod") {
			t.Errorf("%s: remap rendered when HostUID is zero:\n%s", mode, off)
		}
		if !strings.Contains(off, "FROM docker.io/frappe/bench:latest") {
			t.Errorf("%s: base image line missing", mode)
		}

		on := renderDockerfile(t, ComposeData{Mode: mode, DBType: "mariadb", HostUID: 1001, HostGID: 1002})
		for _, want := range []string{
			`[ "$(id -u frappe)" != "1001" ]`,
			`[ "$(id -g frappe)" != "1002" ]`,
			"usermod -u 1001 frappe",
			"groupmod -g 1002 frappe",
			"usermod -g 1002 frappe",
			"chown -R 1001:1002 /home/frappe",
		} {
			if !strings.Contains(on, want) {
				t.Errorf("%s: missing %q in:\n%s", mode, want, on)
			}
		}
		// The remap must precede everything that writes into /home/frappe.
		// Match the directive at line start — the explanatory comment above it
		// also contains the words "USER frappe".
		if i, j := strings.Index(on, "usermod -u"), strings.Index(on, "\nUSER frappe"); i < 0 || j < 0 || i > j {
			t.Errorf("%s: remap (%d) must come before the USER frappe directive (%d)", mode, i, j)
		}
	}
}
