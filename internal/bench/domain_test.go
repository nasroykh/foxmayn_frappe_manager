package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeDomain(t *testing.T) {
	ok := map[string]string{
		"erp.internal":        "erp.internal",
		"ERP.Internal":        "erp.internal",
		"  shop.test  ":       "shop.test",
		"a-b.c-d.example":     "a-b.c-d.example",
		"erp":                 "erp",
		"erp2.sub.test":       "erp2.sub.test",
		"xn--80ak6aa92e.test": "xn--80ak6aa92e.test",
	}
	for in, want := range ok {
		got, err := NormalizeDomain(in)
		if err != nil {
			t.Errorf("NormalizeDomain(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}

	// Every rejection below would otherwise be interpolated into a Traefik label
	// between backticks; the backtick and quote cases are label injection.
	bad := []string{
		"",
		"   ",
		"http://erp.internal",
		"erp.internal/admin",
		"erp.internal:8080",
		"erp.internal.",
		".erp.internal",
		"-erp.internal",
		"erp-.internal",
		"erp .internal",
		"erp`) || Host(`evil.test",
		`erp".test`,
		"erp,shop.test",
		"erp.internal$",
		strings.Repeat("a", 64) + ".test",
		strings.Repeat("a.", 130) + "test",
	}
	for _, in := range bad {
		if got, err := NormalizeDomain(in); err == nil {
			t.Errorf("NormalizeDomain(%q) = %q, want an error", in, got)
		}
	}
}

func TestRouterRules(t *testing.T) {
	dev := ComposeData{Name: "kb", Mode: "dev", DomainAliases: []string{"erp.internal", "shop.test"}}
	if got, want := dev.PrimaryHost(), "kb.localhost"; got != want {
		t.Errorf("dev PrimaryHost = %q, want %q", got, want)
	}
	wantRule := "Host(`kb.localhost`) || Host(`erp.internal`) || Host(`shop.test`)"
	if got := dev.RouterRule(); got != wantRule {
		t.Errorf("dev RouterRule = %q, want %q", got, wantRule)
	}

	bare := ComposeData{Name: "kb", Mode: "dev"}
	if got, want := bare.RouterRule(), "Host(`kb.localhost`)"; got != want {
		t.Errorf("aliasless RouterRule = %q, want %q", got, want)
	}
	if bare.HasAliases() {
		t.Error("HasAliases = true for a bench with no aliases")
	}

	prod := ComposeData{Name: "erp", Mode: "prod", Domain: "erp.example.com",
		DomainAliases: []string{"erp.internal"}}
	if got, want := prod.PrimaryHost(), "erp.example.com"; got != want {
		t.Errorf("prod PrimaryHost = %q, want %q", got, want)
	}
	// The prod alias rule must NOT contain the primary domain: Traefik derives one
	// certificate request per router, so a LAN-only alias sharing the primary
	// router would fail the ACME order for the real domain.
	if got := prod.AliasRule(); strings.Contains(got, prod.Domain) {
		t.Errorf("prod AliasRule = %q, must not include the primary domain", got)
	}
	if got, want := prod.AliasEntrypoint(), "web"; got != want {
		t.Errorf("AliasEntrypoint = %q, want %q", got, want)
	}
	prod.AliasTLS = true
	if got, want := prod.AliasEntrypoint(), "websecure"; got != want {
		t.Errorf("AliasEntrypoint with TLS = %q, want %q", got, want)
	}
}

// renderCompose writes a compose file to a temp dir and returns its contents.
func renderCompose(t *testing.T, data ComposeData) string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteCompose(dir, data); err != nil {
		t.Fatalf("WriteCompose: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	return string(out)
}

func TestComposeRendersDevAlias(t *testing.T) {
	base := ComposeData{
		Name: "kb", Mode: "dev", DBType: "mariadb", DBRootPassword: "pw",
		WebPort: 8000, WebPortEnd: 8005, SocketIOPort: 9000, SocketIOPortEnd: 9005,
	}

	plain := renderCompose(t, base)
	if !strings.Contains(plain, "rule=Host(`kb.localhost`)\"") {
		t.Errorf("aliasless dev compose lost its router rule:\n%s", plain)
	}

	base.DomainAliases = []string{"erp.internal"}
	withAlias := renderCompose(t, base)
	if !strings.Contains(withAlias, "rule=Host(`kb.localhost`) || Host(`erp.internal`)") {
		t.Errorf("dev alias missing from router rule:\n%s", withAlias)
	}
	// Dev must not route socket.io through Traefik: the dev client appends
	// socketio_port to the origin, so moving it to :80 breaks localhost access.
	if strings.Contains(withAlias, "PathPrefix(`/socket.io`)") {
		t.Errorf("dev compose must not add a socket.io router:\n%s", withAlias)
	}
}

func TestComposeRendersProdAlias(t *testing.T) {
	base := ComposeData{
		Name: "erp", Mode: "prod", DBType: "mariadb", DBRootPassword: "pw",
		WebPort: 8000, SocketIOPort: 9000, Domain: "erp.example.com",
		SiteName: "erp.example.com", MariaDBBufferPool: "1G", GunicornWorkers: 2,
		WorkerLongCount: 1, WorkerShortCount: 1,
		RedisCacheMaxmem: "512mb", RedisQueueMaxmem: "512mb",
		DomainAliases: []string{"erp.internal"},
	}

	http := renderCompose(t, base)
	for _, want := range []string{
		"routers.erp-alias.rule=Host(`erp.internal`)",
		"routers.erp-alias.entrypoints=web",
		"routers.erp-alias.service=erp\"",
		"routers.erp-alias-socketio.rule=(Host(`erp.internal`)) && PathPrefix(`/socket.io`)",
		"routers.erp-alias-socketio.entrypoints=web",
		"routers.erp-alias-socketio.service=erp-socketio",
		"middlewares.erp-alias-socketio-headers.headers.customRequestHeaders.Origin=\"",
		"middlewares.erp-alias-socketio-headers.headers.customRequestHeaders.X-Frappe-Site-Name=erp.example.com",
	} {
		if !strings.Contains(http, want) {
			t.Errorf("prod alias compose missing %q:\n%s", want, http)
		}
	}
	// Aliases default to plain HTTP so a LAN-only name never enters an ACME order.
	if strings.Contains(http, "erp-alias.tls.certresolver") {
		t.Errorf("prod alias must not request a certificate by default:\n%s", http)
	}
	// The primary router keeps its own rule and its own Origin middleware.
	if !strings.Contains(http, "routers.erp.rule=Host(`erp.example.com`)") {
		t.Errorf("prod primary router rule changed:\n%s", http)
	}

	base.AliasTLS = true
	tls := renderCompose(t, base)
	for _, want := range []string{
		"routers.erp-alias.entrypoints=websecure",
		"routers.erp-alias.tls.certresolver=letsencrypt",
		"routers.erp-alias-socketio.tls.certresolver=letsencrypt",
		"routers.erp-alias-redirect.entrypoints=web",
	} {
		if !strings.Contains(tls, want) {
			t.Errorf("prod alias-tls compose missing %q:\n%s", want, tls)
		}
	}
}

func TestPatchAuthenticateJs(t *testing.T) {
	// The upstream shape of both patch targets, as shipped in frappe v15.
	const upstream = `function authenticate_with_frappe(socket, next) {
	if (
		socket.request.headers.origin &&
		get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin)
	) {
	}
	if (get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin)) {
		next(new Error("Invalid origin"));
	}
}

function get_site_name(socket) {
	if (socket.site_name) {
		return socket.site_name;
	} else if (socket.request.headers["x-frappe-site-name"]) {
		socket.site_name = get_hostname(socket.request.headers["x-frappe-site-name"]);
	} else if (
		conf.default_site &&
		["localhost", "127.0.0.1"].indexOf(get_hostname(socket.request.headers.host)) !== -1
	) {
		socket.site_name = conf.default_site;
	} else if (socket.request.headers.origin) {
		socket.site_name = get_hostname(socket.request.headers.origin);
	} else {
		socket.site_name = get_hostname(socket.request.headers.host);
	}
	return socket.site_name;
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace", "frappe-bench", "apps", "frappe",
		"realtime", "middlewares", "authenticate.js")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(upstream), 0o644); err != nil {
		t.Fatal(err)
	}

	if RealtimeAcceptsAnyHost(dir) {
		t.Error("RealtimeAcceptsAnyHost = true before patching")
	}
	if err := PatchAuthenticateJs(dir); err != nil {
		t.Fatalf("PatchAuthenticateJs: %v", err)
	}
	patched, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(patched)

	if !strings.Contains(got, "} else if (conf.default_site) {") {
		t.Errorf("default_site branch not relaxed:\n%s", got)
	}
	if strings.Contains(got, `["localhost", "127.0.0.1"].indexOf`) {
		t.Errorf("localhost restriction still present:\n%s", got)
	}
	if strings.Contains(got, "\tif (get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin)) {") {
		t.Errorf("origin check not guarded on Origin being present:\n%s", got)
	}
	if !RealtimeAcceptsAnyHost(dir) {
		t.Error("RealtimeAcceptsAnyHost = false after patching")
	}

	// Idempotent: a second run must not change the file again.
	if err := PatchAuthenticateJs(dir); err != nil {
		t.Fatalf("PatchAuthenticateJs (second run): %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != got {
		t.Errorf("PatchAuthenticateJs is not idempotent:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}
