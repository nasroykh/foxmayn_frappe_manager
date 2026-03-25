# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is this?

**ffm** (Foxmayn Frappe Manager) — a Go CLI that wraps `frappe_docker`'s devcontainer compose pattern to create, manage, and destroy local Frappe development benches. Each bench gets its own Docker Compose project (`ffm-<name>`) with MariaDB, Redis (cache + queue), and a Frappe container running zsh + zinit + starship. A shared Traefik container (`ffm-proxy`) provides `<name>.localhost` domain routing across all benches.

## Install & Build

```bash
# Quick install (no clone needed):
go install github.com/nasroykh/foxmayn_frappe_manager/cmd/ffm@latest

# Or build from source (injects version/commit/date via ldflags):
make build          # compiles to ./bin/ffm
make install        # installs to $GOPATH/bin, creates ~/.config/ffm/ directory
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
    create.go             → multi-step bench provisioning (port alloc → compose+Dockerfile write → docker build [includes bench init] → docker up → site creation → dev server)
    delete.go             → teardown with confirmation prompt (uses charmbracelet/huh)
    list.go               → table output with live docker status (uses lipgloss v2)
    start.go / stop.go    → lifecycle commands
    shell.go / logs.go    → interactive docker exec (zsh for frappe container) / log streaming; shell.go also supports --exec for non-interactive one-shot commands
    status.go / version.go
    pick.go               → resolveBenchName() + pickBench() helpers: interactive bench selector shown when name arg is omitted
    ffc.go                → ffm ffc: generate Frappe API keys + write ffc config inside container; also called as a step in create
    proxy.go              → ffm proxy subcommand group: start / stop / status; bare 'ffm proxy' shows status
    setproxy.go           → ffm set-proxy: configures socketio_port / use_ssl / per-site host_name inside the container for reverse-proxy deployments; --reset restores direct-access defaults; --print-caddy / --print-nginx emit ready-to-paste config snippets using actual state ports

  bench/                  → core bench logic, no CLI concerns
    bench.go              → name validation, project/container name helpers
    app.go                → AppSpec type + ParseAppSpec(): parses --apps values supporting short names, SSH URLs, HTTPS URLs, and url@branch syntax
    compose.go            → renders docker-compose.yml and Dockerfile from embedded Go templates; ComposeData includes Name (Traefik router identifier), FrappeBranch (Docker build arg), and ForwardSSHAgent bool
    docker.go             → Runner type: all docker compose interactions (build/up/down/exec/logs/ps); ExecOutputInDir for non-interactive streaming exec (used by shell --exec); ConfigureGitHubToken/CleanupGitHubToken for private HTTPS repos
    frappe_api.go         → Runner.GenerateAdminAPIKeys(siteName): runs bench execute inside container to generate API key/secret for Administrator
    port.go               → port allocation: scans state store + probes host for free port pairs
    templates/
      docker-compose.yml.tmpl  → Go text/template, embedded via //go:embed; passes FRAPPE_BRANCH build arg, declares frappe-bench named volume, attaches frappe service to ffm-proxy network with Traefik labels, conditionally mounts SSH agent socket
      Dockerfile.tmpl          → Go text/template, embedded via //go:embed; extends frappe/bench:latest with bench init (with BuildKit cache mounts for pip/yarn)+zsh+zinit+starship+Go 1.26+ffc; bakes in fixed starship.toml and .zshrc

  proxy/
    proxy.go              → all Traefik lifecycle logic: EnsureNetwork(), Start(), Stop(), IsRunning(), Status(), containerStatus(); Traefik configured entirely via CLI flags, no config file on disk

  config/                 → path resolution (bench dirs, state file), respects FFM_BENCHES_DIR and FFM_CONFIG_DIR env vars
  state/                  → JSON-file state store (~/.config/ffm/benches.json), tracks bench metadata; Bench struct includes ProxyHost (omitempty, set by ffm set-proxy / ffm create --proxy-host); Store.Update(name, fn) applies a mutation and saves
  version/                → build-time version variables
```

### Key patterns

- **`bench.Runner`** is the central abstraction for docker compose operations. All CLI commands that touch Docker go through it. It has three output modes: silent (`ExecSilent`), verbose-conditional (`withOutput`), and always-interactive (`composeWithIO`). `Build()` always streams output (image build takes minutes).
- **State store** (`state.Store`) is a flat JSON file, not a database. Load-modify-save pattern; not concurrency-safe (OK for a CLI).
- **Port allocation** starts at web=8000 / socketio=9000 and increments by 10 per bench. Each pair is checked against both the state store and a live host port probe.
- **Compose + Dockerfile templates** are embedded at compile time via `//go:embed`. Changes to either template require rebuild.
- **bench init is baked into the Docker image** — `bench init` (which clones Frappe and installs all Python/Node deps) runs during `docker build`, not at container runtime. The `FRAPPE_BRANCH` is passed as a Docker build arg from the compose file. This means the second bench with the same Frappe branch builds in seconds (Docker layer cache hit). BuildKit cache mounts for pip and yarn persist download caches across builds, so even switching branches is faster. The `ARG FRAPPE_BRANCH` is declared as the very last instruction before the `RUN bench init`, so changing the branch only invalidates that one layer.
- **Named volume for bench data** — bench data lives in a named Docker volume (`frappe-bench`, project-scoped to `ffm-<name>_frappe-bench`). Docker auto-populates it from the image layer on the first `docker compose up`. It persists across `ffm stop`/`start` and `docker compose down` (without `-v`). `ffm delete` calls `down -v` which destroys it as part of full cleanup.
- **Interactive bench picker** — `resolveBenchName(args, title)` in `pick.go` is called by every command that takes an optional bench name. If `args` is empty it calls `pickBench()`, which loads the state store, auto-selects if only one bench exists, and otherwise shows a `huh.Select` list. Escape and ctrl+c both abort via a custom `huh.KeyMap`.
- **zsh shell** — `ffm shell` execs into `zsh` for the `frappe` container (falls back to `bash` for other services like mariadb). The shell is pre-configured with zinit, zsh-autosuggestions, zsh-syntax-highlighting, history search, fixed key bindings, and starship — all baked into the image at `ffm create` time via heredocs in `Dockerfile.tmpl`.
- **Starship config** — the starship prompt and `.zshrc` are fixed and baked into the image at build time via shell heredocs in `Dockerfile.tmpl`. There is no preset selection; the config mirrors the host machine's `~/.config/starship.toml` and `~/.zshrc` (history, completion, key bindings, zinit, ffc completions). To change the prompt, edit `Dockerfile.tmpl` and rebuild.
- **ffc integration** — every new bench gets Go 1.26 and `ffc` (Foxmayn Frappe CLI) baked into the image. After site creation, `ffm create` calls `Runner.GenerateAdminAPIKeys` which runs `bench execute frappe.core.doctype.user.user.generate_keys` inside the container (no HTTP needed), then writes `~/.config/ffc/config.yaml`. If this step fails, `ffm ffc [name]` can retry it on a running bench.
- **Private repo support** — `--apps` accepts short names, SSH URLs (`git@github.com:org/app.git`), HTTPS URLs, and `url@branch` or `name@branch` suffix to override the branch. `bench.ParseAppSpec` handles all forms. SSH agent forwarding is automatic when `SSH_AUTH_SOCK` is set on the host (`ForwardSSHAgent` in `ComposeData`). For private HTTPS repos, `--github-token` configures a temporary git credential helper inside the container (cleaned up after all `bench get-app` calls via `defer`).
- **Traefik reverse proxy** — a single `ffm-proxy` Traefik container is shared across all benches, providing `<name>.localhost` routing. The `frappe` service in each bench's compose file is attached to the `ffm-proxy` Docker network and carries Traefik labels. MariaDB and Redis stay on the default project network only. `ffm create` calls `proxy.EnsureNetwork()` before `docker compose up` so the external network always exists (preventing the "network declared as external, but could not be found" error even when the proxy is not running). The Traefik container uses `--providers.docker.network=ffm-proxy` to prevent the multi-NIC ambiguity bug. Container is configured entirely via CLI flags — no config file is written to disk. Uses `--restart=unless-stopped` to survive Docker daemon restarts.
- **External reverse proxy support** — `ffm set-proxy [name]` (or `ffm create --proxy-port / --proxy-host`) configures the bench for Caddy/Nginx/etc. deployments. It execs into the frappe container and sets three Frappe config values: `socketio_port` (global, must match the proxy's public port so the browser JS connects through the proxy rather than directly to port 9000), `use_ssl` (global, set to 1 for port 443 so Frappe generates https:// links and secure cookies), and per-site `host_name` (the public URL, used for link generation/OAuth/email). Dev server is restarted automatically. `--reset` restores direct-access defaults. `--print-caddy` / `--print-nginx` generate ready-to-paste configs using the actual ports from state.
- The `create` command is the most complex — it's a multi-step sequential pipeline. If it fails mid-way, there's no automatic rollback (state may be partially written).

### Dependencies

- `github.com/spf13/cobra` — CLI framework
- `charm.land/lipgloss/v2` — terminal styling (list/status output)
- `github.com/charmbracelet/huh` — interactive prompts (create form, bench picker, delete confirmation)
- `github.com/charmbracelet/bubbles` — key bindings for huh KeyMap (Escape to quit)

## Release

Releases are created by pushing a `v*` tag. The GitHub Actions workflow (`.github/workflows/release.yml`) triggers GoReleaser, which cross-compiles for linux/darwin/windows on amd64/arm64, packages archives, and publishes the GitHub release with a `checksums.txt`.

**Tag-to-release flow:**
```bash
git tag v0.1.0
git push origin v0.1.0
# → GitHub Actions runs GoReleaser → release assets published automatically
```

**Key files:**
- `.goreleaser.yaml` — build config: binary `ffm`, cmd `./cmd/ffm`, ldflags for version injection, archives named `ffm_<version>_<os>_<arch>`
- `.github/workflows/release.yml` — triggered on `v*` tags; uses `goreleaser-action@v6` with `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24: true`
- `install.sh` — `curl | sh` installer for Linux/macOS; detects OS/arch, downloads + verifies SHA256, installs to `/usr/local/bin` or `~/.local/bin`
- `install.ps1` — `irm | iex` installer for Windows; installs to `%LOCALAPPDATA%\Programs\ffm`, adds to user PATH, no admin rights required

## Runtime layout (on user's machine)

```
~/frappe/<bench-name>/
  docker-compose.yml     # generated per bench
  Dockerfile             # generated per bench; extends frappe/bench:latest with bench init+zsh+zinit+starship+Go+ffc

~/.config/ffm/
  benches.json           # state file
```
