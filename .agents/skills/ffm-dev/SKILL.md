---
name: ffm-dev
description: >
  Development guide for the ffm (Foxmayn Frappe Manager) Go codebase. Use this
  skill whenever working inside the foxmayn_frappe_manager repository — adding
  commands, modifying Docker templates, extending the bench runner, updating
  state/config logic, changing the proxy, fixing bugs, or refactoring. Trigger
  on any task involving internal/cli/, internal/bench/, internal/proxy/,
  internal/state/, internal/config/, the Makefile, or the embedded templates.
  Also trigger when the user mentions "ffm", "add a command to ffm", "new ffm
  subcommand", "Docker template", "bench runner", "compose template", or any
  development work on the Frappe Manager CLI itself (not *using* it — that's the
  foxmayn-frappe-manager skill).
---

# ffm Development Guide

Build and extend the ffm CLI — a Go tool for provisioning and managing Dockerized Frappe development and production benches.

## Tech Stack

| Component        | Library     | Import                                               |
| ---------------- | ----------- | ---------------------------------------------------- |
| CLI framework    | cobra       | `github.com/spf13/cobra`                             |
| Terminal styling | lipgloss v2 | `charm.land/lipgloss/v2`                             |
| Forms & prompts  | huh v1.0.0  | `github.com/charmbracelet/huh`                       |
| Spinner          | huh/spinner | `github.com/charmbracelet/huh/spinner`               |
| Key bindings     | bubbles     | `github.com/charmbracelet/bubbles/key`               |
| HTTP client      | resty v2    | `github.com/go-resty/resty/v2` (update command only) |

## Project Layout

```
cmd/ffm/main.go              → entrypoint, calls cli.Execute()

internal/
  cli/                        → cobra command definitions (one file per subcommand)
    root.go                   → root cobra command, NewRootCmd() + Execute()
                                registers all subcommands; global --verbose flag
                                (no -v shorthand; -v reserved for --version);
                                PersistentPreRunE runs update check on every command
    create.go                 → multi-step bench provisioning pipeline with auto-rollback;
                                supports --mode dev (default) and --mode prod;
                                supports --db-type mariadb (default) and --db-type postgres;
                                interactive form (runCreateFormFull) asks mode first, then DB
                                engine, then shows dev or prod follow-up fields
    delete.go                 → teardown with huh confirmation prompt (aliases: rm, remove)
    list.go                   → table output with live docker status via lipgloss;
                                shows MODE (dev/prod), DB (maria/pg), and domain columns
    start.go                  → docker compose up + (dev only) skill install + dev server start;
                                prod: compose command: entries run services automatically
    stop.go                   → docker compose stop
    restart.go                → delegates to runStop + runStart; --rebuild flag rewrites
                                Dockerfile from template (mode-aware) + rebuilds image
    shell.go                  → interactive docker exec: zsh for dev, bash for prod;
                                --exec for non-interactive one-shot commands
    logs.go                   → docker compose logs streaming
    status.go                 → per-container status + credentials display; shows mode, DB engine, domain
    ffc.go                    → generate API keys + write ffc config inside container (dev only)
    proxy.go                  → ffm proxy subcommand group: start / stop / status
    setproxy.go               → configure socketio_port / use_ssl / host_name inside the
                                container for reverse-proxy deployments; works for dev and prod;
                                dev: restarts bench start automatically; prod: prints ffm restart hint;
                                --reset uses mode-aware defaults (dev: 9000/no-ssl; prod: 443/ssl)
    pick.go                   → resolveBenchName() + benchNameFromCWD() + pickBench()
                                CWD auto-detection: if inside ~/frappe/<name>/, returns name
                                without UI; falls back to interactive picker otherwise
    update.go                 → self-update via GitHub Releases API
    update_check.go           → background update notification (24h TTL cache)

  bench/                      → core bench logic, no CLI concerns
    bench.go                  → ValidateName(), ProjectName(), ContainerName()
    app.go                    → AppSpec type + ParseAppSpec(): parses --apps values
    compose.go                → renders docker-compose.yml and Dockerfile from //go:embed
                                templates (dev/ or prod/ based on Mode); also writes
                                .devcontainer/devcontainer.json (dev only)
    docker.go                 → Runner: all docker compose interactions (Build, Run, Up,
                                Down, Exec, ExecSilent, ExecOutputInDir, PS, Logs, etc.)
    frappe_api.go             → Runner.GenerateAdminAPIKeys(): bench execute + JSON parse
    port.go                   → port allocation: webBase=8000, socketIOBase=9000, step=10
    templates/
      dev/
        docker-compose.yml.tmpl → 4-service dev compose (frappe+honcho, mariadb OR postgres,
                                   redis×2); DB service is conditional on ComposeData.DBType
        Dockerfile.tmpl         → full dev image: zsh+zinit+starship+Go+ffc+pnpm+Claude Code
      prod/
        docker-compose.yml.tmpl → 7-service prod compose (gunicorn, socketio, workers,
                                   scheduler, mariadb OR postgres, redis×2); DB service,
                                   healthcheck, and frappe depends_on are conditional on
                                   ComposeData.DBType; Traefik labels for domain routing +
                                   Let's Encrypt; per-bench HTTP→HTTPS redirect
        Dockerfile.tmpl         → minimal prod image (no dev tools)

  proxy/
    proxy.go                  → all Traefik lifecycle: EnsureNetwork(), Start(), Stop(),
                                IsRunning(), Status(), SupportsHTTPS(), EnsureHTTPS();
                                createContainer() for HTTP-only; createContainerHTTPS()
                                adds port 443, letsencrypt volume, ACME resolver flags

  config/
    paths.go                  → BenchesDir(), ConfigDir(), BenchDir(), StateFile(),
                                AcmeEmailFile(), EnsureDataDir();
                                respects FFM_BENCHES_DIR, FFM_CONFIG_DIR

  state/
    store.go                  → JSON-file state store (~/.config/ffm/benches.json);
                                Bench struct with Mode/Domain/DBType/IsProd()/IsDev()/
                                DBEngine()/IsPostgres();
                                Store with Load/Save/Add/Remove/Get/Update

  version/
    version.go                → build-time ldflags: Version, Commit, Date
```

## Adding a New Command

This is the most common task. Follow the pattern established by every existing command.

### 1. Create the file

Create `internal/cli/<command_name>.go` in package `cli`. Use snake_case for filenames, kebab-case for the command's `Use` field.

### 2. Follow this structure

```go
package cli

import (
    "fmt"

    "github.com/spf13/cobra"

    "github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
    "github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newMyCmd() *cobra.Command {
    var someFlag string

    cmd := &cobra.Command{
        Use:   "my-command [name]",
        Short: "One-line description",
        Long:  `Longer description with usage notes.`,
        Args:  cobra.MaximumNArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            // 1. Resolve bench name (CWD auto-detect or interactive picker if omitted)
            name, err := resolveBenchName(args, "Select a bench")
            if err != nil {
                return err
            }
            return runMyCommand(name, someFlag)
        },
    }

    cmd.Flags().StringVar(&someFlag, "some-flag", "default", "Description")
    return cmd
}

func runMyCommand(name, someFlag string) error {
    // 2. Load state
    store := state.Default()
    b, err := store.Get(name)
    if err != nil {
        return err
    }

    // 3. Check mode if behavior differs
    if b.IsProd() {
        // prod-specific behavior
    }

    // 4. Create runner for docker compose operations
    runner := bench.NewRunner(b.Name, b.Dir, verbose)

    // 5. Do work (exec into container, update state, etc.)
    out, err := runner.ExecSilent("frappe", "bash", "-c", "some command")
    if err != nil {
        return fmt.Errorf("my-command: %w\n%s", err, out)
    }

    fmt.Printf("Done: %s\n", out)
    return nil
}
```

### 3. Register in root.go

Add `newMyCmd()` to the `root.AddCommand(...)` call in `NewRootCmd()`:

```go
root.AddCommand(
    // ... existing commands ...
    newMyCmd(),
)
```

### Key patterns to follow

- **Factory functions, not `init()`**: Commands use `newXxxCmd()` factory functions, registered manually in `root.go`. This is different from the ffc codebase which uses `init()` — do NOT use `init()` here.
- **`verbose`** is the package-level flag in `root.go` — use it directly in any command file. Controls docker compose output visibility.
- **`resolveBenchName(args, title)`** — call this in every command that takes an optional `[name]` argument. Resolution order: (1) `args[0]` if provided; (2) `benchNameFromCWD()` — silently returns the bench name if CWD is under `~/frappe/<name>/` and that name is in the state store; (3) `pickBench()` — auto-selects if only one bench exists, otherwise shows a `huh.Select` list.
- **`RunE`, not `Run`**: Always return errors; cobra handles printing and exit code 1.
- **`bench.Runner`** wraps all docker compose operations. Always construct via `bench.NewRunner(b.Name, b.Dir, verbose)`.
- **Mode-aware behavior**: Load state with `store.Get(name)`, then use `b.IsProd()` / `b.IsDev()` to branch. Examples: `shell.go` uses `bash` for prod, `zsh` for dev; `start.go` skips bench start for prod; `setproxy.go` blocks for prod.

## bench.Runner — Docker Compose Abstraction

The `Runner` type in `internal/bench/docker.go` is the central abstraction for all Docker interactions. Every CLI command that touches Docker goes through it.

### Four output modes

| Method                                       | IO                                         | Use case                                              |
| -------------------------------------------- | ------------------------------------------ | ----------------------------------------------------- |
| `ExecSilent(service, args...)`               | Captures output, returns `(string, error)` | Commands that need to parse output or should be quiet |
| `ExecOutputInDir(service, workdir, args...)` | Streams stdout/stderr to terminal, no TTY  | `ffm shell --exec` (non-interactive, show output)     |
| `ExecInDir(service, workdir, args...)`       | Full stdin/stdout/stderr + TTY             | `ffm shell` (interactive shell)                       |
| `Exec(service, args...)`                     | Full IO, no workdir override               | Interactive exec without workdir                      |

### Quiet-with-error-dump pattern

`Build()` and `Run()` capture output in quiet mode and only dump it to stderr when the command fails. This keeps `ffm create` output minimal by default while surfacing failure details:

```go
func (r *Runner) Build() error {
    // In verbose mode: stream to terminal
    // In quiet mode: capture, only dump on failure
    if r.Verbose {
        cmd.Stdout = os.Stdout
        cmd.Stderr = os.Stderr
        return cmd.Run()
    }
    if out, err := cmd.CombinedOutput(); err != nil {
        os.Stderr.Write(out) // dump captured output on failure
        return err
    }
    return nil
}
```

### Other Runner methods

| Method                                                 | Description                                          |
| ------------------------------------------------------ | ---------------------------------------------------- |
| `Up()`                                                 | `docker compose up -d`                               |
| `Down(removeVolumes)`                                  | `docker compose down --remove-orphans [-v]`          |
| `Start()`                                              | `docker compose start` (existing stopped containers) |
| `Stop()`                                               | `docker compose stop`                                |
| `Logs(follow, service)`                                | `docker compose logs [-f] [service]`                 |
| `PS(format)`                                           | `docker compose ps [--format X]`                     |
| `WaitForMariaDB(pw, timeout, writer)`                  | Polls until MariaDB accepts connections              |
| `WaitForPostgres(pw, timeout, writer)`                 | Polls until PostgreSQL accepts connections (`pg_isready`) |
| `ConfigureGitHubToken(token)` / `CleanupGitHubToken()` | Temp git credential helper                           |
| `GenerateAdminAPIKeys(siteName)`                       | Runs bench execute + parses JSON API keys            |

## State Store

`internal/state/store.go` — flat JSON file at `~/.config/ffm/benches.json`.

### Bench struct

```go
type Bench struct {
    Name          string    `json:"name"`
    Dir           string    `json:"dir"`
    WebPort       int       `json:"web_port"`
    SocketIOPort  int       `json:"socketio_port"`
    FrappeBranch  string    `json:"frappe_branch"`
    AdminPassword string    `json:"admin_password"`
    DBPassword    string    `json:"db_password"`
    // DBType is "mariadb" or "postgres". Empty is treated as "mariadb" (backward compatibility).
    DBType        string    `json:"db_type,omitempty"`
    SiteName      string    `json:"site_name"`
    Apps          []string  `json:"apps"`
    ProxyHost     string    `json:"proxy_host,omitempty"`
    // Mode is "dev" or "prod". Empty is treated as "dev" (backward compatibility).
    Mode          string    `json:"mode,omitempty"`
    // Domain is the public domain for production benches (e.g. "erp.example.com").
    Domain        string    `json:"domain,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
}

func (b Bench) IsProd() bool    { return b.Mode == "prod" }
func (b Bench) IsDev() bool     { return b.Mode != "prod" }
func (b Bench) DBEngine() string { /* returns "postgres" or "mariadb"; empty = "mariadb" */ }
func (b Bench) IsPostgres() bool { return b.DBEngine() == "postgres" }
```

Empty `Mode` is treated as `"dev"` and empty `DBType` is treated as `"mariadb"` for backward compatibility with existing state files.

### Store operations

```go
store := state.Default()           // uses config.StateFile()
benches, _ := store.Load()         // returns []Bench (empty slice if no file)
store.Add(benchRecord)             // append + save
store.Remove("name")               // filter out + save
b, _ := store.Get("name")         // lookup by name
store.Update("name", func(b *state.Bench) {
    b.ProxyHost = "https://..."    // mutate in place
})                                 // save after mutation
used, _ := store.UsedPorts()       // map[int]bool of assigned ports
```

Not concurrency-safe across processes — fine for a CLI.

## Embedded Templates

Templates live at `internal/bench/templates/` and are embedded at compile time via `//go:embed`. Changes require rebuild.

### Template layout

```
internal/bench/templates/
  dev/
    docker-compose.yml.tmpl   // 4-service dev compose
    Dockerfile.tmpl           // full image: zsh+zinit+starship+Go+ffc+pnpm+Claude Code
  prod/
    docker-compose.yml.tmpl   // 7-service prod compose with Traefik labels
    Dockerfile.tmpl           // minimal image (no dev tools)
```

### ComposeData (template input)

```go
type ComposeData struct {
    Name            string   // bench name, used as Traefik router ID
    Mode            string   // "dev" or "prod"
    BenchDir        string
    WebPort         int      // first port in the range (e.g. 8000)
    WebPortEnd      int      // last port in range (WebPort + 5); dev only
    SocketIOPort    int      // first port (e.g. 9000)
    SocketIOPortEnd int      // SocketIOPort + 5; dev only
    DBType          string   // "mariadb" or "postgres"; controls which DB service is rendered
    DBRootPassword  string   // root password for whichever DB engine is selected
    ForwardSSHAgent bool     // dev only: mount SSH_AUTH_SOCK into container
    Domain          string   // prod only: public domain for Traefik routing
    NoSSL           bool     // prod only: skip TLS labels, route on HTTP entrypoint
}
```

### Rendering

```go
bench.WriteCompose(benchDir, data)       // → docker-compose.yml (selects dev or prod template)
bench.WriteDockerfile(benchDir, data)     // → Dockerfile (selects dev or prod template)
bench.WriteDevcontainer(benchDir, data)   // → .devcontainer/devcontainer.json (dev only)
```

`WriteCompose` and `WriteDockerfile` select the template based on `data.Mode`. `WriteDevcontainer` uses `json.MarshalIndent` (not a template).

### Prod compose services

| Service              | Command                        | Notes                                                           |
| -------------------- | ------------------------------ | --------------------------------------------------------------- |
| `mariadb` or `postgres` | default entrypoint          | Conditional on `{{.DBType}}`; has healthcheck                   |
| `redis-cache`        | default                        |                                                                 |
| `redis-queue`        | default                        |                                                                 |
| `frappe`             | `bench serve --port 8000`      | gunicorn; depends_on db service (healthy); Traefik labels       |
| `socketio`           | `node apps/frappe/socketio.js` | Traefik labels for `/socket.io` path                            |
| `worker-long`        | `bench worker --queue long`    | No ports                                                        |
| `worker-short`       | `bench worker --queue short`   | No ports                                                        |
| `scheduler`          | `bench schedule`               | No ports                                                        |

Traefik labels on `frappe` and `socketio` route `{{.Domain}}` on `websecure` (HTTPS) by default, or `web` (HTTP) when `{{.NoSSL}}` is true. Per-bench HTTP→HTTPS redirect is applied via labels (no global redirect, which would break dev benches).

The `frappe` service's `depends_on` is conditional: `postgres: condition: service_healthy` when `{{.DBType}}` is `"postgres"`, otherwise `mariadb: condition: service_healthy`. The volume name (`postgres-data` vs `mariadb-data`) is also conditional.

## Config Paths

`internal/config/paths.go` — all path resolution is here.

| Function          | Default                      | Env override             |
| ----------------- | ---------------------------- | ------------------------ |
| `BenchesDir()`    | `~/frappe`                   | `FFM_BENCHES_DIR`        |
| `ConfigDir()`     | `~/.config/ffm`              | `FFM_CONFIG_DIR`         |
| `BenchDir(name)`  | `~/frappe/<name>`            | inherits from BenchesDir |
| `StateFile()`     | `~/.config/ffm/benches.json` | inherits from ConfigDir  |
| `AcmeEmailFile()` | `~/.config/ffm/.acme_email`  | inherits from ConfigDir  |
| `EnsureDataDir()` | creates both dirs            | —                        |

## Proxy Package

`internal/proxy/proxy.go` — manages the shared Traefik container.

The proxy is a standalone `docker run` container (not compose). Key constants:

```go
NetworkName      = "ffm-proxy"         // shared Docker bridge network
ContainerName    = "ffm-proxy"         // Traefik container name
Image            = "traefik:3"
WebPort          = 80
HTTPSPort        = 443
DashboardPort    = 8080                // bound to 127.0.0.1 only
LetsEncryptVolume = "ffm-letsencrypt"  // named volume for ACME state
```

All Traefik configuration is CLI flags — no config file on disk.

### Key functions

| Function             | Description                                                                                                                                                                                                   |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `EnsureNetwork()`    | Creates the `ffm-proxy` Docker network if missing                                                                                                                                                             |
| `Start()`            | Starts Traefik on HTTP only (dev benches)                                                                                                                                                                     |
| `Stop()`             | Stops and removes the Traefik container                                                                                                                                                                       |
| `IsRunning()`        | Returns true if the proxy container is running                                                                                                                                                                |
| `SupportsHTTPS()`    | Inspects container port bindings for :443                                                                                                                                                                     |
| `EnsureHTTPS(email)` | State machine: ensures proxy supports HTTPS + ACME. Creates with HTTPS if not running; recreates (stop→rm→create) if running without port 443. No-op if already supports HTTPS. Also calls `EnsureNetwork()`. |

`EnsureHTTPS` is called by `ffm create --mode prod` (unless `--no-ssl`). It does not add a global HTTP→HTTPS redirect so dev benches on port 80 are unaffected. Each prod bench applies its own per-bench redirect via compose Traefik labels.

`createContainerHTTPS(email)` extends the standard container flags with:
- `-p 0.0.0.0:443:443`
- `-v ffm-letsencrypt:/letsencrypt`
- `--entrypoints.websecure.address=:443`
- `--certificatesresolvers.letsencrypt.acme.httpchallenge=true`
- ACME email and storage flags

## The `create` Command — Pipeline Architecture

`create.go` is the most complex command: a multi-step sequential pipeline with **automatic rollback on failure**. Understanding it is essential.

### Interactive form flow

`RunE` checks `cmd.Flags().Changed("mode")`:
- No `--mode` → `runCreateFormFull()`: asks mode first (dev/prod), then shows relevant follow-up fields
- `--mode dev` without `--frappe-branch`/`--apps` → `runCreateForm()`: dev-only form (branch + apps)
- Any other combination: skip forms, use flags directly

`runCreateFormFull()` for prod shows: domain, admin password (password echo mode), ACME email (empty = no-ssl), Frappe version, apps.

### Rollback mechanism

```go
func runCreate(...) (createErr error) {
    // Named return — defer sees whatever error the function returns
    defer func() {
        if createErr == nil { return }
        // 1. docker compose down -v (if compose file exists)
        // 2. os.RemoveAll(benchDir)
    }()
    // ... steps that may return error ...
}
```

### Step sequence (dev vs prod differences)

| Step                  | Dev                  | Prod                                                     |
| --------------------- | -------------------- | -------------------------------------------------------- |
| Validate + port alloc | same                 | same                                                     |
| Create bench dir      | same                 | same                                                     |
| **Proxy**             | `EnsureNetwork()`    | `EnsureHTTPS(email)` if SSL; `EnsureNetwork()` if no-ssl |
| Site name             | `<name>.localhost`   | `<domain>`                                               |
| Write templates       | Mode: "dev"          | Mode: "prod", Domain, NoSSL                              |
| **devcontainer**      | written              | skipped                                                  |
| Build image           | same                 | same (smaller)                                           |
| Bench init            | + skills copy        | base only (no skills in prod image)                      |
| Up                    | same                 | same                                                     |
| Wait DB               | WaitForMariaDB or WaitForPostgres (based on --db-type) | same              |
| Configure site        | db_host + db_port set per engine; socketio_port=9000 | db_host + db_port set per engine; socketio_port=443 (ssl) or 80 (no-ssl) |
| New site              | `--mariadb-root-password` (mariadb) or `--db-type postgres --db-root-username postgres` (postgres) | same |
| **Developer mode**    | enabled              | skipped                                                  |
| **Host name**         | only if --proxy-host | always set to https/http://domain                        |
| Install apps          | same                 | same                                                     |
| **Build assets**      | skipped              | `bench build`                                            |
| **Dev server**        | nohup bench start    | skipped (compose handles it)                             |
| **HTTP wait**         | polls localhost:port | skipped                                                  |
| **ffc setup**         | runs                 | skipped                                                  |
| Save state            | + Mode, Domain       | same                                                     |

State is saved **only on success** (last step before the success message).

### Production with an existing reverse proxy (Caddy/Nginx already on 80/443)

Use `--no-ssl`. `EnsureNetwork()` is called instead of `EnsureHTTPS()`, so Traefik does not try to bind port 443. The prod compose exposes `WebPort` and `SocketIOPort` on the host. The user then points their existing Caddy/Nginx at those ports.

After creation, `socketio_port` is set to `80` (the `--no-ssl` default). If the user's proxy handles HTTPS, run:

```bash
ffm set-proxy <name> --host <domain>   # sets socketio_port 443, use_ssl 1, host_name https://<domain>
ffm restart <name>                     # apply to all prod services
```

`ffm set-proxy` works for both dev and prod benches. See the `set-proxy` section below.

### ACME email persistence

`saveAcmeEmail(email)` writes to `~/.config/ffm/.acme_email`. `readSavedAcmeEmail()` reads it. First prod+SSL bench requires `--acme-email`; subsequent ones auto-read the saved email. Interactive form pre-fills from saved email.

## Interactive Forms (huh v1.0.0)

### Escape key handling

In huh v1.0.0, Escape is not mapped to Quit by default. When you need Escape to abort a form, create it explicitly with a custom keymap:

```go
import "github.com/charmbracelet/bubbles/key"

km := huh.NewDefaultKeyMap()
km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))

err = huh.NewForm(
    huh.NewGroup(
        huh.NewSelect[string]().Title("...").Options(opts...).Value(&chosen),
    ),
).WithKeyMap(km).Run()

if errors.Is(err, huh.ErrUserAborted) { ... }
```

### Interactive bench picker

`pickBench()` in `pick.go` loads the state store, auto-selects if only one bench exists, and shows a `huh.Select` list otherwise. The keymap includes Escape via a custom `huh.KeyMap`.

### CWD auto-detection

`benchNameFromCWD()` in `pick.go`:
1. Gets `os.Getwd()`
2. Computes `filepath.Rel(config.BenchesDir(), cwd)` — the path relative to `~/frappe/`
3. Extracts the first component (bench name) via `strings.SplitN(rel, string(filepath.Separator), 2)[0]`
4. Validates it is not empty, not `.`, not `..`, and exists in the state store

Called by `resolveBenchName()` when `args` is empty, before showing the interactive picker.

## AppSpec — `--apps` Value Parsing

`internal/bench/app.go` — the `ParseAppSpec(raw, frappeBranch)` function handles all `--apps` formats:

| Input                             | Source                    | Branch                   |
| --------------------------------- | ------------------------- | ------------------------ |
| `erpnext`                         | `erpnext`                 | `frappeBranch` (default) |
| `erpnext@version-16`              | `erpnext`                 | `version-16`             |
| `git@github.com:org/app.git`      | full SSH URL              | `""` (git default)       |
| `git@github.com:org/app.git@main` | SSH URL without `@main`   | `main`                   |
| `https://github.com/org/app`      | full HTTPS URL            | `""`                     |
| `https://github.com/org/app@main` | HTTPS URL without `@main` | `main`                   |

`GetAppCmd()` returns the `bench get-app` shell command. `DisplayName()` extracts the repo name for log output.

## Error Handling

- **Wrap errors with context**: `fmt.Errorf("bench init: %w", err)` — preserves the error chain.
- **Never log and return**: Return the error; let cobra's `RunE` handle it.
- **`ExecSilent` pattern**: Capture output, check error, include output in the error message:
  ```go
  if out, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
      return fmt.Errorf("operation: %w\n%s", err, out)
  }
  ```
- **Non-fatal warnings**: Use `fmt.Fprintf(os.Stderr, "warning: %v\n", err)` and continue.

## Self-Update System

Two files cooperate:

- **`update.go`** — `ffm update`: fetches latest GitHub release, compares semver, downloads platform-specific archive, atomically replaces the running binary.
- **`update_check.go`** — background check on every command (except `update`): reads/writes `~/.config/ffm/.update_check.json` (24h TTL). `runUpdateCheck()` starts a goroutine; `waitForUpdateCheck()` blocks up to 2s before process exit.

Both share `newerThan()` and `parseSemver()` helpers.

## Build & Release

```bash
make           # → tidy + build + install (default goal)
make ship      # → same as above explicitly (tidy → build → install)
make build     # → ./bin/ffm with version ldflags
make install   # → $GOPATH/bin/ffm + creates ~/.config/ffm/
make vet       # → go vet ./...
make fmt       # → gofmt -w .
make tidy      # → go mod tidy + verify
make clean     # → removes ./bin/ffm
```

Version is injected via `-ldflags` into `internal/version` (Version, Commit, Date).

### Releasing

Tag-to-release: push a `v*` tag → GitHub Actions runs GoReleaser → cross-compiled binaries published.

```bash
git tag v0.2.0
git push origin v0.2.0
```

### Skills sync

After modifying skills under `.agents/skills/`, sync to other agent directories:

```bash
make skills-init   # symlinks .agents/skills/* → .claude/ .cursor/ .agent/
```

## Checklist for New Features

1. Create `internal/cli/<name>.go` with the `newXxxCmd()` factory pattern
2. Register in `root.go` via `root.AddCommand(newXxxCmd())`
3. If it needs docker compose operations, use `bench.Runner` methods
4. If it needs persistent state, use `state.Store` (Load/Save/Add/Remove/Get/Update)
5. If behavior differs by mode, check `b.IsProd()` / `b.IsDev()` after `store.Get(name)`
6. If it modifies compose or Dockerfile templates, edit `internal/bench/templates/dev/` or `prod/`
7. If it touches config paths, update `internal/config/paths.go`
8. Run `make ship` (or `make vet && make fmt && make build`) to verify
9. Update CLAUDE.md and the skill files under `.agents/skills/`
10. Run `make skills-init` to sync skills across agent directories

## Testing

**There are no tests yet.** No test framework or test files exist. To verify changes, create a test bench with `ffm create testbench --verbose` and manually exercise the new functionality.
