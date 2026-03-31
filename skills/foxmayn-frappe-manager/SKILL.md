---
name: foxmayn-frappe-manager
description: >
  How to use ffm (Foxmayn Frappe Manager) to create, manage, and operate
  Dockerized Frappe/ERPNext development and production benches. Use this skill
  whenever the user mentions "ffm", "frappe manager", "frappe bench",
  "bench docker", "create a bench", "create a frappe site", "start frappe",
  "stop frappe", "delete bench", "frappe proxy", "reverse proxy for frappe",
  "set-proxy", "bench shell", "frappe container", "prod mode", "production
  frappe", "let's encrypt frappe", or any task involving provisioning, starting,
  stopping, restarting, deleting, or debugging Dockerized Frappe environments.
  Also trigger when the user wants to run bench commands inside a Docker
  container, list existing benches, configure domain routing, deploy Frappe
  behind Traefik with Let's Encrypt, open a shell inside a Frappe container,
  check bench status, view container logs, rebuild a bench image, or use ffc
  inside a bench. Even if the user doesn't say "ffm" directly — if they want
  to spin up a local Frappe dev environment or a production Frappe site, this
  is the skill to use.
---

# ffm — Foxmayn Frappe Manager

A Go CLI that wraps Docker Compose to create, manage, and destroy Frappe benches with a single command. Supports both **development** and **production** modes.

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

### Development mode (default)

Each dev bench has:
- A directory at `~/frappe/<name>/` with `docker-compose.yml`, `Dockerfile`, `workspace/`, and `.devcontainer/`
- 4 Docker containers: `frappe` (app + honcho dev server), `mariadb`, `redis-cache`, `redis-queue`
- Site name: `<name>.localhost` (routed via shared Traefik proxy)
- Admin: `administrator / admin` (default)
- Tools in container: zsh + zinit + starship + Go + ffc + pnpm + Claude Code + 60 Frappe skills
- Bench files on host at `~/frappe/<name>/workspace/frappe-bench/` (bind-mounted)

### Production mode (`--mode prod`)

Each prod bench has:
- 7+ Docker containers: `frappe` (gunicorn via `bench serve`), `socketio`, `worker-long`, `worker-short`, `scheduler`, `mariadb`, `redis-cache`, `redis-queue`
- Minimal Docker image (no dev tools)
- Site name = public domain (e.g. `erp.example.com`)
- Automatic Let's Encrypt SSL via Traefik (`websecure` entrypoint + ACME HTTP-01)
- HTTP → HTTPS redirect per bench (does not affect dev benches)
- `--no-ssl` flag to skip Let's Encrypt when TLS is handled externally

**Proxy** = a shared Traefik container (`ffm-proxy`) that routes all benches. For dev: `<name>.localhost` on port 80. For prod: public domain on ports 80/443 with Let's Encrypt.

**ffc** = Foxmayn Frappe CLI, pre-installed inside every dev bench container.

---

## Essential Commands

### Creating a bench

```bash
# Interactive form (choose mode, version + apps)
ffm create mybench

# Dev bench — explicit flags
ffm create mybench --frappe-branch version-16 --apps erpnext --apps hrms

# Production bench — basic
ffm create myprod --mode prod --domain erp.example.com \
    --admin-password MyStr0ngPass --acme-email admin@example.com

# Production bench — no SSL (handle TLS externally)
ffm create myprod --mode prod --domain erp.example.com \
    --admin-password MyStr0ngPass --no-ssl

# Dev bench with a custom/private app
ffm create mybench --apps "git@github.com:myorg/myapp.git@main"
```

`ffm create` performs the full pipeline automatically: port allocation → compose/Dockerfile generation → Docker build → bench init → container start → site creation → app installation → (dev: dev server + ffc setup) (prod: asset build). Auto-rolls back everything on failure.

**Production requirements:**
- `--domain` is required
- `--admin-password` must not be `admin`
- `--acme-email` required for the first SSL bench (saved to `~/.config/ffm/.acme_email` for subsequent benches)

### Listing benches

```bash
ffm list          # or: ffm ls
```

Shows all benches with live status (running/stopped), mode (dev/prod), port, domain URL, and Frappe branch.

### Bench status

```bash
ffm status mybench
```

Shows mode, per-container status, credentials, ports, URLs, and installed apps.

### Lifecycle (start / stop / restart)

```bash
ffm start mybench
ffm stop mybench
ffm restart mybench
ffm restart mybench --rebuild   # rebuild Docker image first (picks up new tool versions)
```

**CWD auto-detection:** If your current working directory is inside `~/frappe/<name>/`, the bench name is resolved automatically — no interactive picker needed.

For production benches, `ffm start` runs `docker compose up -d` only — services start automatically via their compose `command:` entries.

### Deleting a bench

```bash
ffm delete mybench             # prompts for confirmation
ffm delete mybench --force     # skip confirmation
# aliases: ffm rm, ffm remove
```

Removes all containers, volumes, and the bench directory. **Irreversible.**

---

## Running Commands Inside the Bench Container

The `ffm shell` command with `--exec` lets you run any command inside the Frappe Docker container.

### `ffm shell <name> --exec "<command>"`

Runs a command non-interactively inside the `frappe` container at `/workspace/frappe-bench/` and prints the output.

```bash
# List installed apps
ffm shell mybench --exec "bench list-apps"

# Run bench migrate
ffm shell mybench --exec "bench --site mybench.localhost migrate"

# Clear cache
ffm shell mybench --exec "bench --site mybench.localhost clear-cache"

# Install a new app
ffm shell mybench --exec "bench get-app erpnext --branch version-15"
ffm shell mybench --exec "bench --site mybench.localhost install-app erpnext"

# Use ffc (dev benches only)
ffm shell mybench --exec "ffc list-docs -d Customer --json"

# Production: rebuild assets after code changes
ffm shell myprod --exec "bench build"
ffm shell myprod --exec "bench --site erp.example.com migrate"
```

### Interactive shell

```bash
ffm shell mybench              # dev: drops into zsh at /workspace/frappe-bench
ffm shell myprod               # prod: drops into bash at /workspace/frappe-bench
ffm shell mybench --service mariadb   # bash shell in the MariaDB container
```

---

## Proxy (Domain Routing)

```bash
ffm proxy start    # start Traefik → enables http://<bench>.localhost (dev) / https://<domain> (prod)
ffm proxy stop     # stop Traefik (benches still reachable on direct ports)
ffm proxy status   # show status + dashboard URL
```

For dev benches, Traefik routes `<name>.localhost` on port 80. For prod benches, Traefik also handles port 443 with Let's Encrypt certificates (added automatically on first prod bench creation).

---

## Production with an Existing Reverse Proxy (Caddy/Nginx already on 80/443)

If Caddy, Nginx, or another proxy is already running on the VPS and occupying ports 80/443, Traefik cannot bind there. Use `--no-ssl` to skip Let's Encrypt and expose the bench ports directly on the host so your existing proxy can forward to them.

```bash
# 1. Create without SSL (no Traefik on 80/443)
ffm create kb --mode prod --domain kb.co --no-ssl --admin-password StrongPass

# 2. Check the allocated ports
ffm status kb
#    → note web port (e.g. 8000) and socketio port (e.g. 9000)

# 3. Fix Frappe's SSL config so it knows the browser connects via HTTPS through Caddy
ffm shell kb --exec "cd /workspace/frappe-bench \
  && bench set-config -gp socketio_port 443 \
  && bench set-config -gp use_ssl 1"
```

Then add to your **Caddyfile**:

```caddy
kb.co {
    reverse_proxy /socket.io/* localhost:9000
    reverse_proxy localhost:8000
}
```

Run `caddy reload` and the site is live.

> **Note:** `--no-ssl` sets `socketio_port=80` by default. Step 3 overrides it to 443 because Caddy is handling HTTPS and the browser connects on that port. Skip step 3 if your Caddy site is HTTP-only (`socketio_port` should stay at 80, and omit `use_ssl`).

Alternatively, use `ffm set-proxy` directly after creation — it now works for prod benches too:

```bash
ffm set-proxy kb --host kb.co   # sets socketio_port 443, use_ssl 1, host_name https://kb.co
ffm restart kb                  # apply to all services
```

---

## Configuring a Bench for a Reverse Proxy (`ffm set-proxy`)

Works for both **dev and prod** benches. Sets `socketio_port`, `use_ssl`, and `host_name` inside the container. For dev benches, the dev server restarts automatically. For prod benches, run `ffm restart <name>` afterwards.

```bash
# HTTPS proxy on port 443 (default)
ffm set-proxy mybench --host frappe.example.com       # dev: restarts bench start automatically
ffm set-proxy myprod  --host erp.example.com          # prod: prints "run ffm restart myprod"

# HTTP proxy on port 80
ffm set-proxy mybench --port 80 --host frappe.example.com

# Reset to direct-access defaults
#   dev  → socketio_port 9000, use_ssl 0, host_name http://<name>.localhost
#   prod → socketio_port 443,  use_ssl 1, host_name https://<domain>
ffm set-proxy mybench --reset
ffm set-proxy myprod  --reset

# Generate web server config snippets (works for both modes)
ffm set-proxy mybench --host frappe.example.com --print-caddy
ffm set-proxy myprod  --host erp.example.com    --print-nginx
```

---

## Container Logs

```bash
ffm logs mybench               # tail all container logs (follows by default)
ffm logs mybench frappe         # tail only the frappe container
ffm logs myprod worker-long    # tail a specific prod worker
```

---

## ffc Setup (Dev only)

Every dev bench has [ffc](https://github.com/nasroykh/foxmayn_frappe_cli) pre-installed with API keys auto-generated during `ffm create`. If setup failed or you need to regenerate keys:

```bash
ffm ffc mybench
```

---

## VS Code Integration (Dev only)

```bash
# Option A: edit bench files directly on host
code ~/frappe/mybench/workspace

# Option B: open inside the container (integrated terminal)
code ~/frappe/mybench
# → VS Code prompts "Reopen in Container"
```

---

## File Layout

```
~/frappe/<bench-name>/
  docker-compose.yml       # generated per bench (dev: 4 services, prod: 7+ services)
  Dockerfile               # dev: extends frappe/bench with tools; prod: minimal
  workspace/               # bind-mounted at /workspace in container
    frappe-bench/          # actual Frappe bench (apps/, sites/, etc.)
      .agents/skills/      # dev only: 60 Frappe skills + ffc skill
  .devcontainer/           # dev only: VS Code dev container config
    devcontainer.json

~/.config/ffm/
  benches.json             # tracks all managed benches
  .acme_email              # saved Let's Encrypt email (auto-used on subsequent prod benches)
```

---

## Common Tasks Reference

| Task                                       | Command                                                                                              |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| Create a dev bench                         | `ffm create mybench`                                                                                 |
| Create a prod bench (Let's Encrypt)        | `ffm create myprod --mode prod --domain erp.example.com --admin-password X --acme-email y@z.com`     |
| Create a prod bench (existing Caddy/Nginx) | `ffm create myprod --mode prod --domain erp.example.com --no-ssl --admin-password X`                 |
| List all benches                           | `ffm list`                                                                                           |
| Check bench status                         | `ffm status mybench`                                                                                 |
| Start a bench                              | `ffm start mybench`                                                                                  |
| Stop a bench                               | `ffm stop mybench`                                                                                   |
| Restart a bench                            | `ffm restart mybench`                                                                                |
| Rebuild + restart                          | `ffm restart mybench --rebuild`                                                                      |
| Delete a bench                             | `ffm delete mybench --force`                                                                         |
| Interactive shell (dev)                    | `ffm shell mybench` (zsh)                                                                            |
| Interactive shell (prod)                   | `ffm shell myprod` (bash)                                                                            |
| Run command in container                   | `ffm shell mybench --exec "..."`                                                                     |
| Bench migrate                              | `ffm shell mybench --exec "bench --site mybench.localhost migrate"`                                  |
| Clear cache                                | `ffm shell mybench --exec "bench --site mybench.localhost clear-cache"`                              |
| Install an app                             | `ffm shell mybench --exec "bench get-app <app> && bench --site mybench.localhost install-app <app>"` |
| Query data (ffc)                           | `ffm shell mybench --exec "ffc list-docs -d Customer --json"`                                        |
| Start the proxy                            | `ffm proxy start`                                                                                    |
| Setup dev for VPS                          | `ffm set-proxy mybench --host frappe.example.com`                                                    |
| View logs                                  | `ffm logs mybench`                                                                                   |
| Regenerate ffc keys                        | `ffm ffc mybench`                                                                                    |
| Update ffm itself                          | `ffm update`                                                                                         |

---

## Environment Variables

| Variable          | Default         | Description                        |
| ----------------- | --------------- | ---------------------------------- |
| `FFM_BENCHES_DIR` | `~/frappe`      | Where bench directories are stored |
| `FFM_CONFIG_DIR`  | `~/.config/ffm` | Where the state file is stored     |

---

## Credentials (Defaults — Dev)

| What         | Value                     |
| ------------ | ------------------------- |
| Site admin   | `administrator` / `admin` |
| MariaDB root | `root` / `ffm123456`      |
| Site name    | `<bench-name>.localhost`  |

Override during creation with `--admin-password` and `--db-password`.

**Production:** `--admin-password` is required and must not be `admin`.

---

## Troubleshooting

| Problem                                   | Solution                                                                      |
| ----------------------------------------- | ----------------------------------------------------------------------------- |
| Bench stuck during creation               | Ctrl+C and retry — `ffm create` auto-cleans on failure                        |
| "bench already exists"                    | The previous attempt didn't clean up; `rm -rf ~/frappe/<name>` and retry      |
| Port conflict                             | ffm auto-allocates ports; if Docker left orphans: `docker ps -a` and clean up |
| Proxy domain doesn't work                 | Run `ffm proxy start`; on WSL2, add to Windows `hosts` file                   |
| ffc not working                           | Run `ffm ffc mybench` to regenerate API keys                                  |
| Need to update tools in image             | `ffm restart mybench --rebuild`                                               |
| Prod site not responding                  | Check `ffm logs myprod frappe` and `ffm status myprod`                        |
| Let's Encrypt cert failing                | Ensure DNS points to server, port 80 is open, domain is public                |
| Already have Caddy/Nginx on 80/443        | Use `--no-ssl`; proxy the allocated web/socketio ports from Caddy             |
| Prod site shows wrong URL / broken assets | Run `ffm set-proxy <name> --host <domain>` then `ffm restart <name>`          |
| "No module named frappe"                  | The venv path patching may have failed; recreate the bench                    |
| Container won't start                     | Check `ffm logs mybench` and `ffm status mybench`                             |
