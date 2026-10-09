package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// Config is the proxy's configuration. Traefik takes it as command-line
// flags, so ffm keeps it in proxy.json to recreate the container identically
// on an upgrade or a change.
type Config struct {
	// HTTPS adds the :443 entrypoint and the Let's Encrypt resolver.
	HTTPS     bool   `json:"https,omitempty"`
	ACMEEmail string `json:"acme_email,omitempty"`
	// ACMEStaging uses Let's Encrypt's staging CA (untrusted certificates, far
	// higher rate limits) and its own storage file, for trying a setup out.
	ACMEStaging bool `json:"acme_staging,omitempty"`
	// DNSProvider "cloudflare" solves ACME with DNS-01 instead of HTTP-01:
	// certificates for hosts whose port 80 is closed or behind a proxy.
	// DNSTokenFile holds a Cloudflare API token (Zone:DNS:Edit), mounted
	// read-only; it is never passed as an argument.
	DNSProvider  string `json:"dns_provider,omitempty"`
	DNSTokenFile string `json:"dns_token_file,omitempty"`
	// Cloudflare trusts X-Forwarded-For from Cloudflare's edge ranges, so
	// Frappe sees visitors' addresses instead of Cloudflare's.
	Cloudflare bool `json:"cloudflare,omitempty"`
}

// LoadConfig reads proxy.json. Without one it is derived from the running
// container, which is what proxies made before v0.13.0 have.
func LoadConfig() (Config, error) {
	raw, err := os.ReadFile(config.ProxyConfigFile())
	if os.IsNotExist(err) {
		c := Config{HTTPS: SupportsHTTPS()}
		if b, err := os.ReadFile(config.AcmeEmailFile()); err == nil {
			c.ACMEEmail = strings.TrimSpace(string(b))
		}
		return c, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", config.ProxyConfigFile(), err)
	}
	return c, nil
}

// SaveConfig writes proxy.json (0600).
func SaveConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(config.ProxyConfigFile()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(config.ProxyConfigFile(), append(raw, '\n'), 0o600)
}

// Validate checks a configuration can produce a working proxy.
func (c Config) Validate() error {
	if c.DNSProvider != "" && c.DNSProvider != "cloudflare" {
		return fmt.Errorf("DNS provider %q: only cloudflare is supported", c.DNSProvider)
	}
	if c.DNSProvider != "" {
		if !filepath.IsAbs(c.DNSTokenFile) {
			return fmt.Errorf("the Cloudflare token file must be an absolute path")
		}
		if _, err := os.Stat(c.DNSTokenFile); err != nil {
			return fmt.Errorf("the Cloudflare token file: %w", err)
		}
	}
	if c.HTTPS && c.ACMEEmail == "" {
		return fmt.Errorf("HTTPS needs an ACME email")
	}
	return nil
}

// cloudflareRanges are Cloudflare's edge networks, from
// https://www.cloudflare.com/ips/ (checked 2026-10-08). They change rarely;
// a new ffm release carries a new list.
var cloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
	"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
	"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
	"2a06:98c0::/29", "2c0f:f248::/32",
}

// cfTokenPath is where the Cloudflare token file appears in the container.
const cfTokenPath = "/run/ffm/cloudflare-token"

// runArgs are the `docker run` arguments of the proxy for a configuration.
func runArgs(c Config) []string {
	args := []string{
		"run", "-d",
		"--name", ContainerName,
		// Restart on Docker daemon restart, but respect an explicit stop.
		"--restart=unless-stopped",
		// Traefik's own log and the access log go to json-file; bound it.
		"--log-opt", "max-size=20m", "--log-opt", "max-file=5",
		// HTTP on :80, so .localhost names work without a port.
		"-p", fmt.Sprintf("0.0.0.0:%d:80", WebPort),
	}
	if c.HTTPS {
		args = append(args, "-p", fmt.Sprintf("0.0.0.0:%d:443", HTTPSPort))
	}
	args = append(args,
		// The dashboard stays on loopback.
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", DashboardPort, DashboardPort),
		"--network", NetworkName,
		"-v", "/var/run/docker.sock:/var/run/docker.sock:ro",
	)
	if c.HTTPS {
		args = append(args, "-v", LetsEncryptVolume+":/letsencrypt")
		if c.DNSProvider == "cloudflare" {
			args = append(args, "-v", c.DNSTokenFile+":"+cfTokenPath+":ro", "-e", "CF_DNS_API_TOKEN_FILE="+cfTokenPath)
		}
	}
	args = append(args,
		"--label", "traefik.enable=false",
		Image,
		"--api.dashboard=true",
		"--api.insecure=true",
		"--providers.docker=true",
		"--providers.docker.exposedByDefault=false",
		// Scope discovery to the shared network: with several networks Traefik
		// otherwise picks one at random.
		"--providers.docker.network="+NetworkName,
		"--log.level=INFO",
		"--accesslog=true",
	)
	entrypoints := []string{"web"}
	args = append(args, "--entrypoints.web.address=:80")
	if c.HTTPS {
		entrypoints = append(entrypoints, "websecure")
		args = append(args, "--entrypoints.websecure.address=:443")
	}
	for _, ep := range entrypoints {
		// The 60 s default cuts large uploads (attachments, data imports).
		args = append(args, "--entrypoints."+ep+".transport.respondingTimeouts.readTimeout=600s")
		if c.Cloudflare {
			args = append(args, "--entrypoints."+ep+".forwardedHeaders.trustedIPs="+strings.Join(cloudflareRanges, ","))
		}
	}
	if c.HTTPS {
		r := "--certificatesresolvers.letsencrypt.acme."
		storage := "/letsencrypt/acme.json"
		if c.ACMEStaging {
			storage = "/letsencrypt/acme-staging.json"
			args = append(args, r+"caserver=https://acme-staging-v02.api.letsencrypt.org/directory")
		}
		args = append(args, r+"email="+c.ACMEEmail, r+"storage="+storage)
		if c.DNSProvider == "cloudflare" {
			args = append(args, r+"dnschallenge=true", r+"dnschallenge.provider=cloudflare")
		} else {
			args = append(args, r+"httpchallenge=true", r+"httpchallenge.entrypoint=web")
		}
	}
	return args
}
