package bench

import (
	_ "embed"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates/docker-compose.yml.tmpl
var composeTmpl string

//go:embed templates/Dockerfile.tmpl
var dockerfileTmpl string

// ComposeData holds the values substituted into the compose and Dockerfile templates.
type ComposeData struct {
	// Name is the bench name, used as the Traefik router/service identifier.
	Name                string
	BenchDir            string
	WebPort             int
	WebPortEnd          int
	SocketIOPort        int
	SocketIOPortEnd     int
	MariaDBRootPassword string
	// FrappeBranch is the git branch passed as a Docker build arg so that
	// bench init runs inside the image build (cached across benches).
	FrappeBranch string
	// ForwardSSHAgent, when true, mounts the host SSH agent socket into the
	// frappe container so that SSH-URL private repos work during bench get-app.
	ForwardSSHAgent bool
}

// WriteCompose renders the compose template into the bench directory.
func WriteCompose(benchDir string, data ComposeData) error {
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return err
	}

	tmpl, err := template.New("compose").Parse(composeTmpl)
	if err != nil {
		return err
	}

	dest := filepath.Join(benchDir, "docker-compose.yml")
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}

// WriteDockerfile renders the Dockerfile template into the bench directory.
func WriteDockerfile(benchDir string, data ComposeData) error {
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return err
	}

	tmpl, err := template.New("dockerfile").Parse(dockerfileTmpl)
	if err != nil {
		return err
	}

	dest := filepath.Join(benchDir, "Dockerfile")
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}
