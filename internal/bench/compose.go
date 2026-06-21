package bench

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// PatchAuthenticateJs patches Frappe's realtime authenticate middleware so that
// socket.io connections without an Origin header (same-origin requests where
// browsers omit Origin on GET) are allowed instead of rejected with "Invalid
// origin". The file is at a fixed path under the bind-mounted workspace, so the
// patch survives container restarts. It must be re-applied after bench update.
func PatchAuthenticateJs(benchDir string) error {
	path := filepath.Join(benchDir, "workspace", "frappe-bench", "apps", "frappe",
		"realtime", "middlewares", "authenticate.js")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	original := `if (get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin))`
	patched := `if (socket.request.headers.origin && get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin))`
	if !strings.Contains(string(content), original) {
		return nil // already patched or file changed; leave it alone
	}
	return os.WriteFile(path, []byte(strings.Replace(string(content), original, patched, 1)), 0o644)
}

// PatchUtilsJs patches Frappe's realtime utils so that get_url always uses
// socketio_frappe_url (http://127.0.0.1:8000) for server-to-server auth instead
// of the browser's Origin header. Using origin causes ENOTFOUND / ECONNREFUSED
// inside the container because hostnames like "sitename.localhost" or direct
// host ports (localhost:8040) don't resolve from inside Docker. Must be
// re-applied after bench update (same lifecycle as PatchAuthenticateJs).
func PatchUtilsJs(benchDir string) error {
	path := filepath.Join(benchDir, "workspace", "frappe-bench", "apps", "frappe",
		"realtime", "utils.js")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	original := `return socket.request.headers.origin + path;`
	patched := `return (require("../node_utils").get_conf().socketio_frappe_url || socket.request.headers.origin || "http://localhost:8000") + path;`
	if !strings.Contains(string(content), original) {
		return nil // already patched or file changed; leave it alone
	}
	return os.WriteFile(path, []byte(strings.Replace(string(content), original, patched, 1)), 0o644)
}

// PatchProcfileWorker rewrites the dev Procfile's `worker:` line so the RQ
// background worker auto-restarts instead of taking down the whole honcho stack.
//
// On a dev bench every process (web/werkzeug, socketio, watch, schedule, worker)
// runs under a single honcho supervisor (`bench start`), and honcho SIGTERMs the
// ENTIRE stack the moment any one process exits. The RQ worker (RQ 1.15.1) quits
// with rc=0 whenever its idle Redis dequeue raises redis.exceptions.TimeoutError
// — which happens roughly every worker-TTL window (~7 min) of low background-job
// traffic. That worker exit makes honcho kill the web server too, so the user
// sees a 502 Bad Gateway until `bench start` is re-run.
//
// Wrapping the worker in a self-restarting bash loop means honcho only ever
// watches the wrapper (which never exits), so a worker timeout/crash no longer
// tears down the web server — the worker just relaunches in ~1s. The trap kills
// the current worker child and exits 0 on SIGTERM/SIGINT so `ffm stop`/`restart`
// shut down cleanly. Idempotent (a Procfile already wrapped is left untouched)
// and re-applied on every start, so it survives `bench setup procfile` after a
// bench update. The wrapper also runs the worker with `env -u DEV_SERVER` to
// stop the RQ deprecation-warning flood (see inline note). dev-only — prod runs
// each process in its own container.
func PatchProcfileWorker(benchDir string) error {
	path := filepath.Join(benchDir, "workspace", "frappe-bench", "Procfile")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(content)
	if strings.Contains(text, "while true; do") {
		return nil // already wrapped
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "worker:") {
			continue
		}
		cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "worker:"))
		// Empty, or contains a single quote we can't safely embed in the
		// bash -c '...' wrapper — leave the Procfile untouched.
		if cmd == "" || strings.Contains(cmd, "'") {
			return nil
		}
		// `env -u DEV_SERVER` runs the worker without the dev-web-server flag it
		// inherits from `bench start`. With DEV_SERVER set, Frappe forces
		// `warnings.simplefilter("always", DeprecationWarning)` (frappe/__init__.py),
		// so RQ 1.15.1's `datetime.utcnow()` calls spam worker.error.log on
		// Python 3.12+ (seen as a multi-hundred-MB log). The worker is not the dev
		// web server, so dropping the flag is correct, not a workaround — it just
		// restores Frappe's default ERROR-level logging for this process.
		//
		// `exec` so the bash loop *replaces* the shell honcho spawned and is the
		// process honcho tracks/signals — otherwise SIGTERM on stop can hit a
		// parent shell and leave the loop orphaned. The trap forwards the signal
		// to the current worker child and exits 0 for a clean shutdown.
		lines[i] = "worker: exec bash -c 'trap \"kill \\$child 2>/dev/null; exit 0\" TERM INT; " +
			"while true; do env -u DEV_SERVER " + cmd + " & child=$!; wait $child; sleep 1; done'"
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return nil // no worker: line found
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
