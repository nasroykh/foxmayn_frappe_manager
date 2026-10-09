package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestRunArgs(t *testing.T) {
	plain := runArgs(Config{})
	for _, want := range []string{"--entrypoints.web.address=:80", "--accesslog=true", "max-size=20m",
		"--entrypoints.web.transport.respondingTimeouts.readTimeout=600s", Image} {
		if !has(plain, want) {
			t.Errorf("plain proxy lacks %q", want)
		}
	}
	if has(plain, "--entrypoints.websecure.address=:443") || strings.Contains(strings.Join(plain, " "), "certificatesresolvers") {
		t.Error("plain proxy has HTTPS")
	}

	tls := runArgs(Config{HTTPS: true, ACMEEmail: "a@b.c"})
	for _, want := range []string{"--entrypoints.websecure.address=:443", "--certificatesresolvers.letsencrypt.acme.httpchallenge=true",
		"--certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json", "--certificatesresolvers.letsencrypt.acme.email=a@b.c"} {
		if !has(tls, want) {
			t.Errorf("HTTPS proxy lacks %q", want)
		}
	}

	staging := strings.Join(runArgs(Config{HTTPS: true, ACMEEmail: "a@b.c", ACMEStaging: true}), " ")
	if !strings.Contains(staging, "acme-staging-v02") || !strings.Contains(staging, "acme-staging.json") {
		t.Error("staging must use the staging CA and its own storage")
	}

	dir := t.TempDir()
	tok := filepath.Join(dir, "cf")
	os.WriteFile(tok, []byte("secret-token"), 0o600)
	dns := runArgs(Config{HTTPS: true, ACMEEmail: "a@b.c", DNSProvider: "cloudflare", DNSTokenFile: tok, Cloudflare: true})
	joined := strings.Join(dns, " ")
	if !has(dns, "--certificatesresolvers.letsencrypt.acme.dnschallenge.provider=cloudflare") || has(dns, "--certificatesresolvers.letsencrypt.acme.httpchallenge=true") {
		t.Error("DNS-01 not configured")
	}
	if !strings.Contains(joined, tok+":"+cfTokenPath+":ro") || !strings.Contains(joined, "CF_DNS_API_TOKEN_FILE="+cfTokenPath) {
		t.Error("token file not mounted read-only")
	}
	if strings.Contains(joined, "secret-token") {
		t.Error("the token reached the arguments")
	}
	if !strings.Contains(joined, "--entrypoints.websecure.forwardedHeaders.trustedIPs=173.245.48.0/20,") {
		t.Error("Cloudflare ranges not trusted on websecure")
	}
}

func TestConfigValidate(t *testing.T) {
	for _, bad := range []Config{
		{HTTPS: true},
		{DNSProvider: "route53"},
		{HTTPS: true, ACMEEmail: "a@b.c", DNSProvider: "cloudflare", DNSTokenFile: "relative"},
		{HTTPS: true, ACMEEmail: "a@b.c", DNSProvider: "cloudflare", DNSTokenFile: "/nonexistent/ffm-token"},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
}
