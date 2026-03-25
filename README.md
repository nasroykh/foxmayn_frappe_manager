# ffm — Foxmayn Frappe Manager

A Go CLI that wraps [frappe_docker](https://github.com/frappe/frappe_docker)'s devcontainer compose pattern so you can create, manage, and destroy local Frappe development benches with a single command. No YAML to write, no Docker flags to memorize.

## Requirements

- [Docker](https://docs.docker.com/get-docker/) with the Compose plugin (`docker compose`)
- Go 1.21+ (only needed to build from source)

## Installation

### Linux / macOS — one-liner

```bash
curl -fsSL https://raw.githubusercontent.com/nasroykh/foxmayn_frappe_manager/main/install.sh | sh
```

Detects OS and architecture, downloads the latest release binary, verifies the SHA256 checksum, and installs to `/usr/local/bin` (or `~/.local/bin` if the former is not writable).

### Windows — PowerShell one-liner

```powershell
powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/nasroykh/foxmayn_frappe_manager/main/install.ps1 | iex"
```

Installs to `%LOCALAPPDATA%\Programs\ffm` and adds it to your user `PATH` automatically. No admin rights required.

### Go install (requires Go toolchain)

```bash
go install github.com/nasroykh/foxmayn_frappe_manager/cmd/ffm@latest
```

### Build from source

```bash
git clone https://github.com/nasroykh/foxmayn_frappe_manager
cd foxmayn_frappe_manager
make install
```

## Quick start

```bash
# Create a new bench (interactive form: version + apps)
ffm create mybench

# Open the site
open http://localhost:8000   # or whatever port was allocated
# Login: administrator / admin

# Enable domain routing (mybench.localhost)
ffm proxy start

# Shell into the bench container (drops you into zsh inside frappe-bench/)
ffm shell

# Stop and start
ffm stop
ffm start

# Tear it all down
ffm delete
```

Most commands accept an optional bench name. If omitted, an interactive picker lets you select from your existing benches.

## Commands

### `ffm create <name>`

Creates and starts a new Frappe development bench end-to-end. When run without `--frappe-branch` or `--apps`, an interactive form lets you choose:

- Frappe version (v15 stable / v16 latest)
- Additional apps to install (ERPNext, HRMS)
- Optional custom app (short name, git URL, or `url@branch`)

Steps performed:

1. Allocates a free host port pair (web: 8000+, socketio: 9000+)
2. Writes `docker-compose.yml` and a `Dockerfile` to `~/frappe/<name>/`
3. Builds the Docker image — runs `bench init` (clones Frappe, installs Python/Node deps), installs **zsh**, **zinit**, **starship**, **Go 1.26**, and **[ffc](https://github.com/nasroykh/foxmayn_frappe_cli)**, all baked into the image layer. **This step is cached**: the second bench with the same Frappe branch builds in seconds instead of minutes
4. Starts MariaDB, Redis (cache + queue), and the Frappe container (bench data persisted in a named Docker volume)
5. Configures `common_site_config.json` with DB and Redis connection strings
6. Creates the site with `bench new-site`
7. Enables developer mode
8. Installs any additional `--apps` (public or private)
9. Starts the dev server via `nohup bench start`
10. Generates Frappe API keys and writes `~/.config/ffc/config.yaml` inside the container

```
Flags:
  --frappe-branch string      Frappe branch to initialise (default "version-15")
  --apps stringArray          Apps to install — see formats below
  --admin-password string     Frappe site admin password (default "admin")
  --db-password string        MariaDB root password (default "123")
  --github-token string       GitHub personal access token for private HTTPS repos
  --proxy-port int            Configure for reverse proxy: set socketio_port to this value
                              (e.g. 443 for HTTPS, 80 for HTTP). Omit for local dev.
  --proxy-host string         Public domain for reverse proxy, e.g. frappe.example.com
                              Sets per-site host_name for correct link generation.
  -v, --verbose               Show docker compose output
```

**VPS one-liner example:**

```bash
ffm create mysite --proxy-port 443 --proxy-host frappe.example.com
```

This creates the bench and immediately configures `socketio_port 443`, `use_ssl 1`, and `host_name https://frappe.example.com` so it works correctly behind an HTTPS reverse proxy.

#### `--apps` formats

```bash
# Public short name (branch defaults to --frappe-branch):
ffm create mybench --apps erpnext --apps hrms

# Short name with explicit branch override:
ffm create mybench --apps erpnext@version-16

# Private SSH repo (requires SSH agent — see below):
ffm create mybench --apps git@github.com:myorg/myapp.git

# Private SSH repo with explicit branch:
ffm create mybench --apps "git@github.com:myorg/myapp.git@main"

# HTTPS URL (public or private with --github-token):
ffm create mybench --apps https://github.com/myorg/myapp
ffm create mybench --apps "https://github.com/myorg/myapp@develop" --github-token ghp_xxx
```

#### SSH agent forwarding

When `SSH_AUTH_SOCK` is set in your environment (i.e. you have an SSH agent running), ffm automatically mounts the socket into the frappe container so SSH-URL private repos work without a token. This is written into `docker-compose.yml` at bench creation time and re-evaluated by Docker Compose on every `ffm start`.

### `ffm list` / `ffm ls`

Lists all managed benches with their live status, port, domain URL, and Frappe branch. When the proxy is running, domain URLs are shown in colour; when it is stopped a `(proxy off)` note is appended and a reminder to run `ffm proxy start` is printed at the bottom.

### `ffm status [name]`

Shows per-container status for a bench (image, state, ports, uptime) along with its credentials: admin password, MariaDB root password, installed apps, and URLs. When a bench has been configured with `ffm set-proxy`, a `url (proxy)` line shows the public domain instead of the `.localhost` domain. If `name` is omitted, an interactive picker is shown.

### `ffm start [name]`

Starts a stopped bench and re-launches the dev server. If `name` is omitted, an interactive picker is shown.

### `ffm stop [name]`

Stops all containers for a bench. Data is preserved — use `start` to resume. If `name` is omitted, an interactive picker is shown.

### `ffm shell [name]`

Opens an interactive **zsh** shell inside the frappe container, landing directly in `/home/frappe/frappe-bench`. The shell comes with zinit, zsh-autosuggestions, zsh-syntax-highlighting, history search, fixed key bindings (Ctrl/Alt+Arrow, Home, End, Delete), and a custom starship prompt — all baked into the image.

Use `--exec` to run a single command non-interactively and print its output — the user stays in their own shell. Go and ffc are on the PATH automatically.

```bash
ffm shell mybench --exec "ffc list-docs -D Company"
ffm shell mybench --exec "bench --site mybench.localhost list-apps"
```

If `name` is omitted, an interactive picker is shown.

```
Flags:
  --service string   Container to shell into (default "frappe")
                     Use "mariadb" to get a DB shell (uses bash), etc.
  --exec string      Run a command non-interactively and print its output
```

### `ffm logs [name] [service]`

Streams container logs. Omit `[service]` to tail all containers. If `name` is omitted, an interactive picker is shown.

```
Flags:
  -f, --follow   Follow log output (default true)
```

### `ffm proxy`

Manages the shared [Traefik](https://traefik.io/) reverse proxy that enables `<bench>.localhost` domain routing across all benches. The proxy is a single container shared by every bench — you only need to start it once.

```bash
ffm proxy start    # start Traefik, create ffm-proxy network if absent
ffm proxy stop     # stop Traefik (benches still reachable on direct ports)
ffm proxy status   # show status + dashboard URL
ffm proxy          # alias for 'ffm proxy status'
```

After `ffm proxy start`, every running bench is accessible at `http://<name>.localhost` in addition to its direct port. The Traefik dashboard is available at `http://localhost:8080/dashboard/`.

**WSL2 note**: `.localhost` subdomains resolve inside WSL2 but not in a Windows browser by default. Either use the direct port URL (`ffm list`) or add entries to `C:\Windows\System32\drivers\etc\hosts`:
```
127.0.0.1  mybench.localhost
```

### `ffm set-proxy [name]`

Configures a running bench to work correctly behind an external reverse proxy (Caddy, Nginx, etc.). Applies the necessary Frappe settings inside the container and restarts the dev server.

```bash
# HTTPS proxy on port 443 (default)
ffm set-proxy mybench --host frappe.example.com

# HTTP proxy on port 80
ffm set-proxy mybench --port 80 --host frappe.example.com

# Restore to local direct-access settings
ffm set-proxy mybench --reset

# Print a ready-to-paste Caddy config
ffm set-proxy mybench --host frappe.example.com --print-caddy

# Print a ready-to-paste Nginx config (includes SSL redirect block)
ffm set-proxy mybench --host frappe.example.com --print-nginx
```

What it changes inside the Frappe container:

| Setting | Default | Proxy mode |
|---|---|---|
| `socketio_port` (global) | `9000` | proxy port (443 or 80) |
| `use_ssl` (global) | `0` | `1` when port is 443 |
| `host_name` (per-site) | `http://name.localhost` | `https://frappe.example.com` |

The dev server is restarted automatically so changes take effect immediately. `--reset` reverses all three settings back to direct-access defaults.

```
Flags:
  --port int        Public port the reverse proxy listens on (default 443)
  --host string     Public domain, sets per-site host_name
  --no-ssl          Disable SSL mode even when --port 443
  --reset           Restore direct-access settings
  --print-caddy     Print a Caddy config snippet
  --print-nginx     Print an Nginx config snippet (includes WebSocket upgrade headers)
```

If `name` is omitted, an interactive picker is shown. The bench must be running.

### `ffm ffc [name]`

Generates Frappe API keys for the Administrator user and writes `~/.config/ffc/config.yaml` inside the bench container so that [ffc](https://github.com/nasroykh/foxmayn_frappe_cli) (Foxmayn Frappe CLI) is ready to use immediately after `ffm create`.

Run this manually if ffc setup was skipped or failed during `ffm create`, or to regenerate keys on an existing bench.

If `name` is omitted, an interactive picker is shown. The bench must be running.

### `ffm delete [name]`

Stops and removes all containers, volumes, and the bench directory. Prompts for confirmation unless `--force` is passed. If `name` is omitted, an interactive picker is shown.

```
Aliases: rm, remove

Flags:
  --force   Skip confirmation prompt
```

### `ffm version`

Prints the build version, commit hash, and build date.

## File layout

```
~/frappe/
  <bench-name>/
    docker-compose.yml   # generated per bench
    Dockerfile           # extends frappe/bench:latest with bench init + zsh + zinit + starship + Go + ffc

~/.config/ffm/
  benches.json           # state file tracking all managed benches

# repo root
install.sh               # curl | sh installer for Linux/macOS
install.ps1              # irm | iex installer for Windows
.goreleaser.yaml         # cross-platform release config (linux/darwin/windows, amd64/arm64)
.github/workflows/
  release.yml            # GitHub Actions: build + publish on v* tag push
```

## Environment variables

| Variable          | Default          | Description                        |
|-------------------|------------------|------------------------------------|
| `FFM_BENCHES_DIR` | `~/frappe`       | Where bench directories are stored |
| `FFM_CONFIG_DIR`  | `~/.config/ffm`  | Where the state file is stored     |

## Services per bench

Each bench runs four Docker containers scoped to a Compose project named `ffm-<name>`:

| Service       | Image                               | Purpose                                                      |
|---------------|-------------------------------------|--------------------------------------------------------------|
| `frappe`      | Built locally from bench Dockerfile | Frappe app + dev server (zsh + zinit + starship + Go + ffc)  |
| `mariadb`     | `mariadb:11.8`                      | Database                                                     |
| `redis-cache` | `redis:alpine`                      | Cache                                                        |
| `redis-queue` | `redis:alpine`                      | Background job queue                                         |

The `frappe` container is also attached to the shared `ffm-proxy` Docker network and carries Traefik labels for `<name>.localhost` routing. MariaDB and Redis remain on the default project network only.

## Proxy container

A single Traefik container (`ffm-proxy`) is shared across all benches:

| Container   | Image        | Ports                             |
|-------------|--------------|-----------------------------------|
| `ffm-proxy` | `traefik:3`  | `0.0.0.0:80` (HTTP), `127.0.0.1:8080` (dashboard) |

The container is configured entirely via CLI flags — no config file is written. It uses `--restart=unless-stopped` so it survives Docker daemon restarts without re-running `ffm proxy start`.

## Building from source

```bash
make build    # → ./bin/ffm
make vet      # go vet
make fmt      # gofmt
make tidy     # go mod tidy
make clean    # remove binary
```
