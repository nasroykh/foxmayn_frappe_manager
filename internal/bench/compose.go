package bench

import (
	_ "embed"
	"encoding/json"
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

// WriteDevcontainer writes .devcontainer/devcontainer.json into the bench
// directory so that VS Code can open the full frappe-bench inside the container
// ("Dev Containers: Reopen in Container" or "Attach to Running Container").
func WriteDevcontainer(benchDir string, data ComposeData) error {
	dir := filepath.Join(benchDir, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	type devcontainer struct {
		Name              string `json:"name"`
		DockerComposeFile string `json:"dockerComposeFile"`
		Service           string `json:"service"`
		WorkspaceFolder   string `json:"workspaceFolder"`
		RemoteUser        string `json:"remoteUser"`
		ShutdownAction    string `json:"shutdownAction"`
	}

	cfg := devcontainer{
		Name:              data.Name,
		DockerComposeFile: "../docker-compose.yml",
		Service:           "frappe",
		WorkspaceFolder:   "/workspace/frappe-bench",
		RemoteUser:        "frappe",
		ShutdownAction:    "none",
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	dest := filepath.Join(dir, "devcontainer.json")
	return os.WriteFile(dest, append(b, '\n'), 0o644)
}
