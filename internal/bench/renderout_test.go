package bench

import (
	"os"
	"strconv"
	"testing"
)

// TestRenderToPath is a scaffolding helper, not an assertion: it renders a
// Dockerfile to FFM_RENDER_OUT so the generated artifact can be built by
// docker for real. Skipped unless that variable is set.
func TestRenderToPath(t *testing.T) {
	out := os.Getenv("FFM_RENDER_OUT")
	if out == "" {
		t.Skip("FFM_RENDER_OUT not set")
	}
	uid, _ := strconv.Atoi(os.Getenv("FFM_RENDER_UID"))
	gid, _ := strconv.Atoi(os.Getenv("FFM_RENDER_GID"))
	mode := os.Getenv("FFM_RENDER_MODE")
	if mode == "" {
		mode = "prod"
	}
	if err := WriteDockerfile(out, ComposeData{
		Mode: mode, DBType: "mariadb", HostUID: uid, HostGID: gid,
	}); err != nil {
		t.Fatalf("WriteDockerfile: %v", err)
	}
	t.Logf("rendered %s Dockerfile (uid=%d gid=%d) to %s/Dockerfile", mode, uid, gid, out)
}
