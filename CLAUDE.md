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
cmd/ffm/main.go          → entrypoint, calls cli.Execute() (wraps NewRootCmd().Execute() + waitForUpdateCheck)

internal/
  cli/                    → cobra command definitions (one file per subcommand)
    root.go               → root command, registers all subcommands, global --verbose flag (no -v shorthand); --version/-v flag via cobra root.Version; PersistentPreRunE runs update check on every command; Execute() exported function wraps cobra Execute + waitForUpdateCheck
    create.go             → multi-step bench provisioning (port alloc → compose+Dockerfile write → docker build [tools only] → bench init via docker compose run → docker up → site creation → dev server)
    delete.go             → teardown with confirmation prompt (uses charmbracelet/huh)
    list.go               → table output with live docker status (uses lipgloss v2)
    start.go / stop.go / restart.go → lifecycle commands; restart.go delegates to runStop + runStart (--rebuild flag rewrites Dockerfile from template + rebuilds image before starting); start.go auto-installs Claude/agent skills on start if missing (idempotent check against .claude/skills/foxmayn-frappe-cli/SKILL.md)
    shell.go / logs.go    → interactive docker exec (zsh for frappe container) / log streaming; shell.go also supports --exec for non-interactive one-shot commands
    status.go
    pick.go               → resolveBenchName() + pickBench() helpers: interactive bench selector shown when name arg is omitted
    ffc.go                → ffm ffc: generate Frappe API keys + write ffc config inside container; also called as a step in create
    proxy.go              → ffm proxy subcommand group: start / stop / status; bare 'ffm proxy' shows status
    setproxy.go           → ffm set-proxy: configures socketio_port / use_ssl / per-site host_name inside the container for reverse-proxy deployments; --reset restores direct-access defaults; --print-caddy / --print-nginx emit ready-to-paste config snippets using actual state ports
    update.go             → ffm update: fetches latest release from GitHub API, compares semver, downloads platform-specific archive (tar.gz/zip), atomically replaces running binary; --check (check only) / --yes (skip confirmation); newerThan()/parseSemver() helpers shared with update_check.go
    update_check.go       → background update notification: reads/writes ~/.config/ffm/.update_check.json (24 h TTL); runUpdateCheck() called via PersistentPreRunE on every command except 'update'; startBackgroundFetch() goroutine; waitForUpdateCheck() blocks up to 2 s before process exit

  bench/                  → core bench logic, no CLI concerns
    bench.go              → name validation, project/container name helpers
    app.go                → AppSpec type + ParseAppSpec(): parses --apps values supporting short names, SSH URLs, HTTPS URLs, and url@branch syntax
    compose.go            → renders docker-compose.yml and Dockerfile from embedded Go templates; also writes .devcontainer/devcontainer.json; ComposeData includes Name (Traefik router identifier) and ForwardSSHAgent bool
    docker.go             → Runner type: all docker compose interactions (build/up/down/exec/logs/ps); ExecOutputInDir for non-interactive streaming exec (used by shell --exec); ConfigureGitHubToken/CleanupGitHubToken for private HTTPS repos
    frappe_api.go         → Runner.GenerateAdminAPIKeys(siteName): runs bench execute inside container to generate API key/secret for Administrator
    port.go               → port allocation: scans state store + probes host for free port pairs
    templates/
      docker-compose.yml.tmpl  → Go text/template, embedded via //go:embed; bind-mounts ./workspace to /workspace (bench lives at /workspace/frappe-bench), pip-cache + yarn-cache named volumes for download caching, attaches frappe service to ffm-proxy network with Traefik labels, conditionally mounts SSH agent socket
      Dockerfile.tmpl          → Go text/template, embedded via //go:embed; extends frappe/bench:latest with zsh+zinit+starship+Go+ffc+pnpm+Claude Code (tools only — no bench init); pre-fetches Frappe Claude Skill Package (60 skills) to /opt/frappe-skills and ffc skill to /opt/ffc-skill at image build time; bakes in fixed starship.toml and .zshrc

  proxy/
    proxy.go              → all Traefik lifecycle logic: EnsureNetwork(), Start(), Stop(), IsRunning(), Status(), containerStatus(); Traefik configured entirely via CLI flags, no config file on disk

  config/                 → path resolution (bench dirs, state file), respects FFM_BENCHES_DIR and FFM_CONFIG_DIR env vars
  state/                  → JSON-file state store (~/.config/ffm/benches.json), tracks bench metadata; Bench struct includes ProxyHost (omitempty, set by ffm set-proxy / ffm create --proxy-host); Store.Update(name, fn) applies a mutation and saves
  version/                → build-time version variables
```

### Key patterns

- **`bench.Runner`** is the central abstraction for docker compose operations. All CLI commands that touch Docker go through it. It has four output modes: silent (`ExecSilent`), verbose-conditional (`withOutput`), always-interactive (`composeWithIO`), and quiet-with-error-dump (`Build()` / `Run()`). `Build()` and `Run()` capture output in quiet mode and only dump it to stderr when the command fails, keeping `ffm create` output minimal by default while still surfacing failure details.
- **State store** (`state.Store`) is a flat JSON file, not a database. Load-modify-save pattern; not concurrency-safe (OK for a CLI).
- **Port allocation** starts at web=8000 / socketio=9000 and increments by 10 per bench. Each pair is checked against both the state store and a live host port probe.
- **Compose + Dockerfile templates** are embedded at compile time via `//go:embed`. Changes to either template require rebuild.
- **bench init runs at container runtime** — The Docker image is tools-only (zsh, starship, Go, ffc, pnpm, Claude Code — no Frappe source). `ffm create` runs `docker compose run --rm frappe bash -c "bench init ... /tmp/ffm-bench-init && rm -rf /workspace/frappe-bench && cp -a /tmp/ffm-bench-init /workspace/frappe-bench && grep -rIl '/tmp/ffm-bench-init' /workspace/frappe-bench | xargs -r sed -i 's|/tmp/ffm-bench-init|/workspace/frappe-bench|g' && rm -rf /tmp/ffm-bench-init && mkdir -p .agents/skills .claude/skills && cp -r /opt/frappe-skills/skills/source/. .agents/skills/ && cp -r /opt/frappe-skills/skills/source/. .claude/skills/ && mkdir -p .agents/skills/foxmayn-frappe-cli .claude/skills/foxmayn-frappe-cli && cp /opt/ffc-skill/SKILL.md .agents/skills/foxmayn-frappe-cli/ && cp /opt/ffc-skill/SKILL.md .claude/skills/foxmayn-frappe-cli/"` as a one-off container after the image build. The init runs to `/tmp` first (a clean path not affected by the image's own `/workspace` VOLUME declaration) then is copied into the bind-mounted location. After copying, all text files in the virtualenv that reference the old `/tmp` path are updated in-place via `grep -rIl | xargs sed -i` — this is required because Python venvs embed absolute paths in egg-links and `direct_url.json` files. pip and yarn download caches are persisted in named Docker volumes (`pip-cache`, `yarn-cache`) so repeated bench creation is faster. Frappe source is always cloned fresh from GitHub, so new benches always get the latest commit on the branch. `bench init` exits 0 even on failure, so `ffm create` explicitly checks for `apps/` after init.
- **Bind mount for bench data** — bench files live at `~/frappe/<bench>/workspace/frappe-bench/` on the host. The compose file bind-mounts `./workspace:/workspace`, placing the bench at `/workspace/frappe-bench` inside the container. This gives direct filesystem access: open `~/frappe/<bench>/workspace/` in VS Code for native editing, or use the generated `.devcontainer/devcontainer.json` to reopen in container for integrated terminal + Python debugging. `ffm delete` calls `down -v` (removes named volumes) then `os.RemoveAll(benchDir)` which deletes the workspace directory and all bench files.
- **Interactive bench picker** — `resolveBenchName(args, title)` in `pick.go` is called by every command that takes an optional bench name. Resolution order: (1) explicit `args[0]` if provided; (2) CWD auto-detection via `benchNameFromCWD()` — if the working directory is under `~/frappe/<name>/` and `<name>` is tracked in the state store, that name is returned silently without showing any UI; (3) `pickBench()`, which auto-selects if only one bench exists and otherwise shows a `huh.Select` list. Escape and ctrl+c both abort via a custom `huh.KeyMap`. CWD detection uses `filepath.Rel` against `config.BenchesDir()` and validates the extracted name against the state store.
- **zsh shell** — `ffm shell` execs into `zsh` for the `frappe` container (falls back to `bash` for other services like mariadb). The shell is pre-configured with zinit, zsh-autosuggestions, zsh-syntax-highlighting, history search, fixed key bindings, and starship — all baked into the image at `ffm create` time via heredocs in `Dockerfile.tmpl`.
- **Starship config** — the starship prompt and `.zshrc` are fixed and baked into the image at build time via shell heredocs in `Dockerfile.tmpl`. There is no preset selection; the config mirrors the host machine's `~/.config/starship.toml` and `~/.zshrc` (history, completion, key bindings, zinit, ffc completions). To change the prompt, edit `Dockerfile.tmpl` and rebuild.
- **ffc integration** — every new bench gets `ffc` (Foxmayn Frappe CLI) baked into the image via `curl | sh` (pre-built binary, installed to `/usr/local/bin`). After site creation, `ffm create` calls `Runner.GenerateAdminAPIKeys` which runs `bench execute frappe.core.doctype.user.user.generate_keys` inside the container (no HTTP needed), then writes `~/.config/ffc/config.yaml`. If this step fails, `ffm ffc [name]` can retry it on a running bench.
- **Claude/agent skills** — 60 [Frappe Claude skills](https://github.com/OpenAEC-Foundation/Frappe_Claude_Skill_Package) + the [ffc skill](https://github.com/nasroykh/foxmayn_frappe_cli/blob/main/skills/foxmayn-frappe-cli/SKILL.md) are pre-fetched into the Docker image at build time (`/opt/frappe-skills/` and `/opt/ffc-skill/`) and copied into `frappe-bench/.agents/skills/` and `frappe-bench/.claude/skills/` during `bench init`. On every `ffm start` / `ffm restart`, `runStart` checks for the presence of `.claude/skills/foxmayn-frappe-cli/SKILL.md` and runs the full copy if missing — this is an idempotent back-fill for benches created before this feature was added.
- **Private repo support** — `--apps` accepts short names, SSH URLs (`git@github.com:org/app.git`), HTTPS URLs, and `url@branch` or `name@branch` suffix to override the branch. `bench.ParseAppSpec` handles all forms. SSH agent forwarding is automatic when `SSH_AUTH_SOCK` is set on the host (`ForwardSSHAgent` in `ComposeData`). For private HTTPS repos, `--github-token` configures a temporary git credential helper inside the container (cleaned up after all `bench get-app` calls via `defer`).
- **Traefik reverse proxy** — a single `ffm-proxy` Traefik container is shared across all benches, providing `<name>.localhost` routing. The `frappe` service in each bench's compose file is attached to the `ffm-proxy` Docker network and carries Traefik labels. MariaDB and Redis stay on the default project network only. `ffm create` calls `proxy.EnsureNetwork()` before `docker compose up` so the external network always exists (preventing the "network declared as external, but could not be found" error even when the proxy is not running). The Traefik container uses `--providers.docker.network=ffm-proxy` to prevent the multi-NIC ambiguity bug. Container is configured entirely via CLI flags — no config file is written to disk. Uses `--restart=unless-stopped` to survive Docker daemon restarts.
- **External reverse proxy support** — `ffm set-proxy [name]` (or `ffm create --proxy-port / --proxy-host`) configures the bench for Caddy/Nginx/etc. deployments. It execs into the frappe container and sets three Frappe config values: `socketio_port` (global, must match the proxy's public port so the browser JS connects through the proxy rather than directly to port 9000), `use_ssl` (global, set to 1 for port 443 so Frappe generates https:// links and secure cookies), and per-site `host_name` (the public URL, used for link generation/OAuth/email). Dev server is restarted automatically. `--reset` restores direct-access defaults. `--print-caddy` / `--print-nginx` generate ready-to-paste configs using the actual ports from state.
- The `create` command is the most complex — it's a multi-step sequential pipeline with automatic rollback on failure. A named-return defer (`createErr error`) tears down containers (`docker compose down -v`) and removes the bench directory (`os.RemoveAll(benchDir)`) whenever the function returns a non-nil error, so retries always start from a clean state. The compose file existence is checked before calling `Down` because docker compose requires it; if failure happened before templates were written, only `RemoveAll` runs. State is saved to the store only on success (last step before the success message).

### Dependencies

- `github.com/spf13/cobra` — CLI framework
- `charm.land/lipgloss/v2` — terminal styling (list/status output)
- `github.com/charmbracelet/huh` — interactive prompts (create form, bench picker, delete confirmation)
- `github.com/charmbracelet/huh/spinner` — spinner animation during update download/install (subpackage of huh, no extra go.mod entry)
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
  docker-compose.yml     # generated per bench
  workspace/             # bind-mounted into container at /workspace; bench lives at workspace/frappe-bench/
    frappe-bench/
      .agents/skills/    # 60 Frappe Claude skills + ffc skill (installed at bench init; auto-filled on start if missing)
      .claude/skills/    # same skills, for Claude Code's skill discovery
  .devcontainer/
    devcontainer.json    # VS Code dev container config: opens /workspace/frappe-bench inside the running container
  Dockerfile             # generated per bench; extends frappe/bench:latest with zsh+zinit+starship+Go+ffc+pnpm+Claude Code (tools only)

~/.config/ffm/
  benches.json           # state file
  .update_check.json     # cached latest release tag (refreshed every 24 h by background goroutine)
```
