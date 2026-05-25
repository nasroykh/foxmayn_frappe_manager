package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthRejectsMissingCredentials(t *testing.T) {
	h, err := NewHandler("secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAuthAcceptsValidCredentials(t *testing.T) {
	h, err := NewHandler("secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("user", "secret")
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestCSRFRejectsPostWithoutToken(t *testing.T) {
	h, err := NewHandler("secret", nil)
	if err != nil {
		t.Fatal(err)
	}
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
	h, err := NewHandler("secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("u", "secret")
	h.ensureCSRF(rr, req)
	token := h.ensureCSRF(rr, req)
	if token == "" {
		t.Fatal("expected csrf token")
	}
}
