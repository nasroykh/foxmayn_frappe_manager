<div align="center">
  <img width="150" height="150" alt="logo-foxmayn" src="https://github.com/user-attachments/assets/fa9f3727-dd5c-4748-92e9-f527a740366a" />
</div>

# ffm — Foxmayn Frappe Manager

A Go CLI that wraps Docker Compose to create, manage, and destroy Frappe benches with a single command. Supports both **development** and **production** modes. No YAML to write, no Docker flags to memorize.

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

## Modes

| | Development (`--mode dev`) | Production (`--mode prod`) |
|--|--|--|
| **Purpose** | Local dev with Claude Code, ffc, hot-reload | VPS deployment |
| **Containers** | 4 (frappe + db + redis×2) | 7 (gunicorn + socketio + workers + scheduler + db + redis×2) |
| **Image** | Full dev tools (zsh, starship, Go, ffc, Claude Code) | Minimal (no dev tools) |
| **Database** | MariaDB 11.8 or PostgreSQL 18 (experimental) | MariaDB 11.8 or PostgreSQL 18 (experimental) |
| **Site name** | `<name>.localhost` | Your public domain |
| **SSL** | Via shared Traefik proxy | Let's Encrypt (or `--no-ssl` for external Caddy/Nginx) |
| **Dev server** | `bench start` (honcho) | Services run via compose `command:` |

## Quick start — Development

```bash
# Create a new bench (interactive form: mode, version + apps)
ffm create mybench

# Open the site
open http://localhost:8000   # or whatever port was allocated
# Login: administrator / admin

# Enable domain routing (mybench.localhost)
ffm proxy start

# Shell into the bench container (zsh inside frappe-bench/)
ffm shell

# Stop / start / restart
ffm stop
ffm start
ffm restart

# Tear it all down
ffm delete
```

Most commands accept an optional bench name. If omitted, ffm resolves it automatically: if your working directory is inside `~/frappe/<name>/`, that bench is selected silently. Otherwise an interactive picker appears.

## Quick start — Production (VPS)

```bash
# Requires: public DNS A record for your domain pointing to this server
# Requires: ports 80 and 443 open on the firewall

ffm create mysite \
  --mode prod \
  --domain erp.example.com \
  --admin-password StrongPassword \
  --acme-email admin@example.com

# Access https://erp.example.com — Let's Encrypt cert is provisioned automatically
```

**With existing Caddy/Nginx on 80/443:**

```bash
# --no-ssl: skip Traefik on 443, expose ports directly for Caddy to proxy
ffm create mysite --mode prod --domain erp.example.com --no-ssl --admin-password StrongPassword

# Configure Frappe to know the browser connects via Caddy's HTTPS
ffm set-proxy mysite --host erp.example.com   # sets socketio_port 443, use_ssl 1, host_name
ffm restart mysite                             # apply to all services

# Get a ready-to-paste Caddy snippet (uses actual allocated ports)
ffm set-proxy mysite --host erp.example.com --print-caddy
```

## Commands

### `ffm create <name>`

Creates and starts a new Frappe bench end-to-end. When run interactively (no flags), a form asks for mode first, then the relevant options.

#### Dev mode (default)

Steps performed:

1. Allocates a free host port pair (web: 8000+, socketio: 9000+)
2. Writes `docker-compose.yml` and `Dockerfile` to `~/frappe/<name>/`
3. Builds the Docker image — installs **zsh**, **zinit**, **starship**, **Go**, **[ffc](https://github.com/nasroykh/foxmayn_frappe_cli)**, **pnpm**, and **Claude Code**; pre-fetches 60 [Frappe Claude skills](https://github.com/OpenAEC-Foundation/Frappe_Claude_Skill_Package) to `/opt/`. **Cached after first build.**
4. Runs `bench init` — clones Frappe, installs Python/Node deps, copies skills into `frappe-bench/.agents/skills/` and `.claude/skills/`
5. Starts 4 containers with `workspace/` bind-mounted at `/workspace`
6. Configures `common_site_config.json`, creates site, enables developer mode
7. Installs any `--apps`
8. Starts the dev server (`nohup bench start`)
9. Generates Frappe API keys and writes `~/.config/ffc/config.yaml` for ffc

#### Prod mode (`--mode prod`)

Steps performed:

1–5. Same as dev (minimal image, no dev tools, no devcontainer written)
6. Configures `common_site_config.json`, creates site (skip developer mode)
7. Installs any `--apps`
8. Builds production assets (`bench build`)
9. Sets `host_name` to `https://<domain>` (or `http://` with `--no-ssl`)

On failure, all steps auto-rollback (containers torn down, directory removed).

```
Flags:
  --mode string           Bench mode: dev or prod (default "dev")
  --domain string         Public domain for production (required with --mode prod)
  --no-ssl                Skip Let's Encrypt — use when Caddy/Nginx already handles TLS
  --acme-email string     Email for Let's Encrypt (required on first prod+SSL bench;
                          saved to ~/.config/ffm/.acme_email for subsequent benches)
  --frappe-branch string  Frappe branch to initialise (default "version-15")
  --apps stringArray      Apps to install (see formats below)
  --admin-password string Frappe site admin password (default "admin"; required for prod)
  --db-type string        Database engine: mariadb or postgres (default "mariadb")
  --db-password string    Database root password (default "ffm123456")
  --github-token string   GitHub PAT for private HTTPS repos
  --proxy-port int        Dev reverse proxy: set socketio_port (e.g. 443 or 80)
  --proxy-host string     Dev reverse proxy: set per-site host_name
  --verbose               Stream full Docker and bench init output
```

#### `--apps` formats

```bash
ffm create mybench --apps erpnext --apps hrms
ffm create mybench --apps erpnext@version-16
ffm create mybench --apps git@github.com:myorg/myapp.git
ffm create mybench --apps "git@github.com:myorg/myapp.git@main"
ffm create mybench --apps https://github.com/myorg/myapp
ffm create mybench --apps "https://github.com/myorg/myapp@develop" --github-token ghp_xxx
```

When `SSH_AUTH_SOCK` is set, the SSH agent is automatically forwarded into the container so SSH-URL private repos work without a token.

### `ffm list` / `ffm ls`

Lists all managed benches with their live status, mode (dev/prod), DB engine (maria/pg), port, domain URL, and Frappe branch.

### `ffm status [name]`

Shows per-container status, credentials, ports, and URLs. Prod benches show the domain URL instead of `localhost`.

### `ffm start [name]`

Starts a stopped bench. Dev: also reinstalls skills if missing and relaunches `bench start`. Prod: `docker compose up -d` only (services run via compose `command:`).

### `ffm stop [name]`

Stops all containers. Data is preserved.

### `ffm restart [name]`

Stops then starts a bench in one step.

```
Flags:
  --rebuild   Rebuild the Docker image before starting
```

`--rebuild` rewrites the `Dockerfile` from the current template (mode-aware) and runs `docker compose build`. Useful after an ffm upgrade adds new tools or template changes.

### `ffm shell [name]`

Opens an interactive shell inside the `frappe` container:
- **Dev**: `zsh` with zinit, autosuggestions, syntax-highlighting, starship
- **Prod**: `bash`

Use `--exec` to run a single command non-interactively:

```bash
ffm shell mybench --exec "bench list-apps"
ffm shell mybench --exec "bench --site mybench.localhost migrate"
ffm shell myprod  --exec "bench build"
ffm shell myprod  --exec "bench --site erp.example.com migrate"
```

```
Flags:
  --service string   Container to exec into (default "frappe")
  --exec string      Run a command non-interactively
```

### VS Code devcontainer (dev only)

Every dev bench includes `.devcontainer/devcontainer.json`.

```bash
# Option A: native host editing
code ~/frappe/mybench/workspace

# Option B: open inside the container (integrated terminal)
code ~/frappe/mybench
# → VS Code prompts "Reopen in Container"
```

Both options work simultaneously — same bind-mounted files.

### `ffm logs [name] [service]`

Streams container logs. Omit `[service]` to tail all containers.

```
Flags:
  -f, --follow   Follow log output (default true)
```

### `ffm proxy`

Manages the shared [Traefik](https://traefik.io/) container.

```bash
ffm proxy start    # start Traefik
ffm proxy stop     # stop Traefik
ffm proxy status   # show status + dashboard URL
```

- **Dev benches**: routes `<name>.localhost` on port 80
- **Prod benches with SSL**: also routes on port 443 with Let's Encrypt (added on first prod bench creation)

**WSL2 note**: Add `.localhost` entries to `C:\Windows\System32\drivers\etc\hosts`:
```
127.0.0.1  mybench.localhost
```

### `ffm set-proxy [name]`

Configures a bench (dev or prod) for an external reverse proxy (Caddy, Nginx, etc.). Sets `socketio_port`, `use_ssl`, `host_name`, and `socketio_frappe_url` inside the Frappe container.

- **Dev**: dev server restarts automatically
- **Prod**: prints a reminder to run `ffm restart <name>` to apply changes to all services

```bash
# HTTPS proxy on port 443 (default)
ffm set-proxy mybench --host frappe.example.com
ffm set-proxy myprod  --host erp.example.com

# HTTP proxy on port 80
ffm set-proxy mybench --port 80 --host frappe.example.com

# Reset to direct-access defaults
#   dev  → socketio_port <allocated>, use_ssl 0, host_name http://<name>.localhost, socketio_frappe_url http://127.0.0.1:8000
#   prod → socketio_port 443,          use_ssl 1, host_name https://<domain>,       socketio_frappe_url http://frappe:8000
ffm set-proxy mybench --reset
ffm set-proxy myprod  --reset

# Print a ready-to-paste config snippet
ffm set-proxy mybench --host frappe.example.com --print-caddy
ffm set-proxy myprod  --host erp.example.com    --print-nginx
```

### `ffm ffc [name]`

Generates Frappe API keys and writes `~/.config/ffc/config.yaml` inside the bench container. Dev benches only. Run if ffc setup failed during `ffm create` or to regenerate keys.

### `ffm delete [name]`

Stops and removes all containers, volumes, and the bench directory.

```
Aliases: rm, remove
Flags:  --force   Skip confirmation prompt
```

### `ffm update`

Checks GitHub for the latest release and replaces the running binary in place.

```bash
ffm update           # check and update
ffm update --check   # only check
ffm update --yes     # skip confirmation
```

Update availability is checked silently in the background on every command (24 h cache).

### `ffm --version` / `ffm -v`

Prints the build version, commit hash, and build date.

## File layout

```
~/frappe/
  <bench-name>/
    docker-compose.yml   # generated per bench (dev: 4 services, prod: 7 services)
    Dockerfile           # dev: full tools image; prod: minimal image
    workspace/           # bind-mounted at /workspace in container
      frappe-bench/
        .agents/skills/  # dev only: 60 Frappe Claude skills + ffc skill
        .claude/skills/  # dev only: same skills for Claude Code
    .devcontainer/       # dev only
      devcontainer.json  # VS Code dev container config

~/.config/ffm/
  benches.json           # state file tracking all managed benches
  .update_check.json     # cached latest release tag (refreshed every 24 h)
  .acme_email            # saved Let's Encrypt email (auto-used on subsequent prod benches)
```

## Services per bench

**Dev (4 containers):**

| Service | Image | Purpose |
|--|--|--|
| `frappe` | Built locally (dev image) | App server + `bench start` (honcho) + all dev tools |
| `mariadb` or `postgres` | `mariadb:11.8` / `postgres:18` | Database (selected via `--db-type`) |
| `redis-cache` | `redis:alpine` | Cache |
| `redis-queue` | `redis:alpine` | Background job queue |

**Prod (7 containers):**

| Service | Image | Purpose |
|--|--|--|
| `frappe` | Built locally (minimal image) | Gunicorn (`bench serve --port 8000`) |
| `socketio` | same | Node SocketIO server |
| `worker-long` | same | Long background jobs |
| `worker-short` | same | Short background jobs |
| `scheduler` | same | Scheduled tasks (`bench schedule`) |
| `mariadb` or `postgres` | `mariadb:11.8` / `postgres:18` | Database with healthcheck (selected via `--db-type`) |
| `redis-cache` | `redis:alpine` | Cache |
| `redis-queue` | `redis:alpine` | Job queue |

## Proxy container

A single Traefik container (`ffm-proxy`) is shared across all benches:

| Container | Image | Ports |
|--|--|--|
| `ffm-proxy` | `traefik:3` | `0.0.0.0:80` (HTTP), `0.0.0.0:443` (HTTPS, when a prod bench uses SSL), `127.0.0.1:8080` (dashboard) |

Configured entirely via CLI flags — no config file on disk. Uses `--restart=unless-stopped`.

## Environment variables

| Variable | Default | Description |
|--|--|--|
| `FFM_BENCHES_DIR` | `~/frappe` | Where bench directories are stored |
| `FFM_CONFIG_DIR` | `~/.config/ffm` | Where the state file is stored |

## Building from source

```bash
make          # tidy + build + install (default)
make ship     # same as above explicitly
make build    # → ./bin/ffm
make install  # → $GOPATH/bin/ffm
make vet      # go vet
make fmt      # gofmt
make tidy     # go mod tidy
make clean    # remove binary
```
