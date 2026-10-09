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
	"fmt"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"strings"
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
	// Pinned to a patch, so a host runs what this ffm was tested with; 'ffm proxy
	// upgrade' moves an existing proxy to it.
	Image = "traefik:v3.7.14"

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

// containerStatus returns the Docker status of the Traefik container
// ("running", "exited", "created", …) or an empty string if the container
// does not exist.
func containerStatus() string {
	out, err := execx.Command(
		"docker", "inspect", ContainerName, "--format", "{{.State.Status}}",
	).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsRunning reports whether the Traefik proxy container is currently running.
func IsRunning() bool {
	return containerStatus() == "running"
}

// Status returns a human-readable proxy status string.
func Status() string {
	s := containerStatus()
	switch s {
	case "running":
		return "running"
	case "exited":
		return "stopped (run 'ffm proxy start' to resume)"
	case "created":
		return "created but not started (run 'ffm proxy start')"
	case "":
		return "not installed (run 'ffm proxy start')"
	default:
		return s
	}
}

// Start ensures the ffm-proxy network exists and brings the Traefik container
// up. If the container already exists in a stopped state it is restarted; if
// it does not exist it is created fresh.
func Start() error {
	if err := EnsureNetwork(); err != nil {
		return err
	}

	switch s := containerStatus(); s {
	case "running":
		return fmt.Errorf("proxy is already running — dashboard: %s", DashboardURL())

	case "exited", "created":
		// Container exists but is stopped; restart it.
		out, err := execx.Command("docker", "start", ContainerName).CombinedOutput()
		if err != nil {
			return fmt.Errorf("restart proxy container: %w\n%s", err, strings.TrimSpace(string(out)))
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
	out, err := execx.Command(
		"docker", "inspect", ContainerName,
		"--format", `{{range $p, $conf := .HostConfig.PortBindings}}{{$p}} {{end}}`,
	).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "443")
}

// EnsureHTTPS makes sure the proxy serves HTTPS with Let's Encrypt, keeping
// the rest of its configuration. A proxy without HTTPS is recreated (a brief
// routing interruption for every bench).
func EnsureHTTPS(acmeEmail string) error {
	if err := EnsureNetwork(); err != nil {
		return err
	}
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	if containerStatus() == "running" && SupportsHTTPS() && c.HTTPS {
		return nil
	}
	c.HTTPS = true
	if acmeEmail != "" {
		c.ACMEEmail = acmeEmail
	}
	if err := SaveConfig(c); err != nil {
		return err
	}
	if containerStatus() == "running" {
		fmt.Println("  Upgrading proxy to HTTPS (brief routing interruption)...")
	}
	return Recreate(c)
}

// Recreate removes the proxy container, if any, and starts it again with c.
// Certificates live in a volume and survive.
func Recreate(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := EnsureNetwork(); err != nil {
		return err
	}
	if containerStatus() != "" {
		if out, err := execx.Command("docker", "rm", "-f", ContainerName).CombinedOutput(); err != nil {
			return fmt.Errorf("remove the proxy container: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	}
	return run(c)
}

// Upgrade pulls the pinned Traefik image and recreates the proxy with its
// current configuration. It reports the image it ran before.
func Upgrade() (string, error) {
	before := RunningImage()
	c, err := LoadConfig()
	if err != nil {
		return before, err
	}
	if out, err := execx.Command("docker", "pull", Image).CombinedOutput(); err != nil {
		return before, fmt.Errorf("pull %s: %w\n%s", Image, err, strings.TrimSpace(string(out)))
	}
	if err := SaveConfig(c); err != nil {
		return before, err
	}
	return before, Recreate(c)
}

// createContainer starts a new proxy with the saved configuration.
func createContainer() error {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	return run(c)
}

func run(c Config) error {
	out, err := execx.Command("docker", runArgs(c)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind:") {
			return fmt.Errorf("start Traefik: port 80 or 443 is already in use on this host; stop that process and retry.\n%w\n%s", err, msg)
		}
		return fmt.Errorf("start Traefik: %w\n%s", err, msg)
	}
	return nil
}

// RunningImage is the image of the proxy container, or "".
func RunningImage() string {
	out, err := execx.Command("docker", "inspect", ContainerName, "--format", "{{.Config.Image}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
