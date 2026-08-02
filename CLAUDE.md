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
make                # tidy + build + install (default goal)
make ship           # same as above explicitly
make build          # compiles to ./bin/ffm
make install        # installs to $GOPATH/bin, creates ~/.config/ffm/ directory
make vet            # go vet ./...
make fmt            # gofmt -w .
make tidy           # go mod tidy
make clean          # removes ./bin/ffm
```

Version info is injected at build time via `-ldflags` (see `Makefile` LDFLAGS).

Tests are sparse but present — `make test` runs `go test ./...`. Coverage today is template
rendering (`internal/bench/hostuid_render_test.go`, `internal/bench/renderout_test.go`) and
dashboard handlers (`internal/dashboard/handler_test.go`). The create pipeline and most of the
CLI are untested.

## Architecture

**The most important thing to know:** `internal/cli/` no longer holds bench logic. Its files are
thin cobra wrappers that parse flags, resolve the bench name, prompt when interactive, and
delegate to `internal/manager`. Every operation lives in `manager.Service` so that the CLI and
the web dashboard run identical code paths. If you are looking for the create pipeline, it is
`internal/manager/create.go`, not `internal/cli/create.go`.

```
cmd/ffm/main.go          → entrypoint, calls cli.Execute(), exits 1 on error

internal/
  cli/                    → cobra command definitions; flags, prompts, delegation. No bench logic.
    root.go               → registers all 17 subcommands; global --verbose and --non-interactive;
                            PersistentPreRunE runs the update check (skipped for 'update');
                            Execute() dispatches the hidden __dashboard-daemon argv BEFORE cobra
                            parses anything, then runs cobra, then waitForUpdateCheck()
    interactive.go        → isInteractive() / mustNotPrompt() / cancelled() / withSpinner().
                            $CI and $FFM_NON_INTERACTIVE imply non-interactive; $FFM_INTERACTIVE
                            forces prompting back on; --non-interactive always wins
    interactive_unix.go / interactive_windows.go → hasControllingTerminal() per platform
    create.go             → create flags + the interactive forms (runCreateForm,
                            runCreateFormFull); calls manager.Service.Create
    recreate.go           → ffm recreate: --force / --reallocate-ports / --github-token /
                            --proxy-port / --proxy-host
    delete.go             → confirmation prompt (--force skips), then manager.Service.Delete
    list.go               → lipgloss table over manager.ListBenchViews; alias 'ls'
                            columns: NAME MODE DB STATUS PORT DOMAIN BRANCH
    start.go / stop.go / restart.go → ~25-line wrappers; restart carries --rebuild
    shell.go / logs.go    → the last commands that drive bench.Runner directly.
                            shell: zsh for dev frappe, bash otherwise; --exec for one-shot,
                            --service to target another container. logs: --follow defaults TRUE
    status.go             → per-container status + credentials; also prints a clean-logs tip
                            for benches older than 7 days
    clean_logs.go         → ffm clean-logs: --days 30 / --dry-run / --yes
    pick.go               → resolveBenchName() + benchNameFromCWD() + pickBench();
                            benchPickKeyMap() is what makes Esc abort every form in the package
    ffc.go                → ffm ffc: regenerate Frappe API keys + ffc config
    proxy.go              → ffm proxy start/stop/status (bare 'ffm proxy' shows status)
    setproxy.go           → ffm set-proxy flags (--port default 443, --host, --no-ssl, --reset,
                            --print-caddy, --print-nginx); behaviour in manager/setproxy.go
    tunnel.go             → ffm tunnel flags; bare 'ffm tunnel' PRINTS STATUS, it does not enable
    tunnel_server.go      → ffm tunnel server list/add/set/remove/use
    dashboard.go          → ffm dashboard start/stop/status/logs; --daemon re-execs the binary
                            with the hidden __dashboard-daemon argv
    dashboard_daemon.go   → maybeRunDashboardDaemon(): that hidden entrypoint
    dashboard_sys_unix.go / dashboard_sys_windows.go → SysProcAttr for the detached daemon
    update.go             → ffm update: releases API, semver compare, atomic self-replace
    update_check.go       → background update notice (24 h TTL); returns immediately when
                            $FFM_NO_UPDATE_CHECK or $CI is set
    claude_mcp.go         → DEAD CODE — superseded by internal/manager/claude_mcp.go. Safe to delete.

  manager/                → the shared service layer. All bench operations live here. Output goes
                            through ProgressWriter, never straight to stdout, so the CLI and the
                            dashboard's job runner share one pipeline.
    service.go            → Service{Store, Verbose, mu}; serialises all store access behind a
                            mutex because the dashboard is concurrent
    types.go              → CreateInput / RecreateInput / RestartInput / SetProxyInput /
                            ExecInput / CleanLogsInput / BenchView / BenchDetail / DashboardStats
    create.go             → THE create pipeline (see Key patterns). Also ReadSavedAcmeEmail /
                            SaveAcmeEmail
    recreate.go           → teardown + Create with stored inputs; reuses the old port pair
    lifecycle.go          → Start / Stop / Delete / TeardownBenchFiles; Start also back-fills
                            skills, .mcp.json, the JS/Procfile patches, dev server, tunnel
    restart.go            → Restart; --rebuild re-renders the Dockerfile (carrying MatchHostUser),
                            rewrites wsgi.py for prod, re-applies the JS patches, rebuilds
    benches.go            → LiveStatus / ListBenchViews / GetBenchDetail / DashboardOverview
    setproxy.go           → SetProxy, mode-aware reset defaults, Caddy/Nginx snippets
    tunnel_ops.go         → TunnelEnable / TunnelDisable
    proxy_ops.go          → ProxyStatus / ProxyStart / ProxyStop
    ffc.go                → SetupFFC: API keys + ~/.config/ffc/config.yaml + .mcp.json.
                            NOTE: no mode gate — it will run against a prod bench and fail late
    claude_mcp.go         → writes workspace/frappe-bench/.mcp.json (ffc MCP server)
    clean_logs.go         → deletes old rows from 7 Frappe log tables
    exec.go               → Exec / ExecOrError: one-shot command in a container
    hostuser.go           → hostUserIDs() / composeUserIDs() backing --match-host-user
    jobs.go               → JobStore: async create/recreate/restart jobs persisted to jobs.json
    progress.go           → ProgressWriter + CLIProgress / DiscardProgress / BufferProgress

  dashboard/              → the /admin web UI. Stdlib only: html/template, embed, net/http.
    handler.go            → //go:embed templates + static, basic auth, rendering
    handler_actions.go    → POST endpoints for every bench operation
    handler_jobs.go       → job list/detail + SSE progress stream
    handler_logs.go       → SSE docker compose log stream
    handler_csrf.go       → HMAC double-submit CSRF tokens
    config.go             → dashboard.json; DefaultListenAddr 127.0.0.1:8787
    templates/ static/    → layout + 9 pages; admin.css, admin.js

  server/server.go        → net/http server (Go 1.22 method+pattern routes). GET /health always;
                            /admin/* only when an admin password is set; WriteTimeout 0 for SSE

  bench/                  → core bench logic, no CLI concerns
    bench.go              → name validation, ProjectName/container helpers
    app.go                → AppSpec + ParseAppSpec(): short names, SSH/HTTPS URLs, url@branch
    compose.go            → ComposeData + renders docker-compose.yml / Dockerfile /
                            devcontainer.json. ALSO REWRITES FRAPPE SOURCE — see Key patterns:
                            WriteWsgiWrapper, PatchAuthenticateJs, PatchUtilsJs, PatchProcfileWorker
    docker.go             → Runner: build/up/down/exec/logs/ps, UpServices, RestartService,
                            ExecDetached, LogsString, WaitForMariaDB/WaitForPostgres, WaitForHTTP,
                            ConfigureGitHubToken/CleanupGitHubToken
    frappe_api.go         → Runner.GenerateAdminAPIKeys(siteName)
    port.go               → AllocatePorts (web 8000 / socketio 9000, +10 per bench, max 50) plus
                            ValidBenchPortPair / CheckTCPPortsFree for --web-port/--socketio-port
    templates/
      dev/
        docker-compose.yml.tmpl  → 4 services (DB, redis×2, frappe); DB conditional on DBType;
                                   bind-mounts ./workspace, pip/yarn cache volumes, Traefik labels
                                   for <name>.localhost, conditional SSH agent socket
        Dockerfile.tmpl          → full dev image: zsh/zinit/starship/Go/ffc/pnpm/Claude Code +
                                   pre-fetched Frappe skills; optional HostUID/HostGID remap layer
      prod/
        docker-compose.yml.tmpl  → 8 services (DB, redis-cache, redis-queue, frappe/gunicorn,
                                   socketio, worker-long, worker-short, scheduler) + an x-logging
                                   anchor (json-file 10m×3); Traefik labels + per-bench
                                   HTTP→HTTPS redirect; optional ./mysql-logs bind
        Dockerfile.tmpl          → minimal: frappe/bench + corepack pnpm + optional remap layer

  proxy/proxy.go          → Traefik lifecycle: EnsureNetwork / IsNetworkPresent / Start / Stop /
                            IsRunning / Status / DashboardURL / SupportsHTTPS / EnsureHTTPS(email)
  tunnel/
    config.go             → Server + Config; ~/.config/ffm/tunnel.json (0o600)
    frpc.go               → frpc container via docker run (not compose); RenderFrpcToml (0o600)
  config/paths.go         → honours FFM_BENCHES_DIR / FFM_CONFIG_DIR: BenchesDir, BenchDir,
                            StateFile, AcmeEmailFile, TunnelConfigFile, DashboardConfigFile,
                            DashboardPIDFile, DashboardLogFile, JobsFile
  state/store.go          → JSON state store; Bench includes Mode, DBType, Domain, ProxyHost,
                            MatchHostUser, Tunnel (*TunnelState); IsProd/IsDev/DBEngine/IsPostgres
  version/version.go      → build-time version variables
```

### Key patterns

- **`manager.Service` is the seam.** New behaviour goes in `internal/manager/`, not `internal/cli/`.
  The CLI passes `CLIProgress{}`; the dashboard passes a `BufferProgress` so the same pipeline can
  stream into an async job. `Service` serialises all state-store access behind a mutex; the raw
  `state.Store` is still not concurrency-safe on its own.
- **`bench.Runner`** is the low-level docker compose abstraction. Output modes: silent-capture
  (`ExecSilent`), capture-and-return (`LogsString`), verbose-conditional (`withOutput`),
  always-interactive (`composeWithIO`), stream-without-TTY (`ExecOutputInDir`), fire-and-forget
  (`ExecDetached`), and quiet-with-error-dump (`Build()` / `Run()` — capture in non-verbose mode,
  dump to stderr only on failure). `internal/proxy` and `internal/tunnel` bypass compose entirely
  and use raw `docker run`.
- **Port allocation** starts at web=8000 / socketio=9000, +10 per bench, capped at 50 benches.
  Each pair is checked against the state store and a live host probe. Each bench publishes a
  **6-port range** (`WebPort`..`WebPort+5`), so explicit `--web-port` values must be ≥10 apart —
  `CheckTCPPortsFree` only probes the two base ports and will not catch a range collision.
- **Non-interactive safety** — every huh form and spinner is guarded by `isInteractive()`, which
  probes `/dev/tty` (the descriptor huh itself uses) rather than stat-ing stdin, because
  `/dev/null` is a character device and would read as interactive. Without a terminal, commands
  fail with a message naming the flag to pass instead of hanging forever.
- **Compose + Dockerfile templates** are embedded via `//go:embed` from `templates/dev/` or
  `templates/prod/` based on `ComposeData.Mode`. Changing either requires a rebuild, and existing
  benches only pick up **Dockerfile** changes via `restart --rebuild`; the **compose** file is
  regenerated only by `ffm recreate`.
- **Container/host uid mismatch** — `frappe/bench` runs as uid 1000. On a host with a different
  uid the `./workspace` bind mount is unwritable in both directions: bench init cannot write into
  the directory the host created, and the host cannot write `wsgi.py` into the directory the
  container created. `--match-host-user` renders a remap layer as the Dockerfile's first
  instruction. Persisted as `MatchHostUser` so `restart --rebuild` and `recreate` keep it.
  Invisible on macOS — Docker Desktop rewrites ownership across the virtiofs boundary.
- **bench init runs at container runtime** — the dev image is tools-only. `create` runs
  `docker compose run --rm frappe bash -c "bench init … /tmp/ffm-bench-init && cp -a to
  /workspace/frappe-bench && patch venv paths && copy skills"`. `bench init` exits 0 even on
  failure, so `create` explicitly checks for `apps/` afterwards.
- **ffm rewrites Frappe source on the host.** `PatchAuthenticateJs` and `PatchUtilsJs` modify
  `apps/frappe/realtime/*` so socket.io auth works from inside Docker (`PatchUtilsJs` has separate
  v15 and v16 branches — v16 rewrote `get_url()`). `PatchProcfileWorker` wraps the dev `worker:`
  line in a self-restarting loop so an idle-Redis worker exit doesn't make honcho tear down the
  whole stack. All are idempotent and re-applied on every start and rebuild. If you are wondering
  why a bench's `realtime/utils.js` differs from upstream, this is why.
- **Production create pipeline** uses a **two-phase start**. Phase 1 brings up only DB + redis +
  frappe so scheduler/worker containers don't crash-loop on app modules that aren't installed
  yet. Then site creation, app install, `bench build`. The frappe container is then *restarted* —
  not SIGHUP'd, because PID 1 is `bash -c` and forked gunicorn workers would inherit the
  pre-install `sys.path`. Phase 2 starts socketio, workers, scheduler. Prod skips developer mode,
  always sets `host_name`, sets `socketio_port` to 443/80 and `socketio_frappe_url` to
  `http://frappe:8000`. **`bench build` runs in BOTH modes** — dev needs it too since bench init
  moved to container runtime, or Desk renders unstyled.
- **create rollback** — a named-return defer dumps DB *and* frappe container logs to stderr, then
  tears down containers and removes the bench directory. `--keep-on-failure` /
  `$FFM_KEEP_ON_FAILURE` stops the teardown and prints the literal cleanup command instead,
  because state is saved only on success and `ffm delete` cannot reach an unregistered bench.
- **CWD auto-detection** — `resolveBenchName` resolves: (1) `args[0]`; (2) `benchNameFromCWD()`
  if under `~/frappe/<name>/`; (3) `pickBench()`. `pickBench` errors when no benches exist and
  **auto-selects when exactly one is tracked** — the picker only appears with 2+.
- **Mode-aware behaviour** lives in `manager/`: `lifecycle.go` gates the dev-only start steps,
  `restart.go` re-renders mode-appropriate files, `setproxy.go` has mode-specific reset defaults.
  Only `cli/shell.go` still branches on mode itself (zsh vs bash).
- **Let's Encrypt** — `--mode prod` without `--no-ssl` calls `proxy.EnsureHTTPS(email)`, which
  talks to the **production** ACME API. Never do that from CI or against a domain you don't
  control. `--no-ssl` calls `EnsureNetwork()` instead and publishes ports directly.
- **Claude/agent skills + ffc** — dev benches only. Skills are pre-fetched into the dev image and
  copied during bench init; `Service.Start` back-fills them idempotently if
  `.claude/skills/foxmayn-frappe-cli/SKILL.md` is missing, and also rewrites `.mcp.json` (wiring
  Claude Code to `ffc mcp --site <name>`) and re-applies the three patches above.
- **Private repos** — `--apps` takes short names, SSH URLs, HTTPS URLs, and `@branch` suffixes.
  SSH agent forwarding is automatic when `SSH_AUTH_SOCK` is set (dev only). `--github-token`
  configures a credential helper; because bench init runs in a one-off `compose run` container
  that `exec`-based setup cannot reach, the credential setup is prepended into the bench init
  bash command instead. `--frappe-repo` maps to `bench init --frappe-path` with the same
  `@branch` syntax.
- **VPS tunnel** — frpc runs as a standalone container (`ffm-<name>-frpc`), not under compose, so
  it survives compose stops. `Service.Start` starts it when `b.Tunnel.Enabled` (warning and
  skipping if the named server profile is gone from tunnel.json); `TeardownBenchFiles` stops it
  before `compose down`.

### Undocumented-elsewhere gotchas

- `ffm create` has seven prod-tuning flags with no coverage outside `--help`:
  `--mariadb-buffer-pool` (1G), `--gunicorn-workers` (2), `--worker-long-replicas` (1),
  `--worker-short-replicas` (1), `--redis-cache-maxmem` (512mb, allkeys-lru),
  `--redis-queue-maxmem` (512mb, noeviction), `--slow-query-log` (creates `<bench>/mysql-logs/`).
- Credentials default to `--admin-password admin` and `--db-password ffm123456`. Prod rejects the
  former. Failure paths interpolate `CombinedOutput` into errors, so a failed `bench new-site`
  can print the DB root password.
- `make skills-init*` symlinks `.agents/skills/*` into `.claude/`, `.cursor/`, `.agent/` — this
  repo is itself skill-managed.

### Dependencies

- `github.com/spf13/cobra` — CLI framework
- `charm.land/lipgloss/v2` — terminal styling (list/status output)
- `github.com/charmbracelet/huh` + `huh/spinner` — interactive prompts
- `github.com/charmbracelet/bubbles` — key bindings for `benchPickKeyMap`
- `github.com/go-resty/resty/v2` — HTTP client for the GitHub releases API

The dashboard and server add **no** dependencies — stdlib `net/http`, `html/template`, `embed`,
`log/slog`, `crypto/hmac`. `go.mod` declares Go 1.26.1; the router uses Go 1.22 method+pattern
syntax.

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
  docker-compose.yml     # generated per bench (dev: 4 services, prod: 8 services)
  Dockerfile             # dev: tools image; prod: minimal image
  frpc.toml              # written when tunnel is enabled (0o600 — contains token)
  mysql-logs/            # prod + MariaDB + --slow-query-log only
  workspace/             # bind-mounted into container at /workspace
    frappe-bench/
      .mcp.json          # Claude Code MCP config → `ffc mcp --site <bench>`
      .agents/skills/    # dev only: Frappe Claude skills + ffc skill
      .claude/skills/    # dev only: same skills for Claude Code
      sites/wsgi.py      # prod only: gunicorn entrypoint forcing single-site routing
  .devcontainer/         # dev only
    devcontainer.json

~/.config/ffm/
  benches.json           # state file
  .update_check.json     # cached latest release tag (24 h TTL; skipped when $CI is set)
  .acme_email            # saved Let's Encrypt email
  tunnel.json            # VPS tunnel server profiles (0o600 — contains tokens)
  dashboard.json         # dashboard listen addr + admin password (0600 when a password is set)
  dashboard.pid          # PID of a backgrounded `ffm dashboard start --daemon`
  dashboard.log          # dashboard daemon log
  jobs.json              # async job state for the dashboard
```

Both roots are overridable: `FFM_BENCHES_DIR` and `FFM_CONFIG_DIR`. Setting them per job is how
you isolate concurrent runs, since `benches.json` is a whole-file read-modify-write.
