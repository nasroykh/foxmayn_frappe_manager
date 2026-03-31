# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is this?

**ffm** (Foxmayn Frappe Manager) — a Go CLI that wraps Docker Compose to create, manage, and destroy Frappe benches. Supports two modes:
- **dev**: single `frappe` container running all processes via `bench start` (honcho), with dev tools (zsh, starship, Claude Code, ffc) baked into the image. Site name: `<name>.localhost`, routed by shared Traefik proxy.
- **prod**: separate containers per process (gunicorn, socketio, workers, scheduler), minimal image, public domain, optional Let's Encrypt SSL via Traefik.

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
cmd/ffm/main.go          → entrypoint, calls cli.Execute() (wraps NewRootCmd().Execute() + waitForUpdateCheck)

internal/
  cli/                    → cobra command definitions (one file per subcommand)
    root.go               → root command, registers all subcommands, global --verbose flag (no -v shorthand); --version/-v flag via cobra root.Version; PersistentPreRunE runs update check on every command; Execute() exported function wraps cobra Execute + waitForUpdateCheck
    create.go             → multi-step bench provisioning pipeline (port alloc → compose+Dockerfile write → docker build → bench init via docker compose run → docker up → site creation → app install → mode-specific steps); supports --mode dev (default) and --mode prod; interactive form (runCreateFormFull) asks mode first, then shows dev or prod follow-up fields; automatic rollback on failure
    delete.go             → teardown with confirmation prompt (uses charmbracelet/huh)
    list.go               → table output with live docker status (uses lipgloss v2); shows MODE column + domain for prod benches
    start.go / stop.go / restart.go → lifecycle commands; restart.go delegates to runStop + runStart (--rebuild flag rewrites Dockerfile from template [mode-aware] + rebuilds image before starting); start.go auto-installs Claude/agent skills on start if missing (dev only, idempotent check against .claude/skills/foxmayn-frappe-cli/SKILL.md)
    shell.go / logs.go    → interactive docker exec (zsh for dev frappe container, bash for prod) / log streaming; shell.go also supports --exec for non-interactive one-shot commands
    status.go             → per-container status + credentials display; shows mode + domain URL
    pick.go               → resolveBenchName() + benchNameFromCWD() + pickBench() helpers; CWD auto-detection: if inside ~/frappe/<name>/, returns that bench name silently; falls back to interactive picker otherwise
    ffc.go                → ffm ffc: generate Frappe API keys + write ffc config inside container (dev only); also called as a step in create
    proxy.go              → ffm proxy subcommand group: start / stop / status; bare 'ffm proxy' shows status
    setproxy.go           → ffm set-proxy: configures socketio_port / use_ssl / per-site host_name inside the container for reverse-proxy deployments (dev and prod); dev: restarts bench start automatically; prod: prints 'ffm restart' hint; --reset uses mode-aware defaults (dev: socketio_port 9000 / no ssl; prod: socketio_port 443 / ssl on / host_name https://<domain>); --print-caddy / --print-nginx emit ready-to-paste config snippets
    update.go             → ffm update: fetches latest release from GitHub API, compares semver, downloads platform-specific archive (tar.gz/zip), atomically replaces running binary; --check (check only) / --yes (skip confirmation)
    update_check.go       → background update notification: reads/writes ~/.config/ffm/.update_check.json (24 h TTL); runUpdateCheck() called via PersistentPreRunE on every command except 'update'; startBackgroundFetch() goroutine; waitForUpdateCheck() blocks up to 2 s before process exit

  bench/                  → core bench logic, no CLI concerns
    bench.go              → name validation, project/container name helpers
    app.go                → AppSpec type + ParseAppSpec(): parses --apps values supporting short names, SSH URLs, HTTPS URLs, and url@branch syntax
    compose.go            → renders docker-compose.yml and Dockerfile from embedded Go templates (dev/ or prod/ subdir based on Mode); also writes .devcontainer/devcontainer.json (dev only); ComposeData includes Mode, Domain, NoSSL, ForwardSSHAgent
    docker.go             → Runner type: all docker compose interactions (build/up/down/exec/logs/ps); ExecOutputInDir for non-interactive streaming exec (used by shell --exec); ConfigureGitHubToken/CleanupGitHubToken for private HTTPS repos
    frappe_api.go         → Runner.GenerateAdminAPIKeys(siteName): runs bench execute inside container to generate API key/secret for Administrator
    port.go               → port allocation: scans state store + probes host for free port pairs
    templates/
      dev/
        docker-compose.yml.tmpl  → 4-service dev compose; bind-mounts ./workspace to /workspace, pip-cache + yarn-cache volumes, Traefik labels for <name>.localhost, conditional SSH agent socket
        Dockerfile.tmpl          → full dev image: extends frappe/bench:latest with zsh+zinit+starship+Go+ffc+pnpm+Claude Code; pre-fetches Frappe Claude Skill Package + ffc skill to /opt/
      prod/
        docker-compose.yml.tmpl  → 7-service prod compose: frappe (gunicorn), socketio, worker-long, worker-short, scheduler, mariadb (with healthcheck), redis×2; Traefik labels for domain routing on websecure (or web if NoSSL); per-bench HTTP→HTTPS redirect labels
        Dockerfile.tmpl          → minimal prod image: frappe/bench:latest + corepack pnpm only

  proxy/
    proxy.go              → all Traefik lifecycle logic: EnsureNetwork(), Start(), Stop(), IsRunning(), Status(), SupportsHTTPS(), EnsureHTTPS(email); createContainer() for HTTP-only; createContainerHTTPS() adds port 443, ffm-letsencrypt volume, ACME resolver flags; no global HTTP→HTTPS redirect (each prod bench handles its own via compose labels)

  config/                 → path resolution (bench dirs, state file, acme email file), respects FFM_BENCHES_DIR and FFM_CONFIG_DIR env vars; AcmeEmailFile() → ~/.config/ffm/.acme_email
  state/                  → JSON-file state store (~/.config/ffm/benches.json); Bench struct includes Mode ("dev"/"prod", empty=dev for backward compat), Domain, IsProd()/IsDev() helpers, ProxyHost; Store.Update(name, fn) applies a mutation and saves
  version/                → build-time version variables
```

### Key patterns

- **`bench.Runner`** is the central abstraction for docker compose operations. All CLI commands that touch Docker go through it. Four output modes: silent (`ExecSilent`), verbose-conditional (`withOutput`), always-interactive (`composeWithIO`), and quiet-with-error-dump (`Build()` / `Run()`). `Build()` and `Run()` capture output in quiet mode and only dump it to stderr when the command fails.
- **State store** (`state.Store`) is a flat JSON file, not a database. Load-modify-save pattern; not concurrency-safe (OK for a CLI). `Bench.IsProd()` returns true when `Mode == "prod"`; empty Mode is treated as dev (backward compat).
- **Port allocation** starts at web=8000 / socketio=9000 and increments by 10 per bench. Each pair is checked against both the state store and a live host port probe.
- **Compose + Dockerfile templates** are embedded at compile time via `//go:embed` from `templates/dev/` or `templates/prod/` based on `ComposeData.Mode`. Changes to either template require rebuild.
- **bench init runs at container runtime** — The dev Docker image is tools-only (zsh, starship, Go, ffc, pnpm, Claude Code — no Frappe source). `ffm create --mode dev` runs `docker compose run --rm frappe bash -c "bench init ... /tmp && cp to /workspace/frappe-bench && patch venv paths && copy skills"` as a one-off container. The prod image is minimal (no tools), so bench init skips the skills copy step. `bench init` exits 0 even on failure, so `ffm create` explicitly checks for `apps/` after init.
- **Bind mount for bench data** — bench files live at `~/frappe/<bench>/workspace/frappe-bench/` on the host. The compose file bind-mounts `./workspace:/workspace`. `ffm delete` calls `down -v` (removes named volumes) then `os.RemoveAll(benchDir)`.
- **CWD auto-detection** — `resolveBenchName(args, title)` in `pick.go` resolves the bench name in order: (1) explicit `args[0]`; (2) `benchNameFromCWD()` — if CWD is under `~/frappe/<name>/` and `<name>` is in the state store, returns it silently; (3) `pickBench()` interactive picker. Escape and ctrl+c abort via a custom `huh.KeyMap`.
- **Mode-aware commands**: `shell.go` uses zsh for dev, bash for prod. `start.go` skips skills install + bench start for prod (compose `command:` handles services). `restart.go` passes `Mode` to `bench.ComposeData` when rebuilding. `setproxy.go` works for both modes — dev restarts bench start, prod prints an `ffm restart` hint; `--reset` restores mode-appropriate defaults. `list.go` shows MODE column. `status.go` shows domain URL for prod.
- **Production create pipeline**: After containers start and site is created, prod skips developer mode, runs `bench build` (asset compilation), always sets `host_name` to `https://<domain>` (or `http://` if no-ssl). `socketio_port` is set to 443 (ssl) or 80 (no-ssl). Dev server (`nohup bench start`) and ffc setup are skipped.
- **Let's Encrypt**: `ffm create --mode prod` calls `proxy.EnsureHTTPS(email)` (unless `--no-ssl`), which ensures the Traefik container has port 443 bound and ACME HTTP-01 configured. ACME email is persisted to `~/.config/ffm/.acme_email` after first use. No global HTTP→HTTPS redirect — each prod bench applies per-bench redirect labels in its compose template.
- **Prod with existing Caddy/Nginx on 80/443**: Use `--no-ssl`. `EnsureNetwork()` is called instead of `EnsureHTTPS()`, so Traefik does not try to bind port 443. The compose exposes `WebPort` and `SocketIOPort` directly on the host. The user points Caddy at those ports. After create, `socketio_port` defaults to 80; if Caddy handles HTTPS run `ffm set-proxy <name> --host <domain>` (sets socketio_port 443, use_ssl 1, host_name https://<domain>) then `ffm restart <name>`.
- **zsh shell** — `ffm shell` execs into `zsh` for dev `frappe` containers (bash for prod + all other services). The shell is pre-configured with zinit, zsh-autosuggestions, zsh-syntax-highlighting, history search, fixed key bindings, and starship — baked into the dev image via heredocs in `Dockerfile.tmpl`.
- **ffc integration** — dev benches only. Every new dev bench gets `ffc` baked into the image, API keys auto-generated during `ffm create`, and ffc config written. `ffm ffc [name]` can regenerate keys on a running dev bench.
- **Claude/agent skills** — dev benches only. 60 Frappe Claude skills + ffc skill are pre-fetched into the dev Docker image at build time and copied during bench init. On every `ffm start` / `ffm restart`, `runStart` checks for `.claude/skills/foxmayn-frappe-cli/SKILL.md` and runs the full copy if missing (idempotent back-fill).
- **Private repo support** — `--apps` accepts short names, SSH URLs, HTTPS URLs, and `url@branch` or `name@branch` suffix. SSH agent forwarding is automatic when `SSH_AUTH_SOCK` is set. For private HTTPS repos, `--github-token` configures a temporary git credential helper (cleaned up via `defer`).
- The `create` command has automatic rollback: a named-return defer tears down containers and removes the bench directory on any failure. State is saved only on success.

### Dependencies

- `github.com/spf13/cobra` — CLI framework
- `charm.land/lipgloss/v2` — terminal styling (list/status output)
- `github.com/charmbracelet/huh` — interactive prompts (create form, bench picker, delete confirmation)
- `github.com/charmbracelet/huh/spinner` — spinner animation during update download/install
- `github.com/charmbracelet/bubbles` — key bindings for huh KeyMap (Escape to quit)
- `github.com/go-resty/resty/v2` — HTTP client for GitHub releases API (update command + background check)

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
  docker-compose.yml     # generated per bench (dev: 4 services, prod: 7+ services)
  Dockerfile             # dev: tools image; prod: minimal image
  workspace/             # bind-mounted into container at /workspace; bench lives at workspace/frappe-bench/
    frappe-bench/
      .agents/skills/    # dev only: 60 Frappe Claude skills + ffc skill
      .claude/skills/    # dev only: same skills for Claude Code
  .devcontainer/         # dev only
    devcontainer.json    # VS Code dev container config

~/.config/ffm/
  benches.json           # state file
  .update_check.json     # cached latest release tag (refreshed every 24 h by background goroutine)
  .acme_email            # saved Let's Encrypt email (auto-used on subsequent prod bench creations)
```
