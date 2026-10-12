package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }

func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(dir, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(dir, "backups"))
	fakeexec.Install(t)
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	isolate(t)
	h, err := New(WithAdminPassword("secret")).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCheckListen(t *testing.T) {
	cases := []struct {
		addr     string
		tls, ins bool
		wantErr  bool
	}{
		{"127.0.0.1:8787", false, false, false},
		{"127.5.5.5:8787", false, false, false},
		{"[::1]:8787", false, false, false},
		{"localhost:8787", false, false, false},
		{"0.0.0.0:8787", false, false, true},
		{":8787", false, false, true},
		{"[::]:8787", false, false, true},
		{"192.168.1.10:8787", false, false, true},
		{"example.com:8787", false, false, true},
		{"0.0.0.0:8787", true, false, false},
		{"0.0.0.0:8787", false, true, false},
		{"192.168.1.10:8787", true, true, false},
		{"not-an-address", false, false, true},
	}
	for _, c := range cases {
		err := checkListen(c.addr, c.tls, c.ins)
		if (err != nil) != c.wantErr {
			t.Errorf("checkListen(%q, tls=%v, insecure=%v) = %v, wantErr %v", c.addr, c.tls, c.ins, err, c.wantErr)
		}
	}
}

// Run enforces it, so the background daemon, which only calls Run, is covered.
func TestRunRefusesNonLoopbackWithoutTLS(t *testing.T) {
	isolate(t)
	err := New(WithAddr("0.0.0.0:0"), WithAdminPassword("secret")).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to listen") {
		t.Fatalf("Run on 0.0.0.0 without TLS = %v, want a refusal", err)
	}
	err = New(WithAddr("127.0.0.1:0"), WithTLS("cert.pem", "")).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "both a certificate and a key") {
		t.Fatalf("Run with half a TLS pair = %v", err)
	}
}

func TestRunServesAndStopsOnLoopback(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(WithAddr("127.0.0.1:0"), WithAdminPassword("secret")).Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func assertHeaders(t *testing.T, what string, rr *httptest.ResponseRecorder, wantHSTS bool) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
			"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'",
		"X-Frame-Options":            "DENY",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", what, k, got, v)
		}
	}
	if got := rr.Header().Get("Strict-Transport-Security"); (got != "") != wantHSTS {
		t.Errorf("%s: Strict-Transport-Security = %q, want present=%v", what, got, wantHSTS)
	}
}

func TestSecurityHeadersOn200And401AndHSTSOnlyOnTLS(t *testing.T) {
	h := testHandler(t)

	// 200: the public health check and an authenticated static file.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/health: %d", rr.Code)
	}
	assertHeaders(t, "/health", rr, false)

	req := httptest.NewRequest(http.MethodGet, "/admin/static/admin.js", nil)
	req.SetBasicAuth("u", "secret")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("static: %d", rr.Code)
	}
	assertHeaders(t, "static 200", rr, false)

	// 401 carries them too.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("/admin without credentials: %d", rr.Code)
	}
	assertHeaders(t, "401", rr, false)

	// HSTS appears under TLS only.
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.TLS = &tls.ConnectionState{}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assertHeaders(t, "401 over TLS", rr, true)
}

func TestSecurityHeadersOnCrossOriginRejection(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/proxy/start", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d, want 403", rr.Code)
	}
	assertHeaders(t, "cross-site 403", rr, false)
}

func TestCrossSitePostIsRefusedBeforeAnyHandler(t *testing.T) {
	h := testHandler(t)
	for _, hdr := range []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
		{"Origin": "https://evil.example"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/benches/alpha/delete", nil)
		req.SetBasicAuth("u", "secret")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%v: status %d, want 403", hdr, rr.Code)
		}
	}
	// A same-origin request reaches the handler (which then wants a CSRF token).
	req := httptest.NewRequest(http.MethodPost, "/admin/benches/alpha/delete", nil)
	req.SetBasicAuth("u", "secret")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "csrf") {
		t.Errorf("same-origin POST without token: %d %q, want the handler's csrf 403", rr.Code, rr.Body.String())
	}
}

func TestExecRouteIs404ByDefault(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/benches/alpha/exec", nil)
	req.SetBasicAuth("u", "secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("exec while off: %d, want 404", rr.Code)
	}
}
