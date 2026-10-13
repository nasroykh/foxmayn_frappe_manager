<div align="center">
  <img width="150" height="150" alt="logo-foxmayn" src="https://github.com/user-attachments/assets/fa9f3727-dd5c-4748-92e9-f527a740366a" />
</div>

# ffm — Foxmayn Frappe Manager

A Go CLI that wraps Docker Compose to create, manage, and destroy Frappe benches with a single command. Supports both **development** and **production** modes. No YAML to write, no Docker flags to memorize.

## Requirements

- [Docker](https://docs.docker.com/get-docker/) with the Compose plugin (`docker compose`)
- Go 1.26.1+ (only needed to build from source)

## Installation

### Linux / macOS — one-liner

```bash
curl -fsSL https://raw.githubusercontent.com/nasroykh/foxmayn_frappe_manager/main/install.sh | sh
```

Detects OS and architecture, downloads the latest release binary, verifies the release signature (with OpenSSL 3; SHA-256 only without it) and the SHA-256 checksum, and installs to `/usr/local/bin` (or `~/.local/bin` if the former is not writable).

### Windows — PowerShell one-liner

```powershell
powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/nasroykh/foxmayn_frappe_manager/main/install.ps1 | iex"
```

Installs to `%LOCALAPPDATA%\Programs\ffm` and adds it to your user `PATH` automatically. No admin rights required.

### Debian / Ubuntu / Fedora / RHEL packages

Each release also ships `.deb` and `.rpm` packages for amd64 and arm64:

```bash
curl -fsSLO https://github.com/nasroykh/foxmayn_frappe_manager/releases/latest/download/ffm_<version>_linux_amd64.deb
sudo apt install ./ffm_<version>_linux_amd64.deb      # or: sudo dnf install ./ffm_<version>_linux_amd64.rpm
```

A package-installed ffm is upgraded with the package manager; `ffm update` refuses to replace it.

### Verifying a download

`checksums.txt` is signed (`checksums.txt.sig`, checked by `install.sh` and `ffm update`), every archive and package has an SPDX SBOM (`*.sbom.json`), and every file has a GitHub build-provenance attestation:

```bash
gh attestation verify ffm_<version>_linux_amd64.tar.gz -R nasroykh/foxmayn_frappe_manager
```

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

## Platforms

| Platform | Status | Notes |
|--|--|--|
| Linux amd64 / arm64 | supported | release binaries, `.deb`, `.rpm`; the `arm64-smoke` workflow creates a dev bench on a native arm64 runner every month |
| macOS (Intel, Apple silicon) | supported | Docker Desktop; scheduled backups use launchd |
| Windows | supported | Docker Desktop; scheduled backups use Task Scheduler; no seeds yet |
| WSL2 | supported | see below |

**Rootless Docker.** ffm mounts the daemon's own socket into the proxy when `docker info` reports a
rootless daemon (taken from `DOCKER_HOST`, else the current docker context), and fails before
`docker run` when `net.ipv4.ip_unprivileged_port_start` is above 80, because the proxy publishes
port 80 (and 443 for a prod bench with SSL). To allow it:

```bash
sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80
echo 'net.ipv4.ip_unprivileged_port_start=80' | sudo tee /etc/sysctl.d/99-ffm-rootless.conf
```

With a rootful daemon the proxy always mounts `/var/run/docker.sock`, whatever the context says
(Docker Desktop's context socket cannot be bind-mounted).

### WSL2

- **Docker.** Docker Engine installed natively inside the distribution works; so does Docker Desktop's
  WSL integration. ffm needs `docker compose` on the PATH either way.
- **Scheduled backups need a scheduler that runs.** A WSL2 distribution without systemd starts no cron
  daemon, so a crontab line never fires. Set `systemd=true` under `[boot]` in `/etc/wsl.conf` and run
  `wsl --shutdown`, or start cron by hand (`sudo service cron start`). `ffm backup scheduler install`
  picks cron only when `crontab` exists and a cron daemon is running, and otherwise a systemd user
  timer; with neither it stops with that explanation. A systemd user timer stops when your last
  session ends unless linger is on — ffm warns and prints `sudo loginctl enable-linger $USER`, and
  never runs it.
- **Install location.** `install.sh` puts ffm in `~/.local/bin` when `/usr/local/bin` is not writable,
  `go install` puts it in `~/go/bin`. Make sure the one you want comes first on the PATH, or an old
  copy shadows the new one (`which -a ffm`).
- **Stopping the dev server by hand.** ffm stops `bench start` with `pkill -f '[h]oncho start'`. The
  brackets matter: inside a `bash -c` the plain pattern also matches the shell itself and kills it.
- **Traefik lost its network or ports** after a WSL or Docker restart: check
  `docker inspect ffm-proxy --format '{{json .NetworkSettings.Networks}} {{json .HostConfig.PortBindings}}'`,
  then `ffm proxy start`: it recreates a proxy that runs without its network or published ports, or that
  still runs an older Traefik image (benches keep their routing labels; the Let's Encrypt volume is
  kept, and an HTTPS proxy comes back with the same ACME email). `ffm proxy status` and `ffm doctor`
  report such a proxy as broken.
- **Keep project checkouts off `/mnt/<drive>`.** `ffm up` warns when the mounted checkout is on a Windows
  drive: the 9p mount is slow, Windows-side edits may not trigger reloads, and file modes are ignored.
  Clone into the Linux filesystem.

## Modes

| | Development (`--mode dev`) | Production (`--mode prod`) |
|--|--|--|
| **Purpose** | Local dev with Claude Code, ffc, hot-reload | VPS deployment |
| **Containers** | 4 (frappe + db + redis×2) | 8 (gunicorn + socketio + worker-long + worker-short + scheduler + db + redis×2) |
| **Image** | Full dev tools (zsh, starship, ffc, pnpm, Claude Code) | Minimal (no dev tools) |
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

## Web dashboard

Optional browser UI for managing benches (same operations as the CLI), modeled after the [kbls admin console](https://github.com/KB-Developpement/kb_pro_license_server).

```bash
# Foreground (blocks; Ctrl+C to stop)
ffm dashboard start --admin-password 'choose-a-strong-password'

# Background daemon
ffm dashboard start -d --admin-password 'choose-a-strong-password'

# Management
ffm dashboard status
ffm dashboard stop
ffm dashboard logs -f
```

- Default URL: **http://127.0.0.1:8787/admin** (HTTP Basic auth; any user name, the admin password)
- Every long action (`create`, `recreate`, start, stop, restart, delete, ffc, set-proxy, tunnel on/off,
  clean-logs, proxy start/stop) runs as a **background job**: the click redirects to the job page and
  its output streams live, including the stderr a failed image build prints. Job output keeps the
  last 5000 lines and has the bench's passwords masked. A bench (or the proxy) runs one job at a time;
  a second action on it is refused with a message. Quick edits (tunnel servers) stay synchronous.
- Pages beyond start/stop: the bench page has **Restart** with *Rebuild* and *Fresh* (no layer cache,
  re-pulled base image) options; the **create form** has every `ffm create` option except
  `--keep-on-failure` and `--agent-read-only` (custom Frappe repo, Python/Node, fixed ports,
  domain aliases, LAN, SSH agent, match host user, agent, no seed) and the pipeline's own checks fail
  the job with the CLI's message; **Backups** (`/admin/backups`: archives per bench, deleted benches
  included; back up now; schedule; prune with a dry-run preview you confirm); **Restore**
  (`/admin/restore`); per bench **Domains**, **Update apps** (dry-run preview, then run),
  **Snapshots** (create, restore, delete), **Run doctor** and **Clone**. Everything that touches
  Docker is a job; listing pages and snapshot delete are synchronous. Doctor is a job because its
  probes can take tens of seconds and take no context to cancel.
- Restore only offers archives found under the backups directory (`ffm backup list` shows the same
  set), always into a **new** bench name; there is no upload or free path. The admin password and
  private-app token options stay CLI-only (`ffm restore`), as do encryption keys and upload targets.
- The backup schedule page saves the policy only, like `ffm backup schedule --no-install`: the dashboard
  never installs or removes the hourly system job. It shows whether the job is installed and
  tells you to run `ffm backup scheduler install` when it is not. An app update from the dashboard
  does not run the project's `ffm.yaml` post-update hooks (they need the trust prompt).
- The bench list and home page read every bench's status with one `docker ps` (cached for 2 s).
- Interactive shell: use `ffm shell <name>` in the terminal; the dashboard has no shell
- The UI loads nothing from another origin (no CDN, no web fonts) and sets a strict
  Content-Security-Policy, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: no-referrer`
  and, over TLS, HSTS. Cross-site POSTs are refused before any handler runs.
- Passwords are not in the bench page. **Reveal** and **Copy** fetch them with an audited POST.
- Wrong passwords are throttled per client address: 5 failures in a minute lock that address
  out (`429` with `Retry-After`), starting at 30 s and doubling to 15 min; 30 failures
  from all addresses in a minute lock everyone out until the window drains.
- Every dashboard POST (and each password reveal) is appended to
  `~/.config/ffm/dashboard-audit.jsonl` (mode `0600`): time, client address, action, bench,
  result, and for exec the service and the command, verbatim. The file is not rotated.

### Breaking changes (since the unauthenticated-LAN defaults)

- **A non-loopback `--listen` is refused** unless the dashboard serves TLS itself or you opt
  out. This applies to the saved setting and to the background daemon too, so an old
  `dashboard.json` with `0.0.0.0:8787` stops starting until you choose one:

  ```bash
  # HTTPS from a certificate you already have
  ffm dashboard start -d --listen 0.0.0.0:8787 --tls-cert cert.pem --tls-key key.pem --admin-password '…'

  # Plain HTTP on the network: the password and session cookie cross it in clear
  ffm dashboard start -d --listen 0.0.0.0:8787 --insecure-http --admin-password '…'
  ```

  `--tls-cert`, `--tls-key` and `--insecure-http` are saved in `dashboard.json`
  (pass `--insecure-http=false` to clear it).
- **The exec page is off by default.** It runs a command in a bench container, which is remote
  code execution behind one shared password. Without `--allow-exec` the route answers `404`
  and the link is hidden; with it, the service must be one of the bench's compose services.
  Saved like the other flags (`--allow-exec=false` turns it off again).
- **Behind a reverse proxy, every visitor shares the proxy's address**, so one person
  typing the wrong password five times locks the dashboard out for everyone for 30 s or more.
  ffm keys the limiter on the TCP peer only: `X-Forwarded-For` is attacker-controlled, so it is
  not trusted. Prefer ffm's own TLS, or restrict who can reach the proxy.
- The dashboard's CSS is a committed `tailwind.css`. After editing templates
  run `make dashboard-css` (pinned, sha256-verified Tailwind CLI) and commit the result; CI fails on a diff.

Settings are stored in `~/.config/ffm/dashboard.json` (mode `0600` when a password is set).

## Commands

### `ffm create <name>`

Creates and starts a new Frappe bench end-to-end. When run interactively (no flags), a form asks for mode first, then the relevant options.

#### Dev mode (default)

Steps performed:

1. Allocates a free host port pair (web: 8000+, socketio: 9000+)
2. Writes `docker-compose.yml` and `Dockerfile` to `~/frappe/<name>/`
3. Builds the Docker image — installs **zsh**, **zinit**, **starship**, **[ffc](https://github.com/nasroykh/foxmayn_frappe_cli)**, **pnpm**, and **Claude Code**; pre-fetches 60 [Frappe Claude skills](https://github.com/OpenAEC-Foundation/Frappe_Claude_Skill_Package) to `/opt/`. **Cached after first build.**
4. Runs `bench init --python <version>` — clones Frappe, installs Python/Node deps with the branch's toolchain (see [Frappe versions](#frappe-versions-and-toolchains)), copies skills into `frappe-bench/.agents/skills/` and `.claude/skills/`
5. Starts 5 containers (database, 2× Redis, frappe, Mailpit) with `workspace/` bind-mounted at `/workspace`
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
  --frappe-branch string  Frappe branch to initialise (default "version-16")
  --python string         Python for the virtualenv: 3.12 or 3.14
                          (default: 3.12 for version-15, 3.14 otherwise)
  --node string           Node major: 22 or 24 (default: 22 for version-15, 24 otherwise)
  --frappe-repo string    Custom Frappe repo URL with optional @branch suffix
                          (e.g. https://github.com/your-org/frappe.git@main).
                          Defaults to the official frappe/frappe repo.
  --apps stringArray      Apps to install (see formats below)
  --admin-password string Frappe site admin password (default "admin"; required for prod)
  --db-type string        Database engine: mariadb or postgres (default "mariadb")
  --db-password string    Database root password (default "ffm123456"; prod generates
                          a random one when the default is left)
  --github-token string   GitHub PAT for private HTTPS repos
                          (prefer env FFM_GITHUB_TOKEN / GITHUB_TOKEN; likewise
                          FFM_ADMIN_PASSWORD and FFM_DB_PASSWORD for the two passwords)
  --show-secrets          Print the passwords at the end even when stdout is not a terminal
  --proxy-port int        Dev reverse proxy: set socketio_port (e.g. 443 or 80)
  --proxy-host string     Dev reverse proxy: set per-site host_name
  --web-port int          Fixed host web port instead of auto-allocating from 8000
                          (must be paired with --socketio-port)
  --socketio-port int     Fixed host Socket.IO port (must equal --web-port + 1000)
  --domain-alias stringArray
                          Extra hostname the bench also answers on (repeatable; needs
                          the shared proxy and DNS; same as 'ffm domain add')
  --alias-tls             Serve --domain-alias names over HTTPS with Let's Encrypt
                          (prod with SSL only)
  --match-host-user       Remap the container's frappe user to your uid/gid
                          (needed when your uid is not 1000; env FFM_MATCH_HOST_USER)
  --keep-on-failure       Leave containers and the bench directory in place on failure
                          instead of rolling back (env FFM_KEEP_ON_FAILURE)
  --lan                   Publish the bench's ports on all interfaces instead of
                          127.0.0.1 (needed from another machine and for dev
                          --domain-alias; refused with the default admin password)
  --ssh-agent             Forward the host SSH agent into the dev container (opt-in)
  --verbose               Stream full Docker and bench init output

Production tuning (prod only unless noted):
  --mariadb-buffer-pool string  InnoDB buffer pool size (default "1G"; dev uses 256M)
  --gunicorn-workers int        Gunicorn worker processes (default 2; rule of thumb 2*CPU+1)
  --worker-long-replicas int    Long-queue worker replicas (default 1)
  --worker-short-replicas int   Short-queue worker replicas (default 1)
  --redis-cache-maxmem string   Redis cache maxmemory, allkeys-lru eviction (default "512mb")
  --redis-queue-maxmem string   Redis queue maxmemory, noeviction (default "512mb")
  --slow-query-log              MariaDB slow query log, 2s threshold, written to
                                <bench>/mysql-logs/ (prod + MariaDB only)
  --mariadb-fast-commit         innodb_flush_log_at_trx_commit=2: faster commits, a crash can
                                lose up to about a second of them (default 1: none)
```

#### Frappe versions and toolchains

Each bench gets the Python and Node its Frappe branch is built for. Both come with the pinned
`frappe/bench` image, so nothing extra is downloaded.

| Branch | Python | Node | Status |
| --- | --- | --- | --- |
| `version-16` (default) | 3.14 | 24 | Supported. Frappe requires Python 3.14 and Node 24 |
| `version-15` | 3.12 | 22 | Supported. Frappe allows Python 3.10–3.14 and Node 18+ |
| `develop`, fork branches | 3.14 | 24 | Best effort; override with `--python` / `--node` |
| `version-14` | — | — | Not supported (end of life 2026-01-31; needs Python 3.10/3.11, which the image lacks) |

PostgreSQL stays experimental, as Frappe itself labels it.

A PostgreSQL bench's image carries the PostgreSQL 18 client (`pg_dump`, `psql`), installed from the
official PGDG apt repository: the `frappe/bench` base image ships an older one, and `pg_dump`
refuses to dump a newer server, which broke backup, snapshot, clone, update and delete's pre-backup.
A postgres bench created before ffm added this needs `ffm restart <bench> --rebuild` once.

`ffm status` shows a bench's toolchain. Benches created before ffm v0.11.0 keep running on the
image defaults (Python 3.14 and Node 24 for every branch); `ffm recreate` rebuilds them with the
branch's toolchain. uv's package cache lives in the `pip-cache` volume, so a replaced container
does not download Python packages again (`ffm reconcile` sets this up on existing dev benches).

#### Seeds: the second bench is fast

`bench init`, `bench get-app` and `bench build` take most of a create. At the end of a create
that ran them, ffm saves the bench tree (apps, virtualenv, node_modules, built assets; no site,
logs or skills) as a **seed** in `~/.cache/ffm/seeds/` (`FFM_SEEDS_DIR` overrides). The next
create with the same image, Frappe repo and branch, Python, Node, uid and `--apps` copies the seed
instead (`cp` with block cloning where the filesystem has it) and only creates and installs the
site. Restore, recreate and clone go through create, so they use seeds as well, but only a plain
create saves one.

- A seed freezes each app at the commit it was taken at and is used for 7 days; after that, the
  next create runs bench init again and replaces it. `--no-seed` (or `FFM_NO_SEED=1`) always
  starts from the branch heads and saves nothing; `ffm app update` pulls a bench to the heads later.
- A frappe + erpnext seed is about 1.4 GB. `ffm clean --seeds` lists and removes them.
- The copy is near-instant where the filesystem clones blocks (APFS, Btrfs, XFS). On ext4 it is
  a real copy: 1–2 minutes for that seed on a busy test host. That still beats bench init plus
  get-app plus bench build for erpnext, but by less. A restore (and so `ffm clone`) always runs
  migrate and a build after loading the data, so there the seed saves only bench init and get-app.
- Not on Windows yet.

#### `--apps` formats

```bash
ffm create mybench --apps erpnext --apps hrms
ffm create mybench --apps erpnext@version-16
ffm create mybench --apps git@github.com:myorg/myapp.git
ffm create mybench --apps "git@github.com:myorg/myapp.git@main"
ffm create mybench --apps https://github.com/myorg/myapp
ffm create mybench --apps "https://github.com/myorg/myapp@develop" --github-token ghp_xxx
```

Pass `--ssh-agent` to forward your SSH agent into the container, so SSH-URL private repos work without a token (dev only; `SSH_AUTH_SOCK` must be set). Forwarding is opt-in: it hands your SSH keys to everything running in the bench, including Claude Code. Benches created before v0.8.1 forwarded the agent automatically; `ffm reconcile <bench> --ssh-agent` keeps it for them.

#### Using a custom or forked Frappe repo

By default `bench init` clones the official `frappe/frappe`. Use `--frappe-repo` to point at a fork or mirror, with an optional `@branch` suffix that overrides `--frappe-branch` for the Frappe checkout (apps still use `--frappe-branch`). For a private repo, pass `--github-token` — the credentials are injected into the `bench init` container.

```bash
# Public fork on a custom branch
ffm create mybench --frappe-repo https://github.com/your-org/frappe.git@main

# Private fork (HTTPS) with a token
ffm create mybench \
  --frappe-repo https://github.com/your-org/frappe.git@main \
  --github-token ghp_xxx

# Private fork over SSH (uses the forwarded SSH agent, no token needed)
ffm create mybench --ssh-agent --frappe-repo "git@github.com:your-org/frappe.git@main"
```

Both `--frappe-repo` and `--github-token` are also available in the interactive `ffm create` form (the token field is masked).

### `ffm up` — the bench a project describes (`ffm.yaml`)

Commit an `ffm.yaml` in an app's repository and anyone can bring up the same development bench:

```yaml
version: 1
name: shop                       # bench name; default: the directory's name
frappe:
  branch: version-16             # default version-16
  repo: https://github.com/frappe/frappe   # optional fork
python: "3.14"                   # optional; default per branch
db: mariadb                      # or postgres
apps: [erpnext, hrms@version-16]
hooks:                           # inside the bench, from /workspace/frappe-bench
  post_create: ["bench --site $SITE set-config developer_mode 1"]
  post_update: ["bench --site $SITE clear-cache"]
tooling:                         # ffm run <name> [args...]
  lint: ruff check apps/shop
  tests: { cmd: "bench --site $SITE run-tests --app shop", description: App tests }
```

```bash
ffm up                # create the bench (or start it); post_create hooks run after a create
ffm run               # list the tooling; ffm run lint --fix runs one with extra arguments
ffm trust             # review and allow the file's commands; --revoke forgets it
```

- `ffm up` looks for `ffm.yaml` in the current directory and its parents up to the repository root.
  Unknown fields are errors. An existing bench is started, not changed: apps the file lists and
  the bench lacks are reported for `ffm app add`.
- **Hooks and tooling run only from a file you trusted in its exact current form**, like direnv.
  Any edit, including one pulled from git, asks again (`ffm up` asks interactively; scripts use
  `ffm trust --yes` after review, or `ffm up --no-hooks`). The record is `~/.config/ffm/trust.json`.
- `post_update` hooks run after a successful `ffm app update` of the bench.
- `$SITE` and `$BENCH` are set for hooks and tooling.

### `ffm worktree` — a bench per git worktree

For a project bench (made by `ffm up` inside an app repository):

```bash
ffm worktree add feat/login            # clone the bench, add a git worktree, mount it
ffm worktree list --json               # ffm.worktrees/v1
ffm worktree remove feat/login --delete-branch
ffm worktree hook print                # Claude Code WorktreeCreate/WorktreeRemove hooks
```

`add` backs the source up and restores it as `<source>-<branch-slug>` (own database and ports), adds
the worktree at `<repo-parent>/<repo>.worktrees/<slug>` (`--path` overrides) and mounts it as the app.
The bench answers on `<slug>.<source>.localhost` as well as its own host. Untracked files matching
the repository's `.worktreeinclude` are copied in. Your checkout is never deleted or reset by ffm:
`remove` runs `git worktree remove` without `--force` (git refuses uncommitted or untracked files;
pass `--force` to discard them), and deletes a branch only with `git branch -d`, only if ffm created
it and only with `--delete-branch`. If `add` fails after the worktree exists, the bench is removed,
the worktree is kept and the commands that remove it are printed. Claude Code does not process
`.worktreeinclude` when a WorktreeCreate hook exists; ffm's hook copies those files itself.

### `ffm list` / `ffm ls`

Lists all managed benches with their live status, mode (dev/prod), DB engine (maria/pg), port, domain URL, and Frappe branch.
The status is `running` when the bench's frappe container runs, `partial` when only other containers
do (the bench serves nothing: `ffm restart <bench>`, and `ffm logs <bench> frappe` for the cause),
`stopped`, or `unknown` when Docker cannot be asked.

### `ffm status [name]`

Shows per-container status, credentials, ports, and URLs. Prod benches show the domain URL instead of `localhost`.

### `ffm app` — add, remove, list and update apps

```bash
ffm app list mybench [--json]                   # version, branch, commit, installed on the site (ffm.apps/v1)
ffm app add hrms mybench                        # get-app, install-app, build --app, restart
ffm app add https://github.com/org/my_app@main mybench [--github-token …] [--no-install]
ffm app remove hrms mybench [--keep-code]       # snapshot first, then uninstall-app (data goes) and remove-app
ffm app update mybench --dry-run                # the apps, branches, commits and the plan
ffm app update mybench [--apps erpnext]         # pull, requirements, migrate, build — rolled back on failure
ffm app update mybench --to-branch version-16   # major upgrade, with the new branch's Python and Node
```

`ffm app update` runs one locked pipeline:

1. Refuse uncommitted changes in any app (ffm's own frappe patches excepted), and apps whose
   HEAD is not their upstream branch (local or pinned commits: the pull resets onto upstream).
   Apps without an upstream remote (e.g. made with `bench new-app`) are left as they are.
2. Back the site's database up and take a snapshot.
3. Turn maintenance mode on and pause the scheduler.
4. Pull (`bench update --pull --reset`, which keeps ffm's shallow clones shallow), install
   requirements, clear the cache, migrate and build, then re-apply ffm's realtime patches.
5. Restart, check `/api/method/ping`, turn maintenance off and resume the scheduler.

If a step after 3 fails, ffm rolls back. Every app goes back to its previous commit and branch,
the toolchain to its previous version, the requirements are reinstalled, the snapshot is
restored and the assets are rebuilt. `--no-rollback` leaves the bench in maintenance mode for
inspection instead.

`--to-branch` moves frappe and every app on the bench's branch to the new one, with a shallow
fetch of that branch rather than `bench switch-to-branch` (which unshallows every app and installs
the new requirements into the old virtualenv). It rebuilds the image for the new Node and a new
virtualenv for the new Python (version-16: 3.14 and 24; the old one is kept until the update
succeeds, so a rollback puts it back as it was), then migrates. Apps on other branches stay on theirs. `ffm update` (no `app`) still updates ffm itself.

### `ffm site` — migrate, maintenance mode, Frappe's scheduler

```bash
ffm site mybench [--json]                      # maintenance mode and scheduler state (ffm.site/v1)
ffm site migrate mybench                       # bench migrate, output streamed
ffm site maintenance on|off mybench            # visitors get a maintenance page
ffm site scheduler on|off|pause|resume mybench # Frappe's scheduled jobs
```

`on`/`off` change the System Settings flag; `pause`/`resume` stop jobs for a maintenance window
without changing it. This is Frappe's scheduler, not `ffm backup scheduler` (ffm's own backups).

### `ffm doctor [name]`

Checks that benches are healthy, not just running, and changes nothing:

- **containers:** frappe running, restart counts;
- **site:** `/api/method/ping` on the web port, and through Traefik for dev;
- **database:** the site connects;
- **scheduler:** enabled. A disabled scheduler fails prod and warns on dev;
- **workers:** every RQ queue has a worker listening. Jobs on a queue nobody consumes wait forever
  while the site looks fine;
- **certificate:** for prod with Let's Encrypt, valid for the domain, warn under 14 days, fail
  under 3;
- **backups:** the schedule produced an archive within its interval plus two hours;
- **disk:** free space for the bench and the backups, warn under 10 GiB, fail under 2 GiB;
- **templates:** built from this ffm's templates.

```bash
ffm doctor                 # every bench; exit code 1 when any check fails
ffm doctor mybench --json  # ffm.doctor/v1
ffm doctor --notify        # failures through 'ffm notify', e.g. from cron
```

### `ffm start [name]`

Starts a stopped bench. Dev: also reinstalls skills if missing and relaunches `bench start`. Prod: `docker compose up -d` only (services run via compose `command:`).

### `ffm stop [name]`

Stops all containers. Data is preserved.

### `ffm restart [name]`

Stops then starts a bench in one step.

```
Flags:
  --rebuild   Rebuild the Docker image before starting
  --fresh     Rebuild ignoring the build cache and re-pulling the base image (implies --rebuild)
```

`--rebuild` rewrites the `Dockerfile` from the current template (mode-aware) and runs `docker compose build`. Useful after an ffm upgrade changes the template. Docker's layer cache keeps unpinned tools installed during the build (Claude Code, starship) at the version of the first build; `--fresh` updates them too. ffc is pinned, so a plain `--rebuild` moves it when ffm's pin changes.

The images a bench is built from are pinned in each ffm release: the `frappe/bench` base image, Redis 8, Traefik 3.7 (used when the shared proxy is created), ffc (binary and skills) and the Frappe skill pack. MariaDB (11.8) and PostgreSQL (18) follow their minor or major tag.

### `ffm shell [name]`

Opens an interactive shell inside the `frappe` container:
- **Dev**: `zsh` with zinit and starship
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

### `ffm open [name]`, `ffm mail [name]`, `ffm login [name]`

Open the bench in the browser. Without a desktop session (SSH, a headless server) the URL is
printed instead; `--print` always prints it and `--json` prints `{"schema": "ffm.url/v1", "url": …}`.

```bash
ffm open mybench              # the site
ffm open mybench --mail       # the Mailpit inbox (same as 'ffm mail mybench')
ffm open --traefik            # the shared proxy's dashboard
ffm login mybench             # the desk, already logged in as Administrator (dev only)
ffm login mybench --user jane@example.com --print
```

**Mail (dev).** Every dev bench runs [Mailpit](https://mailpit.axllent.org/). The site's
`site_config.json` points `mail_server` at it, so everything the bench sends lands in the inbox
on the web port + 6 (`http://localhost:8006` for the first bench) instead of reaching anyone.
A default outgoing Email Account configured in the site still wins, and ffm never replaces a
`mail_server` you set yourself. Benches created before v0.11.0 get Mailpit from `ffm reconcile`.

**Login.** `ffm login` starts a session and opens `/app?sid=…`. The URL works as a password until
the session ends, so it is printed only with `--print` or when nothing can be opened. Dev benches
only.

### `ffm console [name]` and `ffm db [name]`

```bash
ffm console mybench                         # bench --site <site> console (IPython, frappe loaded)
ffm db mybench                              # bench --site <site> db-console (mariadb or psql)
ffm db mybench --export dump.sql.gz         # .gz as Frappe writes it; any other name gets plain SQL
ffm db mybench --import dump.sql.gz         # asks first; --yes skips the question (bench name required)
ffm db mybench --import other.sql.gz --migrate --yes
```

`--import` goes through `bench restore`, which drops and recreates the site's database. Files and
`site_config.json` stay: a dump from another site keeps this site's encryption key, so its Password
fields will not decrypt. The root password reaches the container on stdin, never on a host command
line. An export holds every password hash and API secret of the site and is written 0600.

### `ffm test <app> [bench]`

Runs `bench --site <site> run-tests --app <app>` on a dev bench and streams the output; the exit
code is non-zero when a test fails. The tests run on the bench's own site and create and delete
records there. The first run sets `allow_tests` in its `site_config.json`. Production benches are
refused.

```bash
ffm test erpnext mybench --doctype "Sales Invoice"
ffm test myapp --module myapp.myapp.doctype.thing.test_thing --test test_create --failfast
ffm test myapp mybench --junit report.xml       # JUnit XML for CI
```

### `ffm debug on|off [bench]`

`ffm debug on` installs debugpy into the bench's virtualenv, runs the web process under it
(without auto-reload) and writes `.vscode/launch.json` when the bench has none. Attach VS Code on
`localhost:<web port + 5>` ("ffm: attach (host)"), or on port 8005 from a devcontainer window
("ffm: attach (in container)"). `ffm debug off` restores the normal server; bare `ffm debug` shows
the state. Refused on a bench whose ports are published on every interface (`--lan`): debugpy runs
any code it is sent.

### `ffm poweroff`

Stops every running bench and the shared proxy (`--keep-proxy` leaves the proxy up).

### `ffm clean`

Lists, then removes after confirmation, the Docker volumes and images of ffm benches that no longer
exist: no record, no bench directory, no container (running or stopped) and no ffm operation in
progress. A bench tracked under another
`FFM_CONFIG_DIR` looks gone from here, so read the list.

```bash
ffm clean --dry-run                 # list only (--json: ffm.clean/v1)
ffm clean --yes                     # remove without asking
ffm clean --build-cache --dangling  # also Docker's build cache and untagged images (shared with
                                    # every other project on the host)
ffm clean --seeds                   # also the bench seeds (the next create saves a new one)
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
ffm proxy start    # start Traefik (recreates a broken or outdated container)
ffm proxy stop     # stop Traefik
ffm proxy status   # show status + dashboard URL
```

- **Dev benches**: routes `<name>.localhost` on port 80
- **Prod benches with SSL**: also routes on port 443 with Let's Encrypt (added on first prod bench creation)

Prod benches (template version 4) add these on their site routers:

- HSTS (one year, without `includeSubDomains`, so plain-HTTP aliases keep working);
- `nosniff`, `X-Frame-Options: SAMEORIGIN` and a referrer policy;
- compression (not on socket.io).

Socket.IO now receives the browser's own `Origin`, which Frappe checks against the host. Before
v0.13.0 ffm pinned it on the primary router and stripped it on aliases, which let any web page
open a socket with a logged-in user's cookies.

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

Mints Frappe API keys (for Administrator, or for the agent user on an agent-ready bench) and
configures ffc with `ffc site add --api-secret-stdin`, which replaces only the bench's own site
and keeps any others you added. It also rewrites `.mcp.json` and the generated `AGENTS.md`. Dev
benches only; refused on prod, where ffc is not installed. Run it if ffc setup failed during
`ffm create`, or to rotate the keys.

`ffm ffc [name] --host` is for AI clients on your machine (Cursor, Claude Desktop, VS Code): it
adds the bench to the ffc installed on the **host**, reusing the keys the bench already holds
(nothing is regenerated, so the in-container ffc keeps working). The secret reaches ffc on stdin.
The bench must be running; `--force` replaces a host site of the same name. Then run
`ffc mcp --site <bench>` or `ffc mcp install --client claude-code|claude-desktop|cursor|vscode|codex --site <bench>`.
A plain `ffm ffc` mints new keys, so repeat `ffm ffc <bench> --host --force` after it.

The dev image pins ffc to one release (`bench.FfcRef`) for the binary and its skills;
`ffm status` shows the version running in the container and flags a mismatch
(`ffm restart --rebuild` moves it).

ffc's config and Claude Code's login (`CLAUDE_CONFIG_DIR`) live in the bench directory
(`home/ffc`, `home/claude`, both 0700) and survive the container being replaced. When `ffm start`
finds no ffc config there, it sets ffc up again.

### Agent-ready benches: `ffm create --agent`, `ffm agent on|off`

For running a coding agent (Claude Code, Codex…) unattended inside a dev bench:

```bash
ffm create mybench --agent                  # or --agent-read-only
ffm agent on mybench [--read-only]          # an existing bench; 'ffm agent off', bare 'ffm agent' shows it
```

- Ports stay on `127.0.0.1` (`--lan` is refused) and your SSH agent is not forwarded.
- A default `admin` password is replaced with a random one (`ffm status <bench> --show-secrets`).
- ffc and the `frappe` MCP server act as `agent@<site>`, a System Manager user ffm creates, not as
  Administrator. Its changes are attributed to it, and `ffm agent off` disables it and its keys.
  System Manager can still change almost all data: this is attribution and revocability, not a
  sandbox. `--read-only` limits the MCP server to read tools (`ffc mcp --read-only`); ffc on the
  command line keeps the user's full rights.
- Every dev bench gets an `AGENTS.md` (plus a `CLAUDE.md` that imports it) at the bench root, with
  its URLs, the identity the agent uses, how to run tests, Mailpit and the snapshot commands.
  ffm rewrites it while its first line is the "Generated by ffm" marker; delete that line to own
  the file.
- Not included yet: an egress firewall for agents run with permission prompts off.

### `ffm mcp` — manage benches from an agent on the host

`ffm mcp` is an MCP server on stdin/stdout for an agent running on the host (the agent inside a
bench uses ffc's MCP server instead, which manages one site's data):

```bash
ffm mcp install --client claude-code                  # read-only; or claude-desktop, cursor, vscode, codex
ffm mcp install --client cursor --allow-write --print # show the change, write nothing
ffm mcp uninstall --client cursor
```

`install` writes the client's user-level config (Claude Code through `claude mcp add-json`), keeps
comments and other servers, shows a diff and asks first, and backs the old file up as
`<file>.ffm-<time>.bak`. The entry runs this ffm binary by its absolute path; `FFM_*` variables
of your shell are not copied into it.

| Tools | Offered |
|--|--|
| `list`, `status`, `doctor`, `logs`, `snapshots` | always (read-only) |
| `create` (dev benches only), `clone`, `snapshot`, `snapshot_restore`, `test`, `start`, `stop` | with `--allow-write` |
| `delete` | with `--allow-write`; the client asks the user every time |

- Results are the same JSON documents as `--json` (`ffm.list/v1`, `ffm.status/v1`, …); passwords
  are left out and logs have them scrubbed. `logs` returns at most 2000 lines.
- `delete` asks through MCP elicitation, bound to that call and usable once. A client that cannot
  ask gets a refusal naming the `ffm delete` command to run in a terminal.
- Write tools refuse production benches unless the server runs with `--allow-prod`; clone never
  copies one (it needs a domain).
- `create` and `clone` take minutes and send progress notifications when the client asks for
  them; a client with a short tool timeout may give up first while the work goes on.

### `ffm backup [name]`

Writes the bench's database, file attachments and configuration into one portable archive.

```bash
ffm backup                                  # the bench in the current directory
ffm backup mybench
ffm backup mybench --out ~/archives
ffm backup mybench --no-files --label "before the v16 upgrade"
```

```
Flags:  --out <path>          Directory to write into, or an explicit path ending in .tar
        --label <text>        Short note recorded in the archive
        --no-files            Database only, no attachments
        --skip-space-check    Do not check free disk space first
        --vendor-apps <apps>  Also archive these apps' source (comma-separated, or all)
        --encrypt             Encrypt the archive with age (see below)
```

The archive holds Frappe's own database dump, the site's public and private files, the site
and bench configuration, and each installed app's git remote, branch and commit. It does
**not** hold the Python virtualenv or the built assets, nor the source of any app that can be
cloned back — those are rebuilt at restore time, which is what keeps a full ERPNext bench's
backup under a megabyte instead of ~2 GB, and what lets it restore onto a different machine,
architecture or host user.

An app that **cannot** be cloned back has its source archived instead: one that is not a git
repository (`bench new-app --no-git`, or copied in by hand), one with no remote another
machine can reach, and one whose commit was never pushed. `node_modules` and built assets are
left out. `--vendor-apps` archives other apps too — for example to keep uncommitted changes.

A stopped bench is started for the backup and stopped again afterwards.

> The archive contains the database root password, the Administrator password and the site's
> encryption key in plain text. It is written `0600` inside a `0700` directory. ffm warns when
> the filesystem cannot enforce that — notably a Windows drive mounted into WSL2.

#### Encrypted backups (age)

```bash
ffm backup key init                      # prints the identity ONCE; ffm keeps only the public key
ffm backup key init --out ~/ffm-backup.key   # or write it to a new 0600 file, then move it off the host
ffm backup key add age1…                 # also encrypt to another key, e.g. an offline one
ffm backup mybench --encrypt             # <archive>.ffm.tar.age + a cleartext <archive>.age.header.json
ffm backup schedule mybench --encrypt    # every scheduled archive
ffm restore mybench_….ffm.tar.age copy --identity ~/ffm-backup.key   # or FFM_AGE_IDENTITY_FILE
```

The whole archive is encrypted with [age](https://age-encryption.org) to the public keys in
`~/.config/ffm/backup-recipients.txt`, and the plaintext is deleted once encryption has succeeded.
age authenticates the data, so a modified or truncated archive fails to decrypt instead of
restoring something else. The header sidecar (bench, date, trigger, contents; no secrets) lets
`backup list` and pruning work without the key. **ffm never stores the identity**: keep it in a
password manager or offline. Without it, no encrypted backup can be restored. Local archives
stay unencrypted unless you ask (`--encrypt`); uploads to targets are always encrypted.

#### Off-host copies: backup targets

A dead disk or a lost host takes local archives with it. Targets keep copies elsewhere:

```bash
echo "$SECRET" | ffm backup target add r2 --type s3 --endpoint <account>.r2.cloudflarestorage.com \
    --bucket backups --access-key-id <id> --secret-access-key-stdin     # AWS, R2, B2, Wasabi, MinIO
ffm backup target add box --type sftp --host backup.example.com --user ffm \
    --key-file ~/.ssh/ffm_backup --path /backups --accept-host-key       # pins the host key it shows
ffm backup target add nas --type local --path /mnt/nas/ffm             # a mounted directory
ffm backup target add drive --type rclone --remote gdrive:ffm          # anything rclone reaches
ffm backup target list [--json] | test <name> | remove <name>

ffm backup mybench --to r2                       # encrypted, uploaded, confirmed by size
ffm backup schedule mybench --every 6h --to r2,box   # every scheduled archive, retention on each target
ffm backup list mybench --target r2
ffm backup pull r2 mybench [--archive <file>]    # newest by default, into the local backups dir
ffm restore ~/frappe/_backups/mybench/<file>.age copy --identity ~/ffm-backup.key
```

- Every target is tested (write, list, read back, delete) before it is saved.
- An upload is part of the backup: if it fails, the backup fails, the archive stays local, and a
  scheduled run prunes nothing.
- On each target, a schedule's retention applies like it does locally: scheduled archives of that
  bench only, never fewer than 3.
- Credentials live in `~/.config/ffm/backup-targets.json` (0600), never in argv or in archives. An
  S3 secret comes from stdin or `--secret-access-key-file`. SFTP uses a key file without a
  passphrase (make a dedicated one) and a pinned host key; there is no unpinned mode.
- Make the off-host copy survive a compromised host: S3 Object Lock or a lifecycle rule on the
  bucket, an append-only account on the SFTP server.

#### Proving a backup restores: `ffm backup verify`

```bash
ffm backup verify ~/frappe/_backups/mybench/<archive>.ffm.tar         # seconds
ffm backup verify <archive>.age --identity ~/ffm-backup.key --restore  # minutes: a real restore
ffm backup verify --target r2 --bench mybench --identity ~/ffm-backup.key --notify
```

`verify` decrypts the archive (age authenticates every byte). It then unpacks it with the guards a
restore uses, checks every member against the SHA-256 in the manifest, and checks that the
database dump contains Frappe's `__Auth` table. `--restore` also restores the archive into a
throwaway bench (`verify-<random>`), asks its site for `/api/method/ping`, and deletes the bench.
A production archive is restored as production with no TLS, under a name nobody resolves.
`--json` prints `ffm.verify/v1`. Run it from cron with `--notify`.

#### Notifications: `ffm notify`

```bash
ffm notify add phone --type ntfy --url https://ntfy.sh/<secret-topic>
echo "$SLACK_WEBHOOK" | ffm notify add team --type slack --url-stdin
echo "$BOT_TOKEN" | ffm notify add tg --type telegram --chat-id 123456 --token-stdin
ffm notify add hc --type healthchecks --url https://hc-ping.com/<uuid>
ffm notify add ops --type webhook --url https://example.com/hooks/ffm --on always   # JSON POST
ffm notify list [--json] | test <name> | remove <name>
```

Scheduled backups report through every notifier, and so does `backup verify --notify`. Only
failures are reported unless a notifier says `--on always`; a bench skipped because it is
stopped or busy is not a failure. A healthchecks notifier gets a start ping before every
scheduled run, then success or `/fail` with the error (credentials scrubbed). The service then
alerts when pings stop arriving, which is the only way to notice a scheduler that never runs.
URLs and tokens live in `~/.config/ffm/notify.json` (0600). Slack URLs and Telegram tokens are
read from stdin only. A notifier that cannot be reached is logged and never fails a backup.

### Scheduled backups

`ffm backup schedule` backs a bench up automatically and keeps a **bounded** set of archives,
so old backups neither pile up nor eat the disk.

```bash
ffm backup schedule mybench --every 24h                 # daily; ~3-4 weeks of history
ffm backup schedule mybench --every 1h --files weekly   # hourly database, weekly attachments
ffm backup schedule mybench --every weekly --keep 3     # three weekly archives
ffm backup schedule mybench --off                       # stop (archives are kept)
ffm backup schedule                                     # every schedule, last success, next run
ffm backup list [bench]                                 # archives with their full paths
ffm backup prune mybench --dry-run                      # preview the retention policy
```

Retention is tiered: of the **scheduled** archives, ffm keeps the newest one in each of the
last N hours, N days and N weeks, plus the newest 3 whatever their age — so a bench whose
backups have been failing for a month is never pruned down to nothing. With only `--every`,
a preset applies:

| `--every` | Keeps | Archives on disk | History | Attachments |
|--|--|--|--|--|
| `1h` | 24 hourly, 7 daily, 5 weekly | 32–34 | 3–4 weeks | daily |
| `6h` | 4 hourly, 7 daily, 5 weekly | 12–14 | 3–4 weeks | daily |
| `24h` | 7 daily, 5 weekly | 10–11 | 3–4 weeks | every run |
| `168h` | 4 weekly | 4 | 3 weeks | every run |

```
Flags:  --every <interval>      1h, 6h, 24h, 168h (whole hours), or hourly/daily/weekly
        --keep <n>              Keep n archives of the tier matching --every
        --keep-hourly/-daily/-weekly <n>   Set each tier explicitly
        --files <cadence>       Attachments: every-run, daily, weekly or never
        --off                   Stop scheduled backups for the bench
        --no-install            Save the policy without touching the hourly job
```

- **Manual archives are never deleted.** Only archives a scheduled run wrote (recorded in the
  archive header) are pruned, and only after a new backup has succeeded.
- **A stopped bench is skipped**, not started: it has not changed since its last backup.
- **Attachments are usually most of an archive's size.** `--files daily` or `weekly` keeps
  frequent runs small; a database-only archive restores without attachments.
- **One hourly job runs everything.** The first schedule installs a single job running
  `ffm backup run-due`; the last `--off` removes it. `ffm backup scheduler status` checks it
  (backend, drift from what this ffm would install, a missing binary). The job carries the ffm
  path, a `PATH` that reaches docker and any `FFM_*` settings — re-run `ffm backup scheduler
  install` after moving ffm or changing them.
- **Four schedulers, one choice.** `ffm backup scheduler install|print --backend
  cron|systemd|launchd|schtasks`:

  | Backend | Where the job lives |
  |--|--|
  | `cron` | one line tagged `# ffm-backup-tick` in your crontab |
  | `systemd` | `ffm-backup.timer` + `ffm-backup.service` in `~/.config/systemd/user` (`$XDG_CONFIG_HOME`), enabled with `systemctl --user` |
  | `launchd` | `dev.foxmayn.ffm.backup.plist` in `~/Library/LaunchAgents`, loaded into `gui/<uid>` |
  | `schtasks` | task `\ffm\backup` running `backup-run-due.cmd` in the config directory |

  Without `--backend` ffm uses, in order: the backend recorded in `~/.config/ffm/backup-scheduler.json`;
  cron when your crontab already holds an ffm line (installs from before backends existed stay
  on cron); the platform default — Task Scheduler on Windows, launchd on macOS, and on Linux cron
  when `crontab` exists and a cron daemon is running, otherwise a systemd user timer. Installing
  with another backend moves the job: the new one is installed first, then the old one removed.
  A systemd user timer only runs while you are logged in unless linger is on; `install` and
  `status` warn and print `sudo loginctl enable-linger $USER`, which ffm never runs for you.
- **One job per configuration.** With `FFM_CONFIG_DIR` set, the job name carries a hash of that
  directory (`ffm-backup-<hash>`, `dev.foxmayn.ffm.backup.<hash>`, `\ffm\backup-<hash>`), so two
  configurations do not replace each other. cron has one tagged line per user: ffm leaves a line
  another configuration installed alone (warning that this schedule's job was not installed), and
  `ffm backup scheduler install --force` takes it over.
  Runs are logged to `~/.config/ffm/backup-scheduler.log`.
- Archives stay on this machine. They protect against a broken site, not a lost disk — copy
  them off the host if that matters.

### `ffm restore <archive> [name]`

Rebuilds a bench from an archive. Always creates a **new** bench; it never writes into an
existing one, so a failed restore leaves the machine as it found it.

```bash
ffm restore ~/frappe/_backups/mybench/mybench_20260826T090000Z.ffm.tar
ffm restore mybench_20260826T090000Z.ffm.tar staging     # under a different name
ffm restore mybench_20260826T090000Z.ffm.tar --dry-run   # validate only
```

```
Flags:  --dry-run                       Validate the archive and print the plan
        --no-files                      Restore the database only
        --pin-apps                      Check apps out at the archived commits, not branch head
        --allow-missing-encryption-key  Restore without the site key (Password fields will not decrypt)
        --encryption-key <key>          Key for a GPG-encrypted dump
        --domain <domain>               Override the production domain
        --no-ssl / --acme-email <addr>  TLS handling for a production restore
        --reallocate-ports              Always take a fresh port pair
        --web-port / --socketio-port    Explicit host ports
        --admin-password <pw>           Set a new Administrator password
        --github-token <token>          Token for private app clones (github.com SSH sources use HTTPS with it)
        --skip-migrate                  Skip `bench migrate` after restoring
        --keep-on-failure               Leave a failed restore in place for diagnosis
        --lan                           Publish the ports on all interfaces instead of 127.0.0.1
        --ssh-agent                     Forward the host SSH agent into the dev container
        --skip-space-check              Do not check free disk space first
        --app <app>=<git-url>[@branch]  Clone an app from here instead (repeatable; frappe=... too)
```

Apps are cloned from the remote and branch recorded at backup time; apps whose source is in the
archive are unpacked from it. If the archive has no usable source for an app — every archive
written before ffm recorded bench's `upstream` remote, for an app outside the frappe and
erpnext GitHub organisations — the restore stops before building anything and names the
`--app` flag to pass:

```bash
ffm restore old.ffm.tar --app my_app=git@github.com:acme/my_app.git@main
```

The bench is provisioned by the same pipeline as `ffm create` — same image, apps, mode, ports
and proxy wiring — and the archived data is restored into it. Ports are reused when they are
free, so URLs stay stable on a fresh machine.

What a restore cannot bring back, and says so when it happens: uncommitted changes in an app's
working tree (unless the backup archived that app's source), the VPS tunnel (its token lives in this host's `tunnel.json`, not the archive),
the ffc API secret (Frappe mints a new one on every request), and absolute URLs stored *inside*
the database when the site is renamed.

### `ffm snapshot` — quick rollback points

A snapshot is the site's database (and with `--files` its attachments) as Frappe's own backup
writes it, kept in the bench's `workspace/.ffm-snapshots/` (0700). Take one before a migrate, a
`git switch` or an agent run; rolling back replaces the running site's database in place and
clears the cache. On a frappe + erpnext v15 dev bench a snapshot took 2.6 s and a rollback 19 s.

```bash
ffm snapshot create mybench --name before-upgrade [--files]
ffm snapshot list mybench [--json]            # ffm.snapshots/v1, newest first
ffm snapshot restore mybench --yes            # the newest; --name picks one, --migrate runs bench migrate
ffm snapshot delete mybench --name before-upgrade
```

Snapshots are not backups. They stay on this host, go away with the bench (`delete`, `recreate`)
and restore only into the same bench. Use `ffm backup` for a copy that outlives the bench. A
rollback warns when an app's commit has changed since the snapshot (run `bench migrate` or pass
`--migrate`).

### Secrets handling and `ffm rotate-credentials`

ffm keeps passwords and tokens out of every process argument list (what `ps` shows), on the host
and inside the containers:

- The database root password is a compose file secret: `<bench>/secrets/db_root_password` (directory 0700, file
  0600), mounted into the database container (`MARIADB_ROOT_PASSWORD_FILE` / `POSTGRES_PASSWORD_FILE`). It is no longer in
  `docker-compose.yml` or in `docker inspect`. Benches built by an older ffm get this on `ffm reconcile`.
- `bench new-site`, `restore` (also snapshots, `ffm db --import`) and `set-admin-password` read their passwords from
  stdin, the GitHub token and the backup encryption key never appear as arguments either.
- Flag values you type are in *ffm's* argument list: prefer `FFM_ADMIN_PASSWORD`, `FFM_DB_PASSWORD`,
  `FFM_GITHUB_TOKEN` (or `GITHUB_TOKEN`) and `FFM_ENCRYPTION_KEY`. A flag wins when both are set.
- Passwords in command output (the `create` / `restore` summary, `ffm status`, the ffc API secret, a tunnel token)
  are shown when stdout is a terminal or with `--show-secrets`, and printed as `******** (ffm status <bench> --show-secrets)`
  otherwise, so piped or CI output stays clean. Dashboard jobs and MCP output are always masked. `--json` is unchanged:
  secrets only with `--show-secrets`.

```bash
ffm rotate-credentials mybench                 # new database root and Administrator passwords
ffm rotate-credentials mybench --db-root       # only the database root password
ffm rotate-credentials mybench --admin --json --show-secrets   # ffm.credentials/v1
```

The bench must be running. The root password is changed in the database for every root account, the secret file is
rewritten in place, ffm's record is updated, and the new password is checked with a connection from the frappe
container. The site's own database user is not touched. Archives, `benches.json.bak` and dashboard job records written
earlier still hold the old values.

### `ffm clone <source> <new-name>`

Copies a bench: `ffm backup` of the source, then `ffm restore` under the new name, with each app
pinned to the source's commit, new ports and (dev) site `<new-name>.localhost`. Uncommitted app
changes come along only with `--vendor-apps <app>|all`. `--no-files` leaves attachments behind; a
production source needs `--domain` for the copy; `--keep-archive` keeps the intermediate archive.

### `ffm reconcile [name]`

Applies this ffm version's templates to a bench created by an earlier version, without losing data. It regenerates `docker-compose.yml` from the bench's saved settings and runs `docker compose up -d`: only containers whose definition changed are replaced, and the databases, the workspace and the bench record are kept. `ffm recreate`, by contrast, deletes the volumes and rebuilds from scratch.

```bash
ffm reconcile mybench --dry-run     # show what would change
ffm reconcile mybench               # apply it
ffm reconcile mybench --loopback    # publish the ports on 127.0.0.1 only
ffm reconcile mybench --lan         # publish them on all interfaces
ffm reconcile mybench --ssh-agent   # keep forwarding the SSH agent (dev)
```

**Template versions.** Each bench records the template version its `docker-compose.yml` was rendered from. `ffm list` names the benches built from older templates (and `--json` reports `templates_outdated`). Reconcile only applies `docker-compose.yml`; when the image recipe (`Dockerfile`) changed too, it says so, and `ffm restart <bench> --rebuild` applies it.

**Ports.** New benches publish their ports on `127.0.0.1` only; `--lan` (on `create`, `restore` or `reconcile`) publishes them on all interfaces. Benches created before v0.8.1 keep their behaviour until reconciled: prod benches move to `127.0.0.1` (Traefik reaches them over the proxy network, and a host-side Caddy or nginx reaches `127.0.0.1`), dev benches stay on all interfaces unless you pass `--loopback`. A dev bench with domain aliases needs `--lan`, because the browser reaches socket.io on the published port.

**Upgrading to v0.8.1.** Run `ffm reconcile <bench>` on every prod bench: before v0.8.1, prod workers never consumed Frappe's `default` queue, so most scheduled jobs and plain `frappe.enqueue` calls never ran, and gunicorn was reachable over plain HTTP on the published port.

### `ffm recreate [name]`

Backs the bench up, tears it down (containers, **volumes** and directory) and creates it again from its saved settings. The site comes back empty: restore the backup (`ffm restore <archive> <name>`) to get the data back, or use `ffm reconcile` when you only want this version's templates. If the new bench fails to build, the error names the backup to restore. The tunnel, if enabled, is re-enabled.

```
Flags:
  --force              Skip confirmation prompt
  --no-backup          Do not back the bench up first
  --reallocate-ports   Take a new port pair instead of reusing the stored one
  --github-token       Token for private app repos (not stored)
  --proxy-port / --proxy-host   Override the derived reverse-proxy settings (dev)
```

### `ffm clean-logs [name]`

Deletes rows older than `--days` (default 30, minimum 1) from Frappe's log tables, including `tabVersion` (document history) and `tabSessions`. MariaDB benches only.

```
Flags:
  --days int   Delete rows older than this many days (default 30)
  --dry-run    Print row counts without deleting anything
  --yes        Skip confirmation prompt
```

### `ffm scale [name]`

Changes how many processes a **production** bench runs, live and without losing data: the gunicorn workers in the frappe container and the replicas of the long- and short-queue worker containers. The counts are saved on the bench record, `docker-compose.yml` is rendered again and `docker compose up -d` rolls it out (databases, workspace and the other containers are untouched). A replica change adds or removes worker containers only; a `--gunicorn-workers` change replaces the frappe container, so the site is unavailable for a few seconds and ffm waits until it answers again. Dev benches run everything in one container and are refused (exit 2).

```
ffm scale mybench                                  # print the current counts and how many workers run
ffm scale mybench --worker-long-replicas 3
ffm scale mybench --gunicorn-workers 5 --worker-short-replicas 2 --dry-run
```

Every count is between 1 and 32: the record stores 0 as "not set" (so it falls back to the default), and 32 is a cap against typos, since each worker is a Python process of a few hundred MiB. Gunicorn's rule of thumb is 2 x CPUs + 1. `--json` prints `ffm.scale/v1`. If the bench is stopped the counts apply on the next `ffm start`.

### `ffm trim [name]`

Runs Frappe's `trim-tables` (drops columns that no field of the DocType owns any more) and `trim-database` (drops whole tables that no DocType owns, for example those left by an uninstalled app) on a running bench. Both destroy data, so the default is a dry run that lists what would go; `--yes` applies it, and at a terminal ffm asks first. Before anything is dropped ffm takes a snapshot named `pre-trim-<time>`; undo with `ffm snapshot restore <bench> --name pre-trim-<time>`.

```
ffm trim mybench                       # list the columns and tables that would be dropped
ffm trim mybench --yes                 # snapshot, then drop them
ffm trim mybench --tables --yes        # only unused columns
ffm trim mybench --database --yes --no-snapshot
```

`--no-snapshot` skips the ffm snapshot; Frappe's own pre-trim backup then runs instead. `trim-database` supports MariaDB only (on PostgreSQL Frappe's command fails with a SQL error): on a PostgreSQL bench `--tables` works and `--database` is refused. Frappe decides what counts as unused: a fresh bench already lists about a dozen leftover columns of its own core tables (`tabDocType.app`, `tabDocField.trigger`, …), which is one more reason ffm snapshots first. On PostgreSQL `tabFile.module` is created again by Frappe and is listed on every run.

### `ffm domain`

Routes extra hostnames (for example a LAN name such as `erp.internal`) to a bench through the shared Traefik proxy. ffm configures the routing; pointing DNS at this host is up to you, and `domain add` prints the records to create. A dev bench with aliases needs its ports on the LAN (`ffm reconcile <bench> --lan`).

```bash
ffm domain list [bench]
ffm domain add erp.internal [bench] [--tls]   # --tls: HTTPS with Let's Encrypt (prod with SSL only)
ffm domain remove erp.internal [bench]
```

### `ffm tunnel [name]`

Exposes a bench through a frp server on a VPS you own. Refused while the bench still has the default admin password, unless `--allow-default-password` is passed.

```bash
ffm tunnel server add myvps          # configure a server profile
ffm tunnel mybench                   # enable with the default server
ffm tunnel mybench --server myvps    # use a specific profile
ffm tunnel mybench --off             # disable and restore direct access
ffm tunnel mybench --print           # show frpc.toml without applying
ffm tunnel server list|set|use|remove [--yes]
```

### `ffm delete [name]`

Backs the bench up, then removes all containers, volumes, the images built for the bench, and the bench directory. If the backup fails, nothing is deleted. The archive is a manual one (never pruned), labelled "before delete".

```
Aliases: rm, remove
Flags:  --force       Skip confirmation prompt (the bench name is then required)
        --no-backup   Do not back the bench up first
```

### `ffm update`

Checks GitHub for the latest release and replaces the running binary in place. It refuses a release whose `checksums.txt` lacks a valid signature from an ffm release key, or whose archive does not match it.

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
    docker-compose.yml   # generated per bench (dev: 5 services, prod: 8 services)
    Dockerfile           # dev: full tools image; prod: minimal image
    secrets/             # 0700: db_root_password (0600), mounted into the database container as a compose secret
    home/                # dev only, 0700: survives the container
      claude/            # Claude Code config and login (CLAUDE_CONFIG_DIR)
      ffc/               # ffc config with the bench's API secret
    workspace/           # bind-mounted at /workspace in container
      .ffm-snapshots/    # ffm snapshot (0700)
      frappe-bench/
        AGENTS.md        # dev only: generated guide for coding agents (CLAUDE.md imports it)
        .agents/skills/  # dev only: 60 Frappe Claude skills + ffc skill
        .claude/skills/  # dev only: same skills for Claude Code
    .devcontainer/       # dev only
      devcontainer.json  # VS Code dev container config

  _backups/
    <bench-name>/
      <bench>_<UTC timestamp>.ffm.tar   # `ffm backup` archives (0600, in a 0700 directory)
      <bench>_<UTC timestamp>.auto.ffm.tar  # scheduled archives (pruned by retention)
      .schedule.json                     # last scheduled attempt

~/.config/ffm/
  benches.json           # state file tracking all managed benches (0600: holds passwords)
  benches.json.bak       # the previous version, kept on every save (0600)
  backup-scheduler.log   # one line per bench per scheduled run
  backup-scheduler.json  # which scheduler backend holds the hourly job
  backup-run-due.cmd     # Windows only: what the Task Scheduler task runs
  locks/                 # per-bench and run-due lock files
  .update_check.json     # cached latest release tag (refreshed every 24 h)
  .acme_email            # saved Let's Encrypt email (auto-used on subsequent prod benches)
```

## Services per bench

**Dev (5 containers):**

| Service | Image | Purpose |
|--|--|--|
| `frappe` | Built locally (dev image) | App server + `bench start` (honcho) + all dev tools |
| `mariadb` or `postgres` | `mariadb:11.8` / `postgres:18` | Database (selected via `--db-type`) |
| `redis-cache` | `redis:8-alpine` | Cache |
| `redis-queue` | `redis:8-alpine` | Background job queue |
| `mailpit` | `axllent/mailpit:v1.31.4` | Catches outgoing mail; UI on the web port + 6 (`ffm mail`) |

**Prod (8 containers):**

| Service | Image | Purpose |
|--|--|--|
| `frappe` | Built locally (minimal image) | Gunicorn (`wsgi:application` on port 8000); healthcheck on `/api/method/ping` |
| `socketio` | same | Node SocketIO server; TCP healthcheck |
| `worker-long` | same | Long background jobs |
| `worker-short` | same | Short background jobs |
| `scheduler` | same | Scheduled tasks (`bench schedule`) |
| `mariadb` or `postgres` | `mariadb:11.8` / `postgres:18` | Database with healthcheck (selected via `--db-type`) |
| `redis-cache` | `redis:8-alpine` | Cache; healthcheck |
| `redis-queue` | `redis:8-alpine` | Job queue on a volume with an append-only file, so queued jobs survive a recreate; healthcheck |

Since template version 4 (ffm v0.13.0; `ffm reconcile` applies it):
- Every prod container runs with `no-new-privileges`.
- socketio, the workers and the scheduler start once frappe is healthy.
- Gunicorn uses bench's own production flags (`--max-requests 5000 --max-requests-jitter 500
  --graceful-timeout 30`) and `--worker-tmp-dir /dev/shm`.
- MariaDB flushes its redo log at every commit (`--mariadb-fast-commit` restores the old setting).
- No CPU or memory limits are set: a limit that is too low gets workers killed in the middle of a
  job. Add them in `docker-compose.override.yml` if the host is shared.

`ffm create --mode prod` also enables the site's scheduler (a new Frappe site keeps it off until its
setup wizard runs). `ffm doctor` flags a prod site whose scheduler is off.

## Proxy container

A single Traefik container (`ffm-proxy`) is shared across all benches:

| Container | Image | Ports |
|--|--|--|
| `ffm-proxy` | `traefik:v3.7` | `0.0.0.0:80` (HTTP), `0.0.0.0:443` (HTTPS, when a prod bench uses SSL), `127.0.0.1:8080` (dashboard) |

Configured entirely via CLI flags — no config file on disk. Uses `--restart=unless-stopped`.

## Scripting: JSON output and exit codes

Read commands take `--json` and print one JSON object whose `schema` field names its shape and version:

| Command | Schema |
|--|--|
| `ffm list --json` | `ffm.list/v1` |
| `ffm status <bench> --json [--show-secrets]` | `ffm.status/v1` (passwords only with `--show-secrets`) |
| `ffm backup list [bench] --json` | `ffm.backups/v1` |
| `ffm backup schedule --json` | `ffm.schedules/v1` |
| `ffm domain list <bench> --json` | `ffm.domains/v1` |
| `ffm tunnel server --json [--show-secrets]` | `ffm.tunnel-servers/v1` (tokens only with `--show-secrets`) |
| `ffm version --json` | `ffm.version/v1` |
| `ffm open [bench] --json`, `ffm mail [bench] --json` | `ffm.url/v1` |
| `ffm clean --json [--dry-run] [--seeds]` | `ffm.clean/v1` |
| `ffm snapshot list [bench] --json` | `ffm.snapshots/v1` |
| `ffm backup target list --json` | `ffm.targets/v1` (no secrets) |
| `ffm backup verify … --json` | `ffm.verify/v1` |
| `ffm notify list --json` | `ffm.notifiers/v1` (no URLs or tokens) |
| `ffm doctor [bench] --json` | `ffm.doctor/v1` |
| `ffm rotate-credentials [bench] --json [--show-secrets]` | `ffm.credentials/v1` (new passwords only with `--show-secrets`) |
| `ffm site [bench] --json` | `ffm.site/v1` |
| `ffm scale [bench] --json` | `ffm.scale/v1` |
| `ffm app list [bench] --json` | `ffm.apps/v1` |

Within a version, fields are only added. Renaming or removing one bumps the version and is listed in the release's upgrade notes. Times are RFC 3339 UTC; absent values are omitted.

Exit codes:

| Code | Meaning |
|--|--|
| 0 | Success |
| 1 | Any other failure |
| 2 | Usage: bad flags or arguments, or a prompt was needed without a terminal |
| 3 | The named bench does not exist (or no bench exists) |
| 4 | Busy: another ffm operation holds the bench |
| 5 | Reserved: Docker unavailable |
| 6 | Wrong state: e.g. the bench is stopped |
| 7 | Refused before changing anything (restore preflight) |

## Environment variables

| Variable | Default | Description |
|--|--|--|
| `FFM_BENCHES_DIR` | `~/frappe` | Where bench directories are stored |
| `FFM_CONFIG_DIR` | `~/.config/ffm` | Where the state file is stored |
| `FFM_BACKUPS_DIR` | `~/frappe/_backups` | Where `ffm backup` archives are written |
| `FFM_NON_INTERACTIVE` | unset | Never prompt; fail with the flag to pass instead (also implied by `CI` or no terminal) |
| `FFM_INTERACTIVE` | unset | Force prompting back on when no terminal is detected |
| `FFM_NO_UPDATE_CHECK` | unset | Skip the background update notice (also skipped when `CI` is set) |
| `FFM_KEEP_ON_FAILURE` | unset | Same as `ffm create --keep-on-failure` |
| `FFM_MATCH_HOST_USER` | unset | Same as `ffm create --match-host-user` |
| `FFM_ADMIN_PASSWORD` | unset | Same as `--admin-password` (`create`, `restore`); the flag wins |
| `FFM_DB_PASSWORD` | unset | Same as `ffm create --db-password`; the flag wins |
| `FFM_GITHUB_TOKEN`, `GITHUB_TOKEN` | unset | Same as `--github-token` (`create`, `restore`, `recreate`, `app add`); `FFM_GITHUB_TOKEN` first, the flag wins |
| `FFM_ENCRYPTION_KEY` | unset | Same as `ffm restore --encryption-key`; the flag wins |

Global flags: `--verbose` (show docker compose output), `--non-interactive`. Shell completion: `ffm completion bash|zsh|fish|powershell`.

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
