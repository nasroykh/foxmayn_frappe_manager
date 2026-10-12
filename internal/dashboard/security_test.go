package dashboard

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

// --- no inline script, no third-party origin ---

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	inlineEvent = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	styleAttr   = regexp.MustCompile(`(?i)\sstyle\s*=`)
)

func TestRenderedPageLoadsOnlyOwnFiles(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	for _, path := range []string{"/admin", "/admin/benches", "/admin/benches/alpha", "/admin/benches/new", "/admin/proxy", "/admin/tunnel-servers", "/admin/jobs"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("u", "secret")
		req.SetPathValue("name", "alpha")
		rr := httptest.NewRecorder()
		switch path {
		case "/admin":
			h.Dashboard(rr, req)
		case "/admin/benches":
			h.BenchesList(rr, req)
		case "/admin/benches/alpha":
			h.BenchDetail(rr, req)
		case "/admin/benches/new":
			h.BenchNew(rr, req)
		case "/admin/proxy":
			h.ProxyPage(rr, req)
		case "/admin/tunnel-servers":
			h.TunnelServers(rr, req)
		case "/admin/jobs":
			h.JobsList(rr, req)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rr.Code)
		}
		assertSelfContained(t, path, rr.Body.String())
	}
}

func assertSelfContained(t *testing.T, what, page string) {
	t.Helper()
	for _, m := range scriptTag.FindAllStringSubmatch(page, -1) {
		if !strings.Contains(m[1], `src="/admin/static/`) {
			t.Errorf("%s: inline or foreign <script%s>", what, m[1])
		}
	}
	for _, bad := range []string{"cdn.tailwindcss.com", "googleapis.com", "gstatic.com", "@import"} {
		if strings.Contains(page, bad) {
			t.Errorf("%s: page references %s", what, bad)
		}
	}
	// Anything fetched by the page (script, stylesheet, image) must be local;
	// the footer's plain <a href="https://..."> is a link, not a load.
	for _, m := range regexp.MustCompile(`(?is)<(?:link|img|script|iframe|embed|object)\b[^>]*>`).FindAllString(page, -1) {
		if strings.Contains(m, "http://") || strings.Contains(m, "https://") || strings.Contains(m, `="//`) {
			t.Errorf("%s: external load in %s", what, m)
		}
	}
	if inlineEvent.MatchString(page) {
		t.Errorf("%s: inline event handler attribute", what)
	}
	if styleAttr.MatchString(page) {
		t.Errorf("%s: inline style attribute (blocked by style-src 'self')", what)
	}
}

// The templates themselves, including pages no handler test renders.
func TestTemplatesHaveNoInlineScriptOrHandlers(t *testing.T) {
	err := fs.WalkDir(embedFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := embedFS.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		for _, m := range scriptTag.FindAllStringSubmatch(src, -1) {
			if !strings.Contains(m[1], `src="/admin/static/`) {
				t.Errorf("%s: inline <script%s>", path, m[1])
			}
		}
		if inlineEvent.MatchString(src) {
			t.Errorf("%s: inline event handler attribute", path)
		}
		if styleAttr.MatchString(src) {
			t.Errorf("%s: inline style attribute", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	css, err := embedFS.ReadFile("static/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), "@import") || strings.Contains(string(css), "http") {
		t.Error("admin.css loads from another origin")
	}
	tw, err := embedFS.ReadFile("static/tailwind.css")
	if err != nil || len(tw) < 1000 {
		t.Fatalf("static/tailwind.css missing or empty (run make dashboard-css): %v", err)
	}
}

// --- failed-login limiter ---

func authReq(addr, pass string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/admin/proxy", nil)
	req.RemoteAddr = addr
	if pass != "" {
		req.SetBasicAuth("u", pass)
	}
	return req
}

func TestAuthLimiterLocksOutAfterFiveFailures(t *testing.T) {
	h, _ := newTestHandler(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	h.limiter.now = func() time.Time { return now }

	// A request with no credentials is the browser's first probe, never a failure.
	for i := 0; i < 10; i++ {
		rr := httptest.NewRecorder()
		h.ProxyPage(rr, authReq("203.0.113.9:5000", ""))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("probe %d: %d", i, rr.Code)
		}
	}

	for i := 1; i <= 5; i++ {
		rr := httptest.NewRecorder()
		h.ProxyPage(rr, authReq("203.0.113.9:5000", "wrong"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want 401", i, rr.Code)
		}
	}
	// The 6th request is refused, even with the right password.
	rr := httptest.NewRecorder()
	h.ProxyPage(rr, authReq("203.0.113.9:5000", "secret"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: status %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}

	// Another address is unaffected, and the port does not matter.
	rr = httptest.NewRecorder()
	h.ProxyPage(rr, authReq("198.51.100.7:5000", "secret"))
	if rr.Code != http.StatusOK {
		t.Fatalf("other IP: status %d, want 200", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ProxyPage(rr, authReq("203.0.113.9:6001", "secret"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("same IP, new port: status %d, want 429", rr.Code)
	}

	// After the backoff the right password works again.
	now = now.Add(authBackoffBase + time.Second)
	rr = httptest.NewRecorder()
	h.ProxyPage(rr, authReq("203.0.113.9:5000", "secret"))
	if rr.Code != http.StatusOK {
		t.Fatalf("after backoff: status %d, want 200", rr.Code)
	}
}

func TestAuthLimiterBackoffGrowsAndIsCapped(t *testing.T) {
	l := newAuthLimiter()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	want := authBackoffBase
	for round := 0; round < 12; round++ {
		for i := 0; i < authFailsPerWindow; i++ {
			l.fail("ip")
		}
		ok, retry := l.check("ip")
		if ok {
			t.Fatalf("round %d: not locked out", round)
		}
		if retry != want {
			t.Fatalf("round %d: backoff %v, want %v", round, retry, want)
		}
		now = now.Add(want + time.Second)
		if want *= 2; want > authBackoffMax {
			want = authBackoffMax
		}
	}
}

func TestAuthLimiterGlobalCap(t *testing.T) {
	l := newAuthLimiter()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	// Thirty addresses, a few guesses each: no single one is locked out.
	for i := 0; i < authGlobalFails; i++ {
		l.fail("10.0.0." + string(rune('a'+i)))
	}
	if ok, _ := l.check("brand-new-ip"); ok {
		t.Fatal("global cap did not engage")
	}
	now = now.Add(authWindow + time.Second)
	if ok, _ := l.check("brand-new-ip"); !ok {
		t.Fatal("global cap did not release after the window")
	}
}

// --- Secure cookie ---

func TestCSRFCookieSecureOnlyUnderTLS(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, withTLS := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		if withTLS {
			req.TLS = &tls.ConnectionState{}
		}
		rr := httptest.NewRecorder()
		h.ensureCSRF(rr, req)
		cookies := rr.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("cookies = %d", len(cookies))
		}
		if cookies[0].Secure != withTLS {
			t.Errorf("tls=%v: Secure = %v", withTLS, cookies[0].Secure)
		}
		if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie lost HttpOnly/SameSite=Strict: %+v", cookies[0])
		}
	}
}

// --- exec ---

const composeServicesOut = "frappe\nmariadb\nredis-cache\n"

func execRules() []fakeexec.Rule {
	return []fakeexec.Rule{
		{Match: " config --services", Stdout: composeServicesOut},
		{Match: " exec -T ", Stdout: "bench 15.0.0\n"},
	}
}

func postForm(h *Handler, handler http.HandlerFunc, path, name string, form url.Values, withCSRF bool) *httptest.ResponseRecorder {
	cookie, token := csrfPair(h)
	if withCSRF {
		form.Set(csrfFormField, token)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", name)
	if withCSRF {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func readAudit(t *testing.T) []auditEntry {
	t.Helper()
	f, err := os.Open(config.DashboardAuditFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	defer f.Close()
	var out []auditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e auditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("audit line is not JSON: %q", sc.Text())
		}
		out = append(out, e)
	}
	return out
}

func TestExecIs404UnlessAllowed(t *testing.T) {
	h, _ := newTestHandler(t, execRules()...)
	addBench(t, h, "alpha")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/admin/benches/alpha/exec", nil)
		req.SetBasicAuth("u", "secret")
		req.SetPathValue("name", "alpha")
		rr := httptest.NewRecorder()
		h.BenchExec(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s with exec off: %d, want 404", method, rr.Code)
		}
	}
	// The link is hidden on the bench page.
	req := httptest.NewRequest(http.MethodGet, "/admin/benches/alpha", nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", "alpha")
	rr := httptest.NewRecorder()
	h.BenchDetail(rr, req)
	if strings.Contains(rr.Body.String(), "/exec") {
		t.Error("bench page links to exec while it is off")
	}
	h.AllowExec = true
	rr = httptest.NewRecorder()
	h.BenchDetail(rr, req)
	if !strings.Contains(rr.Body.String(), "/admin/benches/alpha/exec") {
		t.Error("bench page hides the exec link while it is on")
	}
}

func TestExecBadCSRFIs403AndRunsNothing(t *testing.T) {
	h, fake := newTestHandler(t, execRules()...)
	h.AllowExec = true
	addBench(t, h, "alpha")
	rr := postForm(h, h.BenchExec, "/admin/benches/alpha/exec", "alpha",
		url.Values{"service": {"frappe"}, "command": {"id"}}, false)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rr.Code)
	}
	if fake.Called(" exec ") {
		t.Fatal("a command ran despite the bad CSRF token")
	}
}

func TestExecRejectsUnknownService(t *testing.T) {
	h, fake := newTestHandler(t, execRules()...)
	h.AllowExec = true
	addBench(t, h, "alpha")
	for _, svc := range []string{"nope", "frappe; reboot", "../etc", "--rm"} {
		rr := postForm(h, h.BenchExec, "/admin/benches/alpha/exec", "alpha",
			url.Values{"service": {svc}, "command": {"id"}}, true)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("service %q: status %d, want 400", svc, rr.Code)
		}
	}
	if fake.Called(" exec ") {
		t.Fatal("a command ran against an unknown service")
	}
}

func TestExecRunsAndIsAuditedVerbatim(t *testing.T) {
	h, fake := newTestHandler(t, execRules()...)
	h.AllowExec = true
	addBench(t, h, "alpha")
	const command = `bench --site alpha.localhost console <<< "frappe.db.sql('select 1')"; echo $HOME`
	rr := postForm(h, h.Audited(h.BenchExec), "/admin/benches/alpha/exec", "alpha",
		url.Values{"service": {"mariadb"}, "command": {command}}, true)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "bench 15.0.0") {
		t.Error("command output missing from the page")
	}
	if !fake.Called(" exec -T mariadb bash -c ") {
		t.Fatalf("exec not run on mariadb: %v", fake.Calls())
	}

	entries := readAudit(t)
	if len(entries) != 2 {
		t.Fatalf("audit has %d lines, want 2 (started, done): %+v", len(entries), entries)
	}
	if entries[0].Result != "started" {
		t.Errorf("first audit line = %+v, want result started (written before the run)", entries[0])
	}
	for _, e := range entries {
		if e.Command != command || e.Service != "mariadb" || e.Bench != "alpha" || e.IP == "" {
			t.Errorf("audit line not verbatim: %+v", e)
		}
	}
	fi, err := os.Stat(config.DashboardAuditFile())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("audit file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestLogsRejectUnknownService(t *testing.T) {
	h, _ := newTestHandler(t, execRules()...)
	addBench(t, h, "alpha")
	req := httptest.NewRequest(http.MethodGet, "/admin/benches/alpha/logs/stream?service=evil", nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", "alpha")
	rr := httptest.NewRecorder()
	h.LogsStream(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("stream: status %d, want 400", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.BenchLogs(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("page: status %d, want 400", rr.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/benches/alpha/logs?service=mariadb", nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", "alpha")
	rr = httptest.NewRecorder()
	h.BenchLogs(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `data-service="mariadb"`) {
		t.Fatalf("known service: status %d", rr.Code)
	}
}

// --- audit of every POST ---

func TestEveryPostIsAudited(t *testing.T) {
	// docker fails fast: the accepted POST starts a proxy job that would
	// otherwise try to bring the proxy up.
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	// Refused (no CSRF token) and accepted requests both leave a line.
	postForm(h, h.Audited(h.ProxyStart), "/admin/proxy/start", "", url.Values{}, false)
	postForm(h, h.Audited(h.ProxyStart), "/admin/proxy/start", "", url.Values{}, true)
	entries := readAudit(t)
	if len(entries) != 2 {
		t.Fatalf("audit has %d lines, want 2: %+v", len(entries), entries)
	}
	if entries[0].Result != "denied" || entries[0].Status != http.StatusForbidden {
		t.Errorf("refused POST audited as %+v", entries[0])
	}
	if entries[1].Status != http.StatusSeeOther {
		t.Errorf("accepted POST audited as %+v", entries[1])
	}
	for _, e := range entries {
		if e.Time.IsZero() || e.Action == "" {
			t.Errorf("incomplete audit line %+v", e)
		}
	}
}

// --- password reveal ---

func TestRevealRejectsGETAndMissingCSRF(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")

	req := httptest.NewRequest(http.MethodGet, "/admin/benches/alpha/reveal?field=admin", nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", "alpha")
	rr := httptest.NewRecorder()
	h.BenchReveal(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "adm1n-secret-pw") {
		t.Error("GET leaked the password")
	}

	rr = postForm(h, h.BenchReveal, "/admin/benches/alpha/reveal", "alpha", url.Values{"field": {"admin"}}, false)
	if rr.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF: %d, want 403", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "adm1n-secret-pw") {
		t.Error("403 leaked the password")
	}
}

func TestRevealReturnsNoStoreJSONAndIsAudited(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	for field, want := range map[string]string{"admin": "adm1n-secret-pw", "db": "db-secret-pw-77"} {
		rr := postForm(h, h.Audited(h.BenchReveal), "/admin/benches/alpha/reveal", "alpha", url.Values{"field": {field}}, true)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d", field, rr.Code)
		}
		if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control = %q", field, cc)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: Content-Type = %q", field, ct)
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["value"] != want {
			t.Errorf("%s: body %q", field, rr.Body.String())
		}
	}
	rr := postForm(h, h.BenchReveal, "/admin/benches/alpha/reveal", "alpha", url.Values{"field": {"nope"}}, true)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d, want 400", rr.Code)
	}

	raw, err := os.ReadFile(config.DashboardAuditFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "adm1n-secret-pw") || strings.Contains(string(raw), "db-secret-pw-77") {
		t.Fatal("audit log contains a password")
	}
	fields := map[string]bool{}
	for _, e := range readAudit(t) {
		fields[e.Field] = true
	}
	if !fields["admin"] || !fields["db"] {
		t.Errorf("reveal fields not audited: %+v", fields)
	}
}

func TestBenchDetailHasNoPasswords(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	req := httptest.NewRequest(http.MethodGet, "/admin/benches/alpha", nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", "alpha")
	rr := httptest.NewRecorder()
	h.BenchDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	for _, secret := range []string{"adm1n-secret-pw", "db-secret-pw-77"} {
		if strings.Contains(body, secret) {
			t.Errorf("detail page contains %q", secret)
		}
	}
	if !strings.Contains(body, `data-reveal-url="/admin/benches/alpha/reveal"`) {
		t.Error("detail page lost its reveal buttons")
	}
}

// --- persisted config ---

func TestConfigRoundTripsSecuritySettings(t *testing.T) {
	isolate(t)
	in := Config{
		ListenAddr: "0.0.0.0:8787", AdminPassword: "pw",
		TLSCert: "/etc/ffm/cert.pem", TLSKey: "/etc/ffm/key.pem", InsecureHTTP: true, AllowExec: true,
	}
	if err := SaveConfig(in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadConfig()
	if err != nil || out != in {
		t.Fatalf("LoadConfig = %+v, %v; want %+v", out, err, in)
	}
	// Defaults stay safe: nothing is on unless saved.
	if err := os.Remove(config.DashboardConfigFile()); err != nil {
		t.Fatal(err)
	}
	def, _ := LoadConfig()
	if def.AllowExec || def.InsecureHTTP || def.TLSCert != "" || def.ListenAddr != DefaultListenAddr {
		t.Fatalf("defaults = %+v", def)
	}
}
