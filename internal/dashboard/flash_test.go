package dashboard

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlashKeepsErrorTextOutOfURL(t *testing.T) {
	secretErr := "bench new-site: exit status 1\nAccess denied (password: s3cret-db)"
	rec := httptest.NewRecorder()
	redirectWithFlash(rec, httptest.NewRequest("POST", "/admin/benches/x/start", nil), "/admin/benches/x", "", secretErr)
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "s3cret") || strings.Contains(loc, "Access") {
		t.Fatalf("error text leaked into the redirect URL: %s", loc)
	}
	req := httptest.NewRequest("GET", loc, nil)
	ok, errMsg := flashFromQuery(req)
	if errMsg != secretErr || ok != "" {
		t.Fatalf("flash = %q / %q", ok, errMsg)
	}
	if _, again := flashFromQuery(req); again != "" {
		t.Fatal("flash shown twice")
	}
}
