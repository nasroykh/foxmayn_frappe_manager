# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is this?

**ffm** (Foxmayn Frappe Manager) — a Go CLI that wraps `frappe_docker`'s devcontainer compose pattern to create, manage, and destroy local Frappe development benches. Each bench gets its own Docker Compose project (`ffm-<name>`) with MariaDB, Redis (cache + queue), and a Frappe container.

## Install & Build

```bash
# Quick install (no clone needed):
go install github.com/nasroykh/foxmayn_frappe_manager/cmd/ffm@latest

# Or build from source (injects version/commit/date via ldflags):
make build          # compiles to ./bin/ffm
make install        # installs to $GOPATH/bin, copies config.example.yaml to ~/.config/ffm/config.yaml
make vet            # go vet ./...
make fmt            # gofmt -w .
make tidy           # go mod tidy
make clean          # removes ./bin/ffm
```

Version info is injected at build time via `-ldflags` (see `Makefile` LDFLAGS).

There are **no tests** yet. No test framework or test files exist.

## Architecture

```
cmd/ffm/main.go          → entrypoint, calls cli.NewRootCmd().Execute()

internal/
  cli/                    → cobra command definitions (one file per subcommand)
    root.go               → root command, registers all subcommands, global --verbose flag
    create.go             → multi-step bench provisioning (port alloc → compose write → docker up → bench init → site creation → dev server)
    delete.go             → teardown with confirmation prompt (uses charmbracelet/huh)
    list.go               → table output with live docker status (uses lipgloss v2)
    start.go / stop.go    → lifecycle commands
    shell.go / logs.go    → interactive docker exec / log streaming
    status.go / version.go

  bench/                  → core bench logic, no CLI concerns
    bench.go              → name validation, project/container name helpers
    compose.go            → renders docker-compose.yml from embedded Go template
    docker.go             → Runner type: all docker compose interactions (up/down/exec/logs/ps)
    port.go               → port allocation: scans state store + probes host for free port pairs
    templates/
      docker-compose.yml.tmpl  → Go text/template, embedded via //go:embed

  config/                 → path resolution (bench dirs, state file), respects FFM_BENCHES_DIR and FFM_CONFIG_DIR env vars
  state/                  → JSON-file state store (~/.config/ffm/benches.json), tracks bench metadata
  version/                → build-time version variables
```

### Key patterns

- **`bench.Runner`** is the central abstraction for docker compose operations. All CLI commands that touch Docker go through it. It has three output modes: silent (`ExecSilent`), verbose-conditional (`withOutput`), and always-interactive (`composeWithIO`).
- **State store** (`state.Store`) is a flat JSON file, not a database. Load-modify-save pattern; not concurrency-safe (OK for a CLI).
- **Port allocation** starts at web=8000 / socketio=9000 and increments by 10 per bench. Each pair is checked against both the state store and a live host port probe.
- **Compose template** is embedded at compile time via `//go:embed`. Changes to `templates/docker-compose.yml.tmpl` require rebuild.
- The `create` command is the most complex — it's a 12-step sequential pipeline. If it fails mid-way, there's no automatic rollback (state may be partially written).

### Dependencies

- `github.com/spf13/cobra` — CLI framework
- `charm.land/lipgloss/v2` — terminal styling (list/status output)
- `github.com/charmbracelet/huh` — interactive prompts (delete confirmation)

## Runtime layout (on user's machine)

```
~/frappe/<bench-name>/docker-compose.yml   # generated per bench
~/.config/ffm/benches.json                 # state file
~/.config/ffm/config.yaml                  # user config (from config.example.yaml)
```
