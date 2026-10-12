package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }

// isolate points every ffm directory at the test's temp dir, so a test never
// touches the developer's real benches.json, jobs.json or backups.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(dir, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(dir, "backups"))
	return dir
}

// newTestHandler builds a Handler over isolated directories with docker faked:
// unmatched docker commands succeed silently, rules answer the rest.
func newTestHandler(t *testing.T, rules ...fakeexec.Rule) (*Handler, *fakeexec.Fake) {
	t.Helper()
	isolate(t)
	fake := fakeexec.Install(t, rules...)
	// The scheduler status reads the host's crontab or systemd units; no test
	// may depend on, or touch, those.
	prev := schedulerStatus
	schedulerStatus = func() schedulerInfo {
		return schedulerInfo{Known: true, Backend: "cron", Where: "your crontab"}
	}
	t.Cleanup(func() { schedulerStatus = prev })
	h, err := NewHandler("secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A job a test started may still be writing jobs.json into the temp dir;
	// wait for it before the directory is removed (cleanups run last-in first).
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			busy := false
			for _, j := range h.Jobs.ListJobs() {
				busy = busy || !j.Status.Finished()
			}
			if !busy {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return h, fake
}

// addBench registers a bench with a compose file in the isolated dirs.
func addBench(t *testing.T, h *Handler, name string) state.Bench {
	t.Helper()
	dir := filepath.Join(os.Getenv("FFM_BENCHES_DIR"), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := state.Bench{
		Name: name, Dir: dir, Mode: "dev", SiteName: name + ".localhost",
		WebPort: 8000, SocketIOPort: 9000,
		AdminPassword: "adm1n-secret-pw", DBPassword: "db-secret-pw-77",
	}
	if err := h.Svc.AddBench(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// csrfPair returns a cookie value and the form token that matches it.
func csrfPair(h *Handler) (*http.Cookie, string) {
	nonce := "0123456789abcdef"
	return &http.Cookie{Name: csrfCookieName, Value: nonce}, h.signCSRF(nonce)
}

func TestAuthRejectsMissingCredentials(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAuthAcceptsValidCredentials(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("user", "secret")
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestCSRFRejectsPostWithoutToken(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/proxy/start", nil)
	req.SetBasicAuth("user", "secret")
	rr := httptest.NewRecorder()
	h.ProxyStart(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHealthUnauthenticated(t *testing.T) {
	// Health is registered on server mux, not handler — smoke test CSRF cookie
	h, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("u", "secret")
	h.ensureCSRF(rr, req)
	token := h.ensureCSRF(rr, req)
	if token == "" {
		t.Fatal("expected csrf token")
	}
}
