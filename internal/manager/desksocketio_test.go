package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// A bench whose compose predates the Traefik socket.io router must keep an
// unpatched Desk: a port-less page would otherwise send socket.io to Traefik,
// which has no route for it on that compose.
func TestPatchDeskSocketioFollowsTemplate(t *testing.T) {
	const upstream = "\t\t\tfrappe.boot = {{ boot | json }};\n"
	for _, tc := range []struct {
		version int
		patched bool
	}{
		{bench.DeskSocketioTemplate - 1, false},
		{bench.DeskSocketioTemplate, true},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "workspace", "frappe-bench", "apps", "frappe", "frappe", "www", "app.html")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(upstream), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := patchDeskSocketio(state.Bench{Dir: dir, TemplateVersion: tc.version}); err != nil {
			t.Fatalf("template %d: %v", tc.version, err)
		}
		got, _ := os.ReadFile(path)
		if strings.Contains(string(got), "socketio_port") != tc.patched || bench.DeskSocketioPatched(dir) != tc.patched {
			t.Errorf("template %d: patched = %v, want %v:\n%s", tc.version, !tc.patched, tc.patched, got)
		}
	}
}
