package proxy

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// defaultSocket is the rootful daemon's socket, and the path Traefik expects
// inside its container whatever is mounted there.
const defaultSocket = "/var/run/docker.sock"

// unprivilegedPortFile holds the lowest port an unprivileged process may bind.
const unprivilegedPortFile = "/proc/sys/net/ipv4/ip_unprivileged_port_start"

// Seams for tests.
var (
	getenv   = os.Getenv
	readFile = os.ReadFile
)

// rootless reports whether the docker daemon in use runs rootless. Only then
// is the socket anywhere but /var/run/docker.sock; Docker Desktop's context
// socket is a proxy that cannot be bind-mounted into a container, so a
// context alone never changes the mount.
func rootless() bool {
	out, err := execx.Command("docker", "info", "--format", "{{json .SecurityOptions}}").Output()
	return err == nil && strings.Contains(string(out), "rootless")
}

// dockerSocket returns the host path to mount at /var/run/docker.sock: the
// daemon's own socket for a rootless daemon, /var/run/docker.sock otherwise.
func dockerSocket() (string, error) {
	if !rootless() {
		return defaultSocket, nil
	}
	endpoint := getenv("DOCKER_HOST")
	source := "DOCKER_HOST"
	if endpoint == "" {
		out, err := execx.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err == nil {
			endpoint = strings.TrimSpace(string(out))
		}
		source = "the current docker context"
	}
	switch {
	case endpoint == "":
		return defaultSocket, nil
	case strings.HasPrefix(endpoint, "unix://"):
		return strings.TrimPrefix(endpoint, "unix://"), nil
	default:
		return "", fmt.Errorf("%s points at %q, but the proxy needs a local unix socket to mount for service discovery; "+
			"point DOCKER_HOST (or the docker context) at the rootless daemon's unix:// socket", source, endpoint)
	}
}

// checkUnprivilegedPorts fails before `docker run` when a rootless daemon
// cannot publish one of ports: the container would be created and then die
// with a bind error that names nothing useful.
func checkUnprivilegedPorts(ports ...int) error {
	if !rootless() {
		return nil
	}
	raw, err := readFile(unprivilegedPortFile)
	if err != nil {
		return nil
	}
	start, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil
	}
	for _, p := range ports {
		if p < start {
			return fmt.Errorf("rootless Docker cannot publish port %d: net.ipv4.ip_unprivileged_port_start is %d.\n"+
				"Lower it (as root) and try again:\n"+
				"  sudo sysctl -w net.ipv4.ip_unprivileged_port_start=%d\n"+
				"  echo 'net.ipv4.ip_unprivileged_port_start=%d' | sudo tee /etc/sysctl.d/99-ffm-rootless.conf",
				p, start, p, p)
		}
	}
	return nil
}

// portHint recognises the ways a failed `docker run` reports a port problem.
func portHint(msg string) string {
	switch {
	// "permission denied" alone also covers a socket ffm may not open, so it
	// counts only next to a bind.
	case strings.Contains(msg, "privileged port") ||
		(strings.Contains(msg, "permission denied") && strings.Contains(msg, "bind")):
		return "privileged"
	case strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind:"):
		return "in use"
	}
	return ""
}
