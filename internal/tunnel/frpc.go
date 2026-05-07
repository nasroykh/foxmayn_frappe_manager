package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const frpcImage = "snowdreamtech/frpc:0.61"

// ContainerName returns the Docker container name for the frpc sidecar of a bench.
func ContainerName(benchName string) string {
	return "ffm-" + benchName + "-frpc"
}

// BenchNetworkName returns the Docker Compose default network for a bench.
// Docker Compose names it <project>_default where project is ffm-<name>.
func BenchNetworkName(benchName string) string {
	return "ffm-" + benchName + "_default"
}

// PublicURL returns the public URL for the bench using its currently-saved subdomain.
func PublicURL(b state.Bench, srv Server) string {
	subdomain := ""
	if b.Tunnel != nil {
		subdomain = b.Tunnel.Subdomain
	}
	return PublicURLFromParts(subdomain, srv)
}

// PublicURLFromParts constructs the public URL from an explicit subdomain and server.
// Use this before the bench state has been updated with the new subdomain.
func PublicURLFromParts(subdomain string, srv Server) string {
	scheme := "http"
	if srv.TLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s.%s", scheme, subdomain, srv.BaseDomain)
}

// RenderFrpcToml returns the frpc TOML configuration for the given bench and server.
// In dev mode, socket.io runs inside the frappe container (honcho); in prod there
// is a dedicated socketio service, so localIP differs.
func RenderFrpcToml(b state.Bench, srv Server, subdomain string) string {
	tlsStr := "false"
	if srv.TLS {
		tlsStr = "true"
	}

	sioLocalIP := "frappe" // dev: socketio in same container
	if b.IsProd() {
		sioLocalIP = "socketio"
	}

	return fmt.Sprintf(`serverAddr   = %q
serverPort   = %d
auth.method  = "token"
auth.token   = %q
transport.tls.enable = %s

[[proxies]]
name      = %q
type      = "http"
localIP   = "frappe"
localPort = 8000
subdomain = %q

[[proxies]]
name      = %q
type      = "http"
localIP   = %q
localPort = 9000
subdomain = %q
locations = ["/socket.io"]
`,
		srv.Host,
		srv.Port,
		srv.Token,
		tlsStr,
		b.Name+"-web",
		subdomain,
		b.Name+"-socketio",
		sioLocalIP,
		subdomain,
	)
}

// WriteFrpcToml renders and writes frpc.toml into benchDir with mode 0o600.
func WriteFrpcToml(benchDir string, b state.Bench, srv Server, subdomain string) error {
	content := RenderFrpcToml(b, srv, subdomain)
	dest := filepath.Join(benchDir, "frpc.toml")
	return os.WriteFile(dest, []byte(content), 0o600)
}

// containerStatus returns the Docker status of the frpc container, or "" if absent.
func containerStatus(benchName string) string {
	out, err := exec.Command(
		"docker", "inspect", ContainerName(benchName), "--format", "{{.State.Status}}",
	).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsRunning reports whether the frpc container for the bench is running or restarting.
func IsRunning(benchName string) bool {
	s := containerStatus(benchName)
	return s == "running" || s == "restarting"
}

// Exists reports whether the frpc container exists in any state.
func Exists(benchName string) bool {
	return containerStatus(benchName) != ""
}

// Status returns a human-readable status for the frpc container.
func Status(benchName string) string {
	switch s := containerStatus(benchName); s {
	case "running":
		return "running"
	case "exited":
		return "stopped"
	case "":
		return "not started"
	default:
		return s
	}
}

// Start starts the frpc container; creates it if it does not yet exist.
// The frpc.toml must already be written to benchDir before calling Start.
func Start(benchDir, benchName string) error {
	switch s := containerStatus(benchName); s {
	case "running", "restarting":
		return nil // already up or coming up — no-op
	case "exited", "created":
		out, err := exec.Command("docker", "start", ContainerName(benchName)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("start frpc container: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "":
		return create(benchDir, benchName)
	default:
		return fmt.Errorf("frpc container in unexpected state %q — run 'docker rm -f %s' and retry", s, ContainerName(benchName))
	}
}

// Stop stops and removes the frpc container. No-op when not present.
// Uses docker rm -f to atomically stop and remove — avoids a race where
// --restart=unless-stopped would bring the container back between stop and rm.
func Stop(benchName string) error {
	if containerStatus(benchName) == "" {
		return nil
	}
	out, err := exec.Command("docker", "rm", "-f", ContainerName(benchName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove frpc container: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Restart stops and recreates the frpc container to pick up a new frpc.toml.
func Restart(benchDir, benchName string) error {
	if err := Stop(benchName); err != nil {
		return err
	}
	return create(benchDir, benchName)
}

func create(benchDir, benchName string) error {
	tomlPath := filepath.Join(benchDir, "frpc.toml")
	args := []string{
		"run", "-d",
		"--name", ContainerName(benchName),
		"--restart=unless-stopped",
		"--network", BenchNetworkName(benchName),
		"-v", tomlPath + ":/etc/frp/frpc.toml:ro",
		frpcImage,
		"-c", "/etc/frp/frpc.toml",
	}
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create frpc container: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
