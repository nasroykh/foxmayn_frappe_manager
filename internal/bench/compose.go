package bench

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates/dev/docker-compose.yml.tmpl
var devComposeTmpl string

//go:embed templates/dev/Dockerfile.tmpl
var devDockerfileTmpl string

//go:embed templates/prod/docker-compose.yml.tmpl
var prodComposeTmpl string

//go:embed templates/prod/Dockerfile.tmpl
var prodDockerfileTmpl string

// ComposeData holds the values substituted into the compose and Dockerfile templates.
type ComposeData struct {
	// Name is the bench name, used as the Traefik router/service identifier.
	Name string
	// Mode is "dev" or "prod". Selects which template pair to render.
	Mode            string
	BenchDir        string
	WebPort         int
	WebPortEnd      int // dev only: upper end of port range
	SocketIOPort    int
	SocketIOPortEnd int // dev only: upper end of port range
	// DBType is "mariadb" or "postgres". Controls which database service is rendered.
	DBType         string
	DBRootPassword string
	// ForwardSSHAgent, when true, mounts the host SSH agent socket into the
	// frappe container so that SSH-URL private repos work during bench get-app.
	// Dev mode only.
	ForwardSSHAgent bool
	// Domain is the public domain for production benches (e.g. "erp.example.com").
	// Prod mode only.
	Domain string
	// SiteName is the Frappe site name (e.g. "erp.example.com" for prod, "<name>.localhost" for dev).
	// Prod mode only — written into wsgi.py to force single-site routing.
	SiteName string
	// NoSSL, when true, routes on HTTP entrypoint instead of websecure.
	// Prod mode only.
	NoSSL bool
	// MariaDBBufferPool sets the InnoDB buffer pool size for the MariaDB
	// service (e.g. "1G", "2G"). Prod mode only; dev uses a hardcoded 256M.
	// Empty is treated as "1G".
	MariaDBBufferPool string
	// GunicornWorkers is the number of gunicorn worker processes.
	// Prod mode only. Zero is treated as 2.
	GunicornWorkers int
	// RedisCacheMaxmem is the maxmemory limit for redis-cache (e.g. "512mb").
	// Prod mode only. Empty is treated as "512mb".
	RedisCacheMaxmem string
	// RedisQueueMaxmem is the maxmemory limit for redis-queue.
	// Uses noeviction so jobs are never silently dropped.
	// Prod mode only. Empty is treated as "512mb".
	RedisQueueMaxmem string
	// WorkerLongCount is the replica count for the long-queue worker.
	// Prod mode only. Zero is treated as 1.
	WorkerLongCount int
	// WorkerShortCount is the replica count for the short-queue worker.
	// Prod mode only. Zero is treated as 1.
	WorkerShortCount int
	// SlowQueryLog enables MariaDB slow query logging (MariaDB + prod only).
	// runCreate must create <benchDir>/mysql-logs/ when this is true.
	SlowQueryLog bool
}

// WriteCompose renders the compose template into the bench directory.
// Selects the dev or prod template based on data.Mode.
func WriteCompose(benchDir string, data ComposeData) error {
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return err
	}

	tmplStr := devComposeTmpl
	if data.Mode == "prod" {
		tmplStr = prodComposeTmpl
	}

	tmpl, err := template.New("compose").Parse(tmplStr)
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
// Selects the dev or prod template based on data.Mode.
func WriteDockerfile(benchDir string, data ComposeData) error {
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return err
	}

	tmplStr := devDockerfileTmpl
	if data.Mode == "prod" {
		tmplStr = prodDockerfileTmpl
	}

	tmpl, err := template.New("dockerfile").Parse(tmplStr)
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

// WriteWsgiWrapper writes wsgi.py into the bench workspace at
// <benchDir>/workspace/frappe-bench/sites/wsgi.py. It lives under the existing
// ./workspace:/workspace bind mount so no extra volume entry is needed, and is
// written after bench init (so the sites/ directory already exists). Gunicorn
// runs with --chdir sites, making this file discoverable as "wsgi" module. It
// forces _site so any Host header (including bare "localhost") resolves correctly.
func WriteWsgiWrapper(benchDir, siteName string) error {
	content := fmt.Sprintf("import frappe.app as _a\n_a._site = %q\napplication = _a.application_with_statics()\n", siteName)
	dest := filepath.Join(benchDir, "workspace", "frappe-bench", "sites", "wsgi.py")
	return os.WriteFile(dest, []byte(content), 0o644)
}

// WriteDevcontainer writes .devcontainer/devcontainer.json into the bench
// directory so that VS Code can open the full frappe-bench inside the container
// ("Dev Containers: Reopen in Container" or "Attach to Running Container").
// Only applicable for dev mode benches.
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
