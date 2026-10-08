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

- Default URL: **http://127.0.0.1:8787/admin** (HTTP Basic auth)
- Bind on LAN: `ffm dashboard start --listen 0.0.0.0:8787 --admin-password '…'` — use TLS via a reverse proxy in production
- Long operations (`create`, `recreate`) run as **background jobs** with live log streaming
- Interactive shell: use `ffm shell <name>` in the terminal (dashboard supports one-shot `exec` only)

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

`ffm status` shows a bench's toolchain. Benches created before ffm v0.11.0 keep running on the
image defaults (Python 3.14 and Node 24 for every branch); `ffm recreate` rebuilds them with the
branch's toolchain. uv's package cache lives in the `pip-cache` volume, so a replaced container
does not download Python packages again (`ffm reconcile` sets this up on existing dev benches).

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
  --fresh     Rebuild ignoring the build cache and re-pulling the base image (implies --rebuild)
```

`--rebuild` rewrites the `Dockerfile` from the current template (mode-aware) and runs `docker compose build`. Useful after an ffm upgrade changes the template. Docker's layer cache keeps tools installed during the build (ffc, Claude Code, starship) at the version of the first build; `--fresh` updates them too.

The images a bench is built from are pinned in each ffm release: the `frappe/bench` base image, Redis 8, Traefik 3.7 (used when the shared proxy is created), the ffc skills and the Frappe skill pack. MariaDB (11.8) and PostgreSQL (18) follow their minor or major tag.

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
exist: no record, no bench directory and no ffm operation in progress. A bench tracked under another
`FFM_CONFIG_DIR` looks gone from here, so read the list.

```bash
ffm clean --dry-run                 # list only (--json: ffm.clean/v1)
ffm clean --yes                     # remove without asking
ffm clean --build-cache --dangling  # also Docker's build cache and untagged images (shared with
                                    # every other project on the host)
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

Generates Frappe API keys and writes `~/.config/ffc/config.yaml` inside the bench container. Dev benches only; refused on prod, where ffc is not installed. Run if ffc setup failed during `ffm create` or to regenerate keys.

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
- **One hourly job runs everything.** The first schedule installs a single tagged line in your
  crontab running `ffm backup run-due`; the last `--off` removes it. `ffm backup scheduler
  status` checks it. The line carries the ffm path, a `PATH` that reaches docker and any
  `FFM_*` settings — re-run `ffm backup scheduler install` after moving ffm or changing them.
  Runs are logged to `~/.config/ffm/backup-scheduler.log`. On Windows, `ffm backup scheduler
  print` gives the Task Scheduler command to run once.
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
    workspace/           # bind-mounted at /workspace in container
      frappe-bench/
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
| `frappe` | Built locally (minimal image) | Gunicorn (`wsgi:application` on port 8000) |
| `socketio` | same | Node SocketIO server |
| `worker-long` | same | Long background jobs |
| `worker-short` | same | Short background jobs |
| `scheduler` | same | Scheduled tasks (`bench schedule`) |
| `mariadb` or `postgres` | `mariadb:11.8` / `postgres:18` | Database with healthcheck (selected via `--db-type`) |
| `redis-cache` | `redis:8-alpine` | Cache |
| `redis-queue` | `redis:8-alpine` | Job queue |

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
| `ffm clean --json [--dry-run]` | `ffm.clean/v1` |

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
