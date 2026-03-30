---
name: foxmayn-frappe-manager
description: >
  How to use ffm (Foxmayn Frappe Manager) to create, manage, and operate
  Dockerized Frappe/ERPNext development benches. Use this skill whenever the
  user mentions "ffm", "frappe manager", "frappe bench", "bench docker",
  "create a bench", "create a frappe site", "start frappe", "stop frappe",
  "delete bench", "frappe proxy", "reverse proxy for frappe",
  "set-proxy", "bench shell", "frappe container", or any task involving
  provisioning, starting, stopping, restarting, deleting, or debugging
  Dockerized Frappe development environments. Also trigger when the user
  wants to run bench commands inside a Docker container, list existing
  benches, configure domain routing, deploy Frappe behind Caddy/Nginx, open
  a shell inside a Frappe container, check bench status, view container logs,
  rebuild a bench image, or use ffc inside a bench. Even if the user doesn't
  say "ffm" directly — if they want to spin up a local Frappe dev environment
  or interact with one, this is the skill to use.
---

# ffm — Foxmayn Frappe Manager

A Go CLI that wraps Docker Compose to create, manage, and destroy local Frappe development benches with a single command. Each bench gets its own isolated Docker Compose project with MariaDB, Redis, and a fully configured Frappe container.

## Prerequisites

- Docker with the Compose plugin (`docker compose`)
- `ffm` binary installed (see Installation below)

## Installation

```bash
# Linux / macOS — one-liner
curl -fsSL https://raw.githubusercontent.com/nasroykh/foxmayn_frappe_manager/main/install.sh | sh

# Go install
go install github.com/nasroykh/foxmayn_frappe_manager/cmd/ffm@latest

# Self-update
ffm update
```

---

## Core Concepts

**Bench** = a self-contained Frappe development environment running in Docker. Each bench has:
- A directory at `~/frappe/<name>/` with `docker-compose.yml`, `Dockerfile`, and `workspace/`
- 4 Docker containers: `frappe` (app + dev server), `mariadb`, `redis-cache`, `redis-queue`
- The site name is always `<name>.localhost`
- The admin user is `administrator` with password `admin` (default)
- Bench files live on the host at `~/frappe/<name>/workspace/frappe-bench/` (bind-mounted)

**Proxy** = a shared Traefik container (`ffm-proxy`) that routes `<name>.localhost` to the correct bench.

**ffc** = Foxmayn Frappe CLI, a separate tool pre-installed inside every bench container for querying the Frappe REST API from the command line.

---

## Essential Commands

### Creating a bench

```bash
# Interactive form (choose version + apps)
ffm create mybench

# Non-interactive with explicit options
ffm create mybench --frappe-branch version-16 --apps erpnext --apps hrms

# With a custom/private app
ffm create mybench --apps "git@github.com:myorg/myapp.git@main"

# For VPS deployment behind a reverse proxy
ffm create mybench --proxy-port 443 --proxy-host frappe.example.com
```

`ffm create` performs ~11 steps automatically: port allocation → compose/Dockerfile generation → Docker build → bench init → container start → site creation → developer mode → app installation → dev server start → ffc API key generation. If any step fails, it automatically tears down everything for a clean retry.

### Listing benches

```bash
ffm list          # or: ffm ls
```

Shows all benches with live status (running/stopped), port, domain URL, and Frappe branch.

### Bench status

```bash
ffm status mybench
```

Shows per-container status, credentials, ports, URLs, and installed apps.

### Lifecycle (start / stop / restart)

```bash
ffm start mybench
ffm stop mybench
ffm restart mybench
ffm restart mybench --rebuild   # rebuild Docker image first (picks up new tool versions)
```

If the bench name is omitted, ffm resolves it automatically: if your working directory is inside `~/frappe/<name>/`, that bench is selected silently. Otherwise an interactive picker appears (auto-selects when only one bench exists).

### Deleting a bench

```bash
ffm delete mybench             # prompts for confirmation
ffm delete mybench --force     # skip confirmation
# aliases: ffm rm, ffm remove
```

Removes all containers, volumes, and the bench directory. **Irreversible.**

---

## Running Commands Inside the Bench Container

This is the most important capability for an LLM. The `ffm shell` command with `--exec` lets you run any command inside the Frappe Docker container without needing to enter an interactive shell.

### `ffm shell <name> --exec "<command>"`

Runs a command non-interactively inside the `frappe` container at `/workspace/frappe-bench/` and prints the output. You stay in your host shell.

```bash
# List installed apps
ffm shell mybench --exec "bench list-apps"

# Check Frappe version
ffm shell mybench --exec "bench version"

# Run a bench console command
ffm shell mybench --exec "bench --site mybench.localhost console"

# Execute bench migrate
ffm shell mybench --exec "bench --site mybench.localhost migrate"

# Clear cache
ffm shell mybench --exec "bench --site mybench.localhost clear-cache"

# Install a new app
ffm shell mybench --exec "bench get-app erpnext --branch version-15"
ffm shell mybench --exec "bench --site mybench.localhost install-app erpnext"

# Run a Frappe Python expression
ffm shell mybench --exec "bench --site mybench.localhost execute frappe.client.get_count --args '{\"doctype\":\"ToDo\"}'"

# Use ffc (pre-installed inside the container)
ffm shell mybench --exec "ffc list-docs -d Customer --json"
ffm shell mybench --exec "ffc get-doc -d 'Sales Invoice' -n 'SINV-0001' --json"
ffm shell mybench --exec "ffc ping --json"

# Run arbitrary shell commands
ffm shell mybench --exec "ls apps/"
ffm shell mybench --exec "cat sites/mybench.localhost/site_config.json"
ffm shell mybench --exec "pip list | grep frappe"

# Access the MariaDB shell
ffm shell mybench --service mariadb --exec "mysql -u root -p123 -e 'SHOW DATABASES;'"
```

### Interactive shell

```bash
ffm shell mybench              # drops into zsh at /workspace/frappe-bench
ffm shell mybench --service mariadb   # bash shell in the MariaDB container
```

The frappe container has a pre-configured zsh with autosuggestions, syntax highlighting, starship prompt, and Go/ffc/pnpm on PATH.

---

## Proxy (Domain Routing)

```bash
ffm proxy start    # start Traefik → enables http://<bench>.localhost
ffm proxy stop     # stop Traefik (benches still reachable on direct ports)
ffm proxy status   # show status + dashboard URL
```

After `ffm proxy start`, every running bench is accessible at `http://<name>.localhost`. The Traefik dashboard runs at `http://localhost:8080/dashboard/`.

---

## Reverse Proxy for VPS Deployments

### Configure at creation time

```bash
ffm create mysite --proxy-port 443 --proxy-host frappe.example.com
```

### Configure an existing bench

```bash
# HTTPS proxy (default port 443)
ffm set-proxy mybench --host frappe.example.com

# HTTP proxy on port 80
ffm set-proxy mybench --port 80 --host frappe.example.com

# Reset to local direct-access settings
ffm set-proxy mybench --reset

# Generate web server config snippets
ffm set-proxy mybench --host frappe.example.com --print-caddy
ffm set-proxy mybench --host frappe.example.com --print-nginx
```

What `set-proxy` changes inside the container:

| Setting | Default | Proxy mode |
|---------|---------|------------|
| `socketio_port` (global) | `9000` | proxy port (443 or 80) |
| `use_ssl` (global) | `0` | `1` when port is 443 |
| `host_name` (per-site) | `http://name.localhost` | `https://frappe.example.com` |

The dev server restarts automatically.

---

## Container Logs

```bash
ffm logs mybench               # tail all container logs (follows by default)
ffm logs mybench frappe         # tail only the frappe container
ffm logs mybench mariadb        # tail only MariaDB
```

---

## ffc Setup

Every bench has [ffc](https://github.com/nasroykh/foxmayn_frappe_cli) (Foxmayn Frappe CLI) pre-installed with API keys auto-generated during `ffm create`. If setup failed or you need to regenerate keys:

```bash
ffm ffc mybench
```

This generates new API keys for the Administrator user and writes the ffc config inside the container.

---

## VS Code Integration

```bash
# Option A: edit bench files directly on host
code ~/frappe/mybench/workspace

# Option B: open inside the container (integrated terminal with bench/ffc/go on PATH)
code ~/frappe/mybench
# → VS Code prompts "Reopen in Container"
```

Both work simultaneously — same bind-mounted files.

---

## File Layout

```
~/frappe/<bench-name>/
  docker-compose.yml       # generated per bench
  Dockerfile               # extends frappe/bench:latest + tools
  workspace/               # bind-mounted at /workspace in container
    frappe-bench/           # ← actual Frappe bench (apps/, sites/, etc.)
      .agents/skills/      # 60 Frappe skills + ffc skill
  .devcontainer/
    devcontainer.json       # VS Code dev container config

~/.config/ffm/
  benches.json              # tracks all managed benches
```

---

## Common Tasks Reference

| Task | Command |
|------|---------|
| Create a bench | `ffm create mybench` |
| List all benches | `ffm list` |
| Check bench status | `ffm status mybench` |
| Start a bench | `ffm start mybench` |
| Stop a bench | `ffm stop mybench` |
| Restart a bench | `ffm restart mybench` |
| Rebuild + restart | `ffm restart mybench --rebuild` |
| Delete a bench | `ffm delete mybench --force` |
| Interactive shell | `ffm shell mybench` |
| Run command in container | `ffm shell mybench --exec "..."` |
| List installed apps | `ffm shell mybench --exec "bench list-apps"` |
| Install an app | `ffm shell mybench --exec "bench get-app <app> && bench --site mybench.localhost install-app <app>"` |
| Run bench migrate | `ffm shell mybench --exec "bench --site mybench.localhost migrate"` |
| Clear cache | `ffm shell mybench --exec "bench --site mybench.localhost clear-cache"` |
| Check site config | `ffm shell mybench --exec "cat sites/mybench.localhost/site_config.json"` |
| Query Frappe data (via ffc) | `ffm shell mybench --exec "ffc list-docs -d Customer --json"` |
| Start the proxy | `ffm proxy start` |
| Setup for VPS | `ffm set-proxy mybench --host frappe.example.com` |
| Generate Caddy config | `ffm set-proxy mybench --host frappe.example.com --print-caddy` |
| View container logs | `ffm logs mybench` |
| Regenerate ffc keys | `ffm ffc mybench` |
| Update ffm itself | `ffm update` |

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `FFM_BENCHES_DIR` | `~/frappe` | Where bench directories are stored |
| `FFM_CONFIG_DIR` | `~/.config/ffm` | Where the state file is stored |

---

## Credentials (Defaults)

| What | Value |
|------|-------|
| Site admin | `administrator` / `admin` |
| MariaDB root | `root` / `123` |
| Site name | `<bench-name>.localhost` |

Override during creation with `--admin-password` and `--db-password`.

---

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Bench stuck during creation | Ctrl+C and retry — `ffm create` auto-cleans on failure |
| "bench already exists" | The previous attempt didn't clean up; `rm -rf ~/frappe/<name>` and retry |
| Port conflict | ffm auto-allocates ports, but if Docker left orphans: `docker ps -a` and clean up |
| Proxy domain doesn't work | Run `ffm proxy start`; on WSL2, add to Windows `hosts` file |
| ffc not working | Run `ffm ffc mybench` to regenerate API keys |
| Need to update tools in image | `ffm restart mybench --rebuild` |
| "No module named frappe" | The venv path patching may have failed; recreate the bench |
| Container won't start | Check `ffm logs mybench` and `ffm status mybench` |
