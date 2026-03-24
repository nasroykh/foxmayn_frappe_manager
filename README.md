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
# Create a new bench (interactive form: version, apps, starship preset)
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
- Starship prompt preset for the shell (Pure is the default; Tokyo Night, Pastel Powerline, and more available)

Steps performed:

1. Allocates a free host port pair (web: 8000+, socketio: 9000+)
2. Writes `docker-compose.yml` and a `Dockerfile` to `~/frappe/<name>/`
3. Builds the Docker image — installs **zsh**, **zinit** (with zsh-autosuggestions + zsh-syntax-highlighting), **starship**, **Go 1.26**, and **[ffc](https://github.com/nasroykh/foxmayn_frappe_cli)**, baked into the image layer
4. Starts MariaDB, Redis (cache + queue), and the Frappe container
5. Runs `bench init` (clones Frappe, installs Python/Node deps)
6. Configures `common_site_config.json` with DB and Redis connection strings
7. Creates the site with `bench new-site`
8. Enables developer mode
9. Starts the dev server via `nohup bench start`
10. Generates Frappe API keys and writes `~/.config/ffc/config.yaml` inside the container

```
Flags:
  --frappe-branch string      Frappe branch to initialise (default "version-15")
  --apps stringArray          Additional apps to install (e.g. --apps erpnext)
  --admin-password string     Frappe site admin password (default "admin")
  --db-password string        MariaDB root password (default "123")
  --starship-preset string    Starship prompt preset (default "pure-preset")
  -v, --verbose               Show docker compose output
```

### `ffm list` / `ffm ls`

Lists all managed benches with their live status, port, site name, and Frappe branch.

### `ffm status [name]`

Shows per-container status for a bench (image, state, ports, uptime). If `name` is omitted, an interactive picker is shown.

### `ffm start [name]`

Starts a stopped bench and re-launches the dev server. If `name` is omitted, an interactive picker is shown.

### `ffm stop [name]`

Stops all containers for a bench. Data is preserved — use `start` to resume. If `name` is omitted, an interactive picker is shown.

### `ffm shell [name]`

Opens an interactive **zsh** shell inside the frappe container, landing directly in `/home/frappe/frappe-bench`. The shell comes with zinit, zsh-autosuggestions, zsh-syntax-highlighting, and the starship prompt pre-configured.

If `name` is omitted, an interactive picker is shown.

```
Flags:
  --service string   Container to shell into (default "frappe")
                     Use "mariadb" to get a DB shell (uses bash), etc.
```

### `ffm logs [name] [service]`

Streams container logs. Omit `[service]` to tail all containers. If `name` is omitted, an interactive picker is shown.

```
Flags:
  -f, --follow   Follow log output (default true)
```

### `ffm preset [name]`

Changes the starship prompt preset on a running bench without rebuilding the image. Applies the new preset config directly inside the container — takes under a second.

Available presets: Default, Pure, Tokyo Night, Pastel Powerline, Gruvbox Rainbow, Nerd Font Symbols, Bracketed Segments, Jetpack.

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
    Dockerfile           # extends frappe/bench:latest with zsh + zinit + starship

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
