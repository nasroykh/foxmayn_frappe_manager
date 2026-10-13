// Package proxy manages the shared Traefik reverse-proxy container that
// provides sitename.localhost routing across all ffm benches.
//
// Architecture:
//   - One Docker network "ffm-proxy" is created on first use and never removed
//     automatically (benches may still be attached to it).
//   - One Traefik container named "ffm-proxy" listens on :80 and :8080
//     (dashboard). It watches the Docker socket for label-based service
//     discovery, scoped to the ffm-proxy network.
//   - Each bench's frappe service is attached to ffm-proxy at compose-render
//     time and carries the Traefik labels needed for routing.
//
// The Traefik container is configured entirely via CLI flags — no config file
// is written to disk.
package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

const (
	// NetworkName is the shared Docker bridge network that Traefik and all
	// bench frappe containers attach to.
	NetworkName = "ffm-proxy"

	// ContainerName is both the Docker container name and the Docker Compose
	// project name for the Traefik instance.
	ContainerName = "ffm-proxy"

	// Image is the Traefik image, pinned to a supported minor: only the newest Traefik minor gets
	// security fixes, and a bare "traefik:3" was pulled once and never updated.
	Image = "traefik:v3.7"

	// WebPort is the host port Traefik binds for HTTP traffic.
	WebPort = 80

	// HTTPSPort is the host port Traefik binds for HTTPS traffic (production).
	HTTPSPort = 443

	// DashboardPort is the host port for the Traefik read-only dashboard.
	// Bound to 127.0.0.1 only — not exposed publicly.
	DashboardPort = 8080

	// LetsEncryptVolume is the Docker named volume for persisting ACME certs.
	LetsEncryptVolume = "ffm-letsencrypt"
)

// DashboardURL returns the local URL for the Traefik dashboard.
func DashboardURL() string {
	return fmt.Sprintf("http://localhost:%d/dashboard/", DashboardPort)
}

// IsNetworkPresent reports whether the ffm-proxy Docker network exists.
func IsNetworkPresent() bool {
	out, err := execx.Command(
		"docker", "network", "inspect", NetworkName, "--format", "{{.Name}}",
	).CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == NetworkName
}

// EnsureNetwork creates the ffm-proxy bridge network if it does not yet exist.
// Safe to call multiple times — no-op when the network already exists.
func EnsureNetwork() error {
	if IsNetworkPresent() {
		return nil
	}
	out, err := execx.Command("docker", "network", "create", NetworkName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create docker network %q: %w\n%s", NetworkName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// acmeEmailFlag is the Traefik argument that carries the Let's Encrypt email.
const acmeEmailFlag = "--certificatesresolvers.letsencrypt.acme.email="

// containerInfo is what one `docker inspect` says about the proxy container.
type containerInfo struct {
	Status string // "running", "exited", "created", …; "" when the container does not exist
	Image  string // the image the container was created from
	// Attached reports whether the container has an endpoint on NetworkName.
	Attached bool
	// Published80 reports whether the host side of 80/tcp is bound right now.
	Published80 bool
	// Published443 likewise for 443/tcp.
	Published443 bool
	// HTTPS reports whether the container was created with 443 in its port
	// bindings (a configuration, not a live state).
	HTTPS     bool
	AcmeEmail string
}

// inspectFormat builds one JSON document, which parses more robustly than
// the Go-template text of a map.
const inspectFormat = `{"status":{{json .State.Status}},"image":{{json .Config.Image}},` +
	`"networks":{{json .NetworkSettings.Networks}},"ports":{{json .NetworkSettings.Ports}},` +
	`"bindings":{{json .HostConfig.PortBindings}},"args":{{json .Args}}}`

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// inspectContainer is a seam for tests that need the container to change
// between two calls.
var inspectContainer = realInspect

func realInspect() containerInfo {
	out, err := execx.Command("docker", "inspect", ContainerName, "--format", inspectFormat).Output()
	if err != nil {
		return containerInfo{}
	}
	var raw struct {
		Status   string                   `json:"status"`
		Image    string                   `json:"image"`
		Networks map[string]any           `json:"networks"`
		Ports    map[string][]portBinding `json:"ports"`
		Bindings map[string][]portBinding `json:"bindings"`
		Args     []string                 `json:"args"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &raw) != nil {
		return containerInfo{}
	}
	published := func(port string) bool {
		for _, b := range raw.Ports[port] {
			if b.HostPort != "" {
				return true
			}
		}
		return false
	}
	info := containerInfo{
		Status:       raw.Status,
		Image:        raw.Image,
		Published80:  published("80/tcp"),
		Published443: published("443/tcp"),
	}
	_, info.Attached = raw.Networks[NetworkName]
	_, info.HTTPS = raw.Bindings["443/tcp"]
	for _, a := range raw.Args {
		if v, ok := strings.CutPrefix(a, acmeEmailFlag); ok {
			info.AcmeEmail = v
		}
	}
	return info
}

// problem says why a container is not serving, or "" when it is. A container
// that docker reports as running can hold no network endpoint and no
// published port (seen after an old ffm's container was stopped and started),
// and then nothing answers on *.localhost.
func (i containerInfo) problem() string {
	switch {
	case i.Status != "running":
		return "the container is not running"
	case !i.Attached:
		return "it is not attached to the " + NetworkName + " network"
	case !i.Published80:
		return fmt.Sprintf("port %d is not published", WebPort)
	case i.HTTPS && !i.Published443:
		return fmt.Sprintf("port %d is not published", HTTPSPort)
	}
	return ""
}

// containerStatus returns the Docker status of the Traefik container
// ("running", "exited", "created", …) or an empty string if the container
// does not exist.
func containerStatus() string {
	return inspectContainer().Status
}

// IsRunning reports whether the Traefik proxy container is currently running.
func IsRunning() bool {
	return containerStatus() == "running"
}

// Serving reports whether the proxy container is running and actually serving
// (attached, ports published), from one docker inspect. Callers that decide
// which URL to show, or whether to wire a bench to the proxy, use this; a
// running-but-broken container answers nothing on *.localhost.
func Serving() bool {
	serving, _ := Probe()
	return serving
}

// Probe is Serving plus the reason: broken is non-empty only when the
// container is running but not serving, which is the case worth a warning
// (a missing or stopped proxy is already said by the callers' own hints).
func Probe() (serving bool, broken string) {
	i := inspectContainer()
	if i.Status != "running" {
		return false, ""
	}
	if p := i.problem(); p != "" {
		return false, p
	}
	return true, ""
}

// Healthy reports whether the proxy container is running, attached to the
// ffm-proxy network and publishing its ports. When it is not, reason says why.
func Healthy() (bool, string) {
	i := inspectContainer()
	if p := i.problem(); p != "" {
		return false, p
	}
	return true, ""
}

// Status returns a human-readable proxy status string.
func Status() string {
	i := inspectContainer()
	switch i.Status {
	case "running":
		if p := i.problem(); p != "" {
			// 'ffm proxy start' recreates it on the pinned image as well.
			return fmt.Sprintf("running but broken: %s (run 'ffm proxy start' to recreate it)", p)
		}
		if i.Image != Image {
			return fmt.Sprintf("running (image %s, ffm pins %s: 'ffm proxy stop && ffm proxy start' updates it)", i.Image, Image)
		}
		return "running"
	case "exited":
		return "stopped (run 'ffm proxy start' to resume)"
	case "created":
		return "created but not started (run 'ffm proxy start')"
	case "":
		return "not installed (run 'ffm proxy start')"
	default:
		return i.Status
	}
}

// Start ensures the ffm-proxy network exists and brings the Traefik container
// up. A stopped container is restarted, and a missing one is created. A
// container that is running without its network or ports, one that does not
// serve after a restart, and a stopped one on an older image are removed and
// created again; the Let's Encrypt volume survives that.
func Start() error {
	if err := EnsureNetwork(); err != nil {
		return err
	}

	info := inspectContainer()
	switch s := info.Status; s {
	case "running":
		p := info.problem()
		if p == "" {
			return fmt.Errorf("proxy is already running — dashboard: %s", DashboardURL())
		}
		fmt.Printf("  Proxy container is running but broken: %s. Recreating it...\n", p)
		return recreate(info)

	case "exited", "created":
		if info.Image != Image {
			fmt.Printf("  Updating proxy image %s -> %s...\n", info.Image, Image)
			return recreate(info)
		}
		out, err := execx.Command("docker", "start", ContainerName).CombinedOutput()
		if err != nil {
			return fmt.Errorf("restart proxy container: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		after := inspectContainer()
		if p := after.problem(); p != "" {
			fmt.Printf("  Proxy container restarted but is not serving: %s. Recreating it...\n", p)
			return recreate(info)
		}
		return nil

	case "":
		// Container does not exist; create and start it.
		return createContainer()

	default:
		return fmt.Errorf(
			"proxy container in unexpected state %q — run 'docker rm %s' then 'ffm proxy start'",
			s, ContainerName,
		)
	}
}

// recreate removes the container and creates it again with the settings old
// had: HTTPS with the same ACME email when it published 443.
func recreate(old containerInfo) error {
	if old.HTTPS && old.AcmeEmail == "" {
		return fmt.Errorf("the proxy publishes port %d (HTTPS) but its Let's Encrypt email cannot be read, "+
			"so it cannot be recreated as it was. Run 'docker rm -f %s', then enable HTTPS again by creating "+
			"a prod bench with a domain ('ffm create --mode prod --domain <domain> --acme-email <email>'); "+
			"certificates stay in the %s volume",
			HTTPSPort, ContainerName, LetsEncryptVolume)
	}
	if out, err := execx.Command("docker", "rm", "-f", ContainerName).CombinedOutput(); err != nil {
		return fmt.Errorf("remove proxy container: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	if old.HTTPS {
		return createContainerHTTPS(old.AcmeEmail)
	}
	return createContainer()
}

// Stop halts the Traefik container without removing it or the network.
// Existing benches continue to be reachable on their direct ports.
// The network and labels are preserved so that 'ffm proxy start' re-enables
// domain routing instantly without recreating any bench.
func Stop() error {
	switch s := containerStatus(); s {
	case "":
		return fmt.Errorf("proxy container not found — nothing to stop")
	case "exited", "created":
		return fmt.Errorf("proxy is already stopped (status: %s)", s)
	case "running":
		// fall through
	default:
		return fmt.Errorf("proxy container in unexpected state %q", s)
	}

	out, err := execx.Command("docker", "stop", ContainerName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop proxy container: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SupportsHTTPS reports whether the running Traefik container has port 443
// bound, indicating it was started with Let's Encrypt support.
func SupportsHTTPS() bool {
	return inspectContainer().HTTPS
}

// EnsureHTTPS ensures the proxy is running with HTTPS (port 443) and Let's
// Encrypt configured. If the proxy is not running it is created with HTTPS
// support. If it is running without HTTPS it is stopped, removed, and
// recreated with HTTPS. If it already supports HTTPS this is a no-op.
//
// Note: recreating the proxy causes a brief routing interruption for all
// benches during the container restart (~1 second).
func EnsureHTTPS(acmeEmail string) error {
	if err := EnsureNetwork(); err != nil {
		return err
	}

	switch s := containerStatus(); s {
	case "running":
		if SupportsHTTPS() {
			return nil // already supports HTTPS — no-op
		}
		// Running but HTTP-only: stop, remove, recreate with HTTPS.
		fmt.Println("  Upgrading proxy to HTTPS (brief routing interruption)...")
		if out, err := execx.Command("docker", "stop", ContainerName).CombinedOutput(); err != nil {
			return fmt.Errorf("stop proxy for HTTPS upgrade: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		if out, err := execx.Command("docker", "rm", ContainerName).CombinedOutput(); err != nil {
			return fmt.Errorf("remove proxy for HTTPS upgrade: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		return createContainerHTTPS(acmeEmail)

	case "exited", "created":
		// Stopped container exists: remove and recreate with HTTPS.
		if out, err := execx.Command("docker", "rm", ContainerName).CombinedOutput(); err != nil {
			return fmt.Errorf("remove stopped proxy: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		return createContainerHTTPS(acmeEmail)

	case "":
		// No container: create fresh with HTTPS.
		return createContainerHTTPS(acmeEmail)

	default:
		return fmt.Errorf("proxy container in unexpected state %q — run 'docker rm %s' and retry", s, ContainerName)
	}
}

// createContainer runs a brand-new Traefik container attached to ffm-proxy.
// All Traefik configuration is passed as CLI flags — no file is written.
func createContainer() error {
	if err := checkUnprivilegedPorts(WebPort); err != nil {
		return err
	}
	sock, err := dockerSocket()
	if err != nil {
		return err
	}
	args := []string{
		"run", "-d",
		"--name", ContainerName,
		// Restart on Docker daemon restart, but respect explicit `docker stop`.
		"--restart=unless-stopped",
		// HTTP entry point — must be on port 80 for .localhost domains to work
		// without a port in the browser URL.
		"-p", fmt.Sprintf("0.0.0.0:%d:80", WebPort),
		// Dashboard on localhost only — not meant to be publicly exposed.
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", DashboardPort, DashboardPort),
		// Attach to the shared network so Traefik can reach bench containers.
		"--network", NetworkName,
		// Mount Docker socket read-only for service discovery (the rootless
		// daemon's own socket when there is one).
		"-v", sock + ":" + defaultSocket + ":ro",
		// Prevent Traefik from routing to itself.
		"--label", "traefik.enable=false",
		Image,
		// ── Traefik static configuration (via CLI flags) ──────────────────
		"--api.dashboard=true",
		"--api.insecure=true", // dashboard on :8080, no TLS needed for local dev
		// Docker provider: only route containers that have traefik.enable=true.
		"--providers.docker=true",
		"--providers.docker.exposedByDefault=false",
		// Scope discovery to the shared network to avoid the multi-network
		// ambiguity bug where Traefik randomly picks the wrong network.
		"--providers.docker.network=" + NetworkName,
		// Single HTTP entrypoint on port 80.
		"--entrypoints.web.address=:80",
		"--log.level=INFO",
	}

	out, err := execx.Command("docker", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if portHint(msg) == "privileged" {
			return fmt.Errorf("start Traefik: Docker may not publish port %d (a rootless daemon needs "+
				"net.ipv4.ip_unprivileged_port_start <= %d: 'sudo sysctl -w net.ipv4.ip_unprivileged_port_start=%d').\n"+
				"Original error: %w\n%s", WebPort, WebPort, WebPort, err, msg)
		}
		// Provide an actionable hint for the most common failure: port 80 in use.
		if portHint(msg) == "in use" {
			return fmt.Errorf(
				"start Traefik: port %d is already in use on this host.\n"+
					"Stop the process occupying port %d and try again.\n"+
					"Original error: %w\n%s",
				WebPort, WebPort, err, msg,
			)
		}
		return fmt.Errorf("start Traefik: %w\n%s", err, msg)
	}
	return nil
}

// createContainerHTTPS runs a Traefik container with HTTPS support and
// Let's Encrypt ACME HTTP-01 challenge. Port 443 is bound in addition to 80.
// Certs are persisted in the ffm-letsencrypt named Docker volume.
//
// Dev benches route on the "web" entrypoint (:80) and are unaffected by the
// addition of the "websecure" entrypoint. Per-bench HTTP→HTTPS redirects are
// handled via compose labels so no global redirect is configured here (which
// would break dev benches).
func createContainerHTTPS(acmeEmail string) error {
	if err := checkUnprivilegedPorts(WebPort, HTTPSPort); err != nil {
		return err
	}
	sock, err := dockerSocket()
	if err != nil {
		return err
	}
	args := []string{
		"run", "-d",
		"--name", ContainerName,
		"--restart=unless-stopped",
		"-p", fmt.Sprintf("0.0.0.0:%d:80", WebPort),
		"-p", fmt.Sprintf("0.0.0.0:%d:443", HTTPSPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", DashboardPort, DashboardPort),
		"--network", NetworkName,
		"-v", sock + ":" + defaultSocket + ":ro",
		"-v", fmt.Sprintf("%s:/letsencrypt", LetsEncryptVolume),
		"--label", "traefik.enable=false",
		Image,
		"--api.dashboard=true",
		"--api.insecure=true",
		"--providers.docker=true",
		"--providers.docker.exposedByDefault=false",
		"--providers.docker.network=" + NetworkName,
		"--entrypoints.web.address=:80",
		"--entrypoints.websecure.address=:443",
		"--certificatesresolvers.letsencrypt.acme.httpchallenge=true",
		"--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web",
		"--certificatesresolvers.letsencrypt.acme.email=" + acmeEmail,
		"--certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json",
		"--log.level=INFO",
	}

	out, err := execx.Command("docker", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if portHint(msg) == "privileged" {
			return fmt.Errorf("start Traefik (HTTPS): Docker may not publish port %d or %d (a rootless daemon needs "+
				"net.ipv4.ip_unprivileged_port_start <= %d: 'sudo sysctl -w net.ipv4.ip_unprivileged_port_start=%d').\n"+
				"Original error: %w\n%s", WebPort, HTTPSPort, WebPort, WebPort, err, msg)
		}
		if portHint(msg) == "in use" {
			return fmt.Errorf(
				"start Traefik (HTTPS): port 80 or 443 is already in use on this host.\n"+
					"Stop the conflicting process and try again.\n"+
					"Original error: %w\n%s",
				err, msg,
			)
		}
		return fmt.Errorf("start Traefik (HTTPS): %w\n%s", err, msg)
	}
	return nil
}
