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
	"os/exec"
	"strings"
)

const (
	// NetworkName is the shared Docker bridge network that Traefik and all
	// bench frappe containers attach to.
	NetworkName = "ffm-proxy"

	// ContainerName is both the Docker container name and the Docker Compose
	// project name for the Traefik instance.
	ContainerName = "ffm-proxy"

	// Image is the Traefik Docker image used.
	Image = "traefik:3"

	// WebPort is the host port Traefik binds for HTTP traffic.
	WebPort = 80

	// DashboardPort is the host port for the Traefik read-only dashboard.
	// Bound to 127.0.0.1 only — not exposed publicly.
	DashboardPort = 8080
)

// DashboardURL returns the local URL for the Traefik dashboard.
func DashboardURL() string {
	return fmt.Sprintf("http://localhost:%d/dashboard/", DashboardPort)
}

// IsNetworkPresent reports whether the ffm-proxy Docker network exists.
func IsNetworkPresent() bool {
	out, err := exec.Command(
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
	out, err := exec.Command("docker", "network", "create", NetworkName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create docker network %q: %w\n%s", NetworkName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// containerStatus returns the Docker status of the Traefik container
// ("running", "exited", "created", …) or an empty string if the container
// does not exist.
func containerStatus() string {
	out, err := exec.Command(
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
		out, err := exec.Command("docker", "start", ContainerName).CombinedOutput()
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

	out, err := exec.Command("docker", "stop", ContainerName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop proxy container: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// createContainer runs a brand-new Traefik container attached to ffm-proxy.
// All Traefik configuration is passed as CLI flags — no file is written.
func createContainer() error {
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
		// Mount Docker socket read-only for service discovery.
		"-v", "/var/run/docker.sock:/var/run/docker.sock:ro",
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

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// Provide an actionable hint for the most common failure: port 80 in use.
		if strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind:") {
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
