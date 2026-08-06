package bench

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// DomainAliases are extra hostnames Traefik should route to this bench, on
	// top of the mode's primary host (<name>.localhost for dev, Domain for prod).
	// Typically LAN names such as "erp.internal" that a local DNS server points
	// at this host. Every entry must have passed NormalizeDomain — the values are
	// interpolated into Traefik labels inside backticks, so an unvalidated string
	// could inject arbitrary router configuration into the generated compose file.
	DomainAliases []string
	// AliasTLS routes the aliases through the websecure entrypoint with the
	// Let's Encrypt resolver instead of plain HTTP. Prod + SSL only, and only
	// safe when every alias is publicly resolvable — Traefik puts them in the
	// same certificate request, so one unreachable name fails the whole order.
	AliasTLS bool
	// HostUID and HostGID, when both non-zero, make the Dockerfile remap the
	// in-container `frappe` user (uid/gid 1000 in the frappe/bench base image)
	// onto these ids. Zero — the default — leaves the base image untouched and
	// renders exactly the Dockerfile ffm has always produced.
	//
	// Set from --match-host-user for hosts whose uid is not 1000, where the
	// ./workspace bind mount is otherwise unwritable from one side or the other.
	// HostUID alone gates rendering, so callers must set both or neither —
	// manager.hostUserIDs enforces that.
	HostUID int
	HostGID int
}

// domainLabelRe matches a single DNS label: alphanumeric, inner hyphens only.
var domainLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// NormalizeDomain lower-cases and validates a hostname for use in a Traefik
// Host() matcher. Any scheme prefix, trailing dot, or :port suffix is an error
// rather than silently stripped, so a typo never becomes a router that quietly
// matches the wrong name.
//
// Validation is deliberately strict: the result is written into a compose label
// between backticks, so accepting a backtick, quote, or comma would let a
// domain string inject arbitrary Traefik configuration.
func NormalizeDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" {
		return "", fmt.Errorf("domain is empty")
	}
	if strings.Contains(d, "://") {
		return "", fmt.Errorf("invalid domain %q: drop the scheme (use erp.internal, not http://erp.internal)", raw)
	}
	if strings.Contains(d, "/") {
		return "", fmt.Errorf("invalid domain %q: a path is not allowed", raw)
	}
	if strings.Contains(d, ":") {
		return "", fmt.Errorf("invalid domain %q: a port is not allowed — Traefik matches the hostname only", raw)
	}
	if len(d) > 253 {
		return "", fmt.Errorf("invalid domain %q: longer than 253 characters", raw)
	}
	// Report labels from the caller's own spelling — echoing the lower-cased form
	// back reads like a second, unrelated error. Lower-casing preserves the label
	// count, so the indexes line up.
	original := strings.Split(strings.TrimSpace(raw), ".")
	for i, label := range strings.Split(d, ".") {
		shown := label
		if i < len(original) {
			shown = original[i]
		}
		if len(label) > 63 {
			return "", fmt.Errorf("invalid domain %q: label %q is longer than 63 characters", raw, shown)
		}
		if !domainLabelRe.MatchString(label) {
			return "", fmt.Errorf("invalid domain %q: %q is not a valid hostname label "+
				"(letters, digits and inner hyphens only)", raw, shown)
		}
	}
	return d, nil
}

// PrimaryHost returns the hostname the bench's main Traefik router matches.
func (d ComposeData) PrimaryHost() string {
	if d.Mode == "prod" {
		return d.Domain
	}
	return d.Name + ".localhost"
}

// hostMatcher renders a Traefik v3 rule matching any of the given hostnames.
func hostMatcher(hosts []string) string {
	parts := make([]string, 0, len(hosts))
	for _, h := range hosts {
		parts = append(parts, fmt.Sprintf("Host(`%s`)", h))
	}
	return strings.Join(parts, " || ")
}

// RouterRule is the dev router rule: the primary host plus any aliases. Dev has
// a single HTTP router, so aliases are folded straight into it.
func (d ComposeData) RouterRule() string {
	return hostMatcher(append([]string{d.PrimaryHost()}, d.DomainAliases...))
}

// AliasRule matches the alias hostnames only. Prod keeps aliases on separate
// routers from the primary domain so that a LAN-only alias can never end up in
// the primary domain's ACME certificate request.
func (d ComposeData) AliasRule() string { return hostMatcher(d.DomainAliases) }

// HasAliases reports whether any alias hostname is configured.
func (d ComposeData) HasAliases() bool { return len(d.DomainAliases) > 0 }

// AliasEntrypoint is the Traefik entrypoint the prod alias routers bind to.
func (d ComposeData) AliasEntrypoint() string {
	if d.AliasTLS {
		return "websecure"
	}
	return "web"
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

// authenticateJsPath is Frappe's realtime auth middleware inside the workspace
// bind mount, so edits survive container restarts.
func authenticateJsPath(benchDir string) string {
	return filepath.Join(benchDir, "workspace", "frappe-bench", "apps", "frappe",
		"realtime", "middlewares", "authenticate.js")
}

// siteNamePatched is the shape get_site_name() has after the default-site
// relaxation below. Used both to detect an already-patched file and to report
// whether alias hostnames will work (see RealtimeAcceptsAnyHost).
const siteNamePatched = "} else if (conf.default_site) {"

// PatchAuthenticateJs applies two edits to Frappe's realtime authenticate
// middleware. Both are idempotent and must be re-applied after a bench update.
//
//  1. Origin check. Upstream rejects a connection whenever the Host and Origin
//     hostnames differ — including when Origin is absent, which is exactly what
//     browsers do for same-origin GETs. Guard the comparison on Origin being
//     present so those connections are allowed instead of failing with
//     "Invalid origin". This is also what lets ffm strip the Origin header on
//     the prod alias socket.io router, where Host is an alias and any fixed
//     Origin value would necessarily mismatch.
//
//  2. Namespace check. The browser connects to the namespace
//     "/<frappe.boot.sitename>" (frappe/public/js/frappe/socketio_client.js),
//     and the server compares it against get_site_name(socket), which resolves
//     the site from the X-Frappe-Site-Name header, then conf.default_site — but
//     only when Host is localhost/127.0.0.1 — then the Origin or Host hostname.
//     So reaching a bench on any hostname that is not literally the site name
//     (an alias domain, a LAN IP) fails with "Invalid namespace". Dropping the
//     localhost restriction makes conf.default_site authoritative, which is
//     correct here because every ffm bench hosts exactly one site: create runs
//     `bench use <site>`, which writes default_site into common_site_config.json.
//
// Either edit is skipped when its target text is absent (already patched, or a
// Frappe version that reformatted the function), matching PatchUtilsJs.
func PatchAuthenticateJs(benchDir string) error {
	path := authenticateJsPath(benchDir)
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(content)
	changed := false

	const originOriginal = `if (get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin))`
	const originPatched = `if (socket.request.headers.origin && get_hostname(socket.request.headers.host) != get_hostname(socket.request.headers.origin))`
	if strings.Contains(text, originOriginal) {
		text = strings.Replace(text, originOriginal, originPatched, 1)
		changed = true
	}

	const siteNameOriginal = "} else if (\n\t\tconf.default_site &&\n\t\t" +
		"[\"localhost\", \"127.0.0.1\"].indexOf(get_hostname(socket.request.headers.host)) !== -1\n\t) {"
	if strings.Contains(text, siteNameOriginal) {
		text = strings.Replace(text, siteNameOriginal, siteNamePatched, 1)
		changed = true
	}

	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// RealtimeAcceptsAnyHost reports whether authenticate.js resolves the site from
// conf.default_site regardless of the request Host — i.e. whether edit 2 above
// is in effect. Callers use it to warn when adding a domain alias to a dev
// bench, where socket.io connects straight to the published port and Traefik
// cannot inject an X-Frappe-Site-Name header as a fallback.
func RealtimeAcceptsAnyHost(benchDir string) bool {
	content, err := os.ReadFile(authenticateJsPath(benchDir))
	if err != nil {
		return false
	}
	return strings.Contains(string(content), siteNamePatched)
}

// PatchUtilsJs patches Frappe's realtime utils so that get_url always uses
// socketio_frappe_url (http://127.0.0.1:8000) for server-to-server auth instead
// of the browser's Origin header. Using origin causes ENOTFOUND / ECONNREFUSED
// inside the container because hostnames like "sitename.localhost" or direct
// host ports (localhost:8040) don't resolve from inside Docker. Must be
// re-applied after bench update (same lifecycle as PatchAuthenticateJs).
//
// Frappe rewrote realtime/utils.js's get_url() between version-15 and
// version-16, so the v15 string this patch originally targeted no longer
// exists on v16 — the function silently no-ops instead of patching. v16 only
// substitutes webserver_port when developer_mode is set and otherwise still
// falls back to the raw origin, so the same server-to-server auth bug remains
// unpatched on prod (developer_mode=0) v16 benches. Try both function bodies;
// whichever one matches gets patched, the other is a no-op.
func PatchUtilsJs(benchDir string) error {
	path := filepath.Join(benchDir, "workspace", "frappe-bench", "apps", "frappe",
		"realtime", "utils.js")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(content)
	if strings.Contains(text, "socketio_frappe_url") {
		return nil // already patched
	}

	if v15Original := `return socket.request.headers.origin + path;`; strings.Contains(text, v15Original) {
		patched := `return (require("../node_utils").get_conf().socketio_frappe_url || socket.request.headers.origin || "http://localhost:8000") + path;`
		return os.WriteFile(path, []byte(strings.Replace(text, v15Original, patched, 1)), 0o644)
	}

	if v16Original := `let url = socket.request.headers.origin;`; strings.Contains(text, v16Original) {
		patched := "if (conf.socketio_frappe_url) {\n\t\treturn conf.socketio_frappe_url + path;\n\t}\n\t" +
			"let url = socket.request.headers.origin || `http://localhost:${conf.webserver_port || 8000}`;"
		return os.WriteFile(path, []byte(strings.Replace(text, v16Original, patched, 1)), 0o644)
	}

	return nil // unrecognized file structure; leave it alone
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
