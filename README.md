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
# Create a new bench (pulls images, runs bench init, creates site — takes ~10 min first run)
ffm create mybench

# Open the site
open http://localhost:8000   # or whatever port was allocated
# Login: administrator / admin

# Shell into the bench container
ffm shell mybench

# Stop and start
ffm stop mybench
ffm start mybench

# Tear it all down
ffm delete mybench
```

## Commands

### `ffm create <name>`

Creates and starts a new Frappe development bench end-to-end:

1. Allocates a free host port pair (web: 8000+, socketio: 9000+)
2. Writes a `docker-compose.yml` to `~/frappe/<name>/`
3. Starts MariaDB, Redis (cache + queue), and the Frappe container
4. Runs `bench init` (clones Frappe, installs Python/Node deps)
5. Configures `common_site_config.json` with DB and Redis connection strings
6. Creates the site with `bench new-site`
7. Enables developer mode
8. Starts the dev server via `nohup bench start`

```
Flags:
  --frappe-branch string    Frappe branch to initialise (default "version-15")
  --admin-password string   Frappe site admin password (default "admin")
  --db-password string      MariaDB root password (default "123")
  -v, --verbose             Show docker compose output
```

### `ffm list` / `ffm ls`

Lists all managed benches with their live status, port, site name, and Frappe branch.

### `ffm status <name>`

Shows per-container status for a bench (image, state, ports, uptime).

### `ffm start <name>`

Starts a stopped bench and re-launches the dev server.

### `ffm stop <name>`

Stops all containers for a bench. Data is preserved — use `start` to resume.

### `ffm shell <name>`

Opens an interactive bash shell inside the frappe container, landing directly in `/home/frappe/frappe-bench`.

```
Flags:
  --service string   Container to shell into (default "frappe")
                     Use "mariadb" to get a DB shell, etc.
```

### `ffm logs <name> [service]`

Streams container logs. Omit `[service]` to tail all containers.

```
Flags:
  -f, --follow   Follow log output (default true)
```

### `ffm delete <name>`

Stops and removes all containers, volumes, and the bench directory. Prompts for confirmation unless `--force` is passed.

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

| Service       | Image                        | Purpose                  |
|---------------|------------------------------|--------------------------|
| `frappe`      | `frappe/bench:latest`        | Frappe app + dev server  |
| `mariadb`     | `mariadb:11.8`               | Database                 |
| `redis-cache` | `redis:alpine`               | Cache                    |
| `redis-queue` | `redis:alpine`               | Background job queue     |

## Building from source

```bash
make build    # → ./bin/ffm
make vet      # go vet
make fmt      # gofmt
make tidy     # go mod tidy
make clean    # remove binary
```
