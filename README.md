# ffm — Foxmayn Frappe Manager

A Go CLI that wraps [frappe_docker](https://github.com/frappe/frappe_docker)'s devcontainer compose pattern so you can create, manage, and destroy local Frappe development benches with a single command. No YAML to write, no Docker flags to memorize.

## Requirements

- [Docker](https://docs.docker.com/get-docker/) with the Compose plugin (`docker compose`)
- Go 1.21+ (only needed to build from source)

## Installation

The quickest way to install — no clone needed:

```bash
go install github.com/nasroykh/foxmayn_frappe_manager/cmd/ffm@latest
```

This downloads, compiles, and places the `ffm` binary in your `$GOPATH/bin` (make sure it's in your `PATH`).

Alternatively, build from source for version metadata (commit hash, build date):

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

Steps performed:

1. Allocates a free host port pair (web: 8000+, socketio: 9000+)
2. Writes `docker-compose.yml` and a `Dockerfile` to `~/frappe/<name>/`
3. Builds the Docker image — installs **zsh**, **zinit** (with zsh-autosuggestions + zsh-syntax-highlighting), **starship**, **Go 1.26**, and **[ffc](https://github.com/nasroykh/foxmayn_frappe_cli)**, baked into the image layer
4. Starts MariaDB, Redis (cache + queue), and the Frappe container
5. Runs `bench init` (clones Frappe, installs Python/Node deps)
6. Configures `common_site_config.json` with DB and Redis connection strings
7. Creates the site with `bench new-site`
8. Enables developer mode
9. Installs any additional `--apps` (public or private)
10. Starts the dev server via `nohup bench start`
11. Generates Frappe API keys and writes `~/.config/ffc/config.yaml` inside the container

```
Flags:
  --frappe-branch string      Frappe branch to initialise (default "version-15")
  --apps stringArray          Apps to install — see formats below
  --admin-password string     Frappe site admin password (default "admin")
  --db-password string        MariaDB root password (default "123")
  --github-token string       GitHub personal access token for private HTTPS repos
  -v, --verbose               Show docker compose output
```

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

Lists all managed benches with their live status, port, site name, and Frappe branch.

### `ffm status [name]`

Shows per-container status for a bench (image, state, ports, uptime). If `name` is omitted, an interactive picker is shown.

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
    Dockerfile           # extends frappe/bench:latest with zsh + zinit + starship + Go + ffc

~/.config/ffm/
  benches.json           # state file tracking all managed benches
```

## Environment variables

| Variable          | Default          | Description                        |
|-------------------|------------------|------------------------------------|
| `FFM_BENCHES_DIR` | `~/frappe`       | Where bench directories are stored |
| `FFM_CONFIG_DIR`  | `~/.config/ffm`  | Where the state file is stored     |

## Services per bench

Each bench runs four Docker containers scoped to a Compose project named `ffm-<name>`:

| Service       | Image                              | Purpose                  |
|---------------|------------------------------------|--------------------------|
| `frappe`      | Built locally from bench Dockerfile | Frappe app + dev server (zsh + zinit + starship + Go + ffc) |
| `mariadb`     | `mariadb:11.8`                     | Database                 |
| `redis-cache` | `redis:alpine`                     | Cache                    |
| `redis-queue` | `redis:alpine`                     | Background job queue     |

## Building from source

```bash
make build    # → ./bin/ffm
make vet      # go vet
make fmt      # gofmt
make tidy     # go mod tidy
make clean    # remove binary
```
