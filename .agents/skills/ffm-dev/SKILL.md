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

Build and extend the ffm CLI — a Go tool for provisioning and managing Dockerized Frappe development benches.

## Tech Stack

| Component        | Library     | Import                                                    |
| ---------------- | ----------- | --------------------------------------------------------- |
| CLI framework    | cobra       | `github.com/spf13/cobra`                                  |
| Terminal styling | lipgloss v2 | `charm.land/lipgloss/v2`                                  |
| Forms & prompts  | huh v1.0.0  | `github.com/charmbracelet/huh`                            |
| Spinner          | huh/spinner | `github.com/charmbracelet/huh/spinner`                    |
| Key bindings     | bubbles     | `github.com/charmbracelet/bubbles/key`                    |
| HTTP client      | resty v2    | `github.com/go-resty/resty/v2` (update command only)      |

## Project Layout

```
cmd/ffm/main.go              → entrypoint, calls cli.Execute()

internal/
  cli/                        → cobra command definitions (one file per subcommand)
    root.go                   → root cobra command, NewRootCmd() + Execute()
                                registers all subcommands; global --verbose flag
                                (no -v shorthand; -v reserved for --version);
                                PersistentPreRunE runs update check on every command
    create.go                 → multi-step bench provisioning pipeline with auto-rollback
    delete.go                 → teardown with huh confirmation prompt (aliases: rm, remove)
    list.go                   → table output with live docker status via lipgloss
    start.go                  → docker compose up + skill install + dev server start
    stop.go                   → docker compose stop
    restart.go                → delegates to runStop + runStart; --rebuild flag rewrites
                                Dockerfile from template + rebuilds image
    shell.go                  → interactive docker exec (zsh) + --exec for non-interactive
    logs.go                   → docker compose logs streaming
    status.go                 → per-container status + credentials display
    ffc.go                    → generate API keys + write ffc config inside container
    proxy.go                  → ffm proxy subcommand group: start / stop / status
    setproxy.go               → configure socketio_port / use_ssl / host_name for reverse
                                proxy deployments; --print-caddy / --print-nginx snippets
    pick.go                   → resolveBenchName() + pickBench() interactive bench selector
    update.go                 → self-update via GitHub Releases API
    update_check.go           → background update notification (24h TTL cache)

  bench/                      → core bench logic, no CLI concerns
    bench.go                  → ValidateName(), ProjectName(), ContainerName()
    app.go                    → AppSpec type + ParseAppSpec(): parses --apps values
    compose.go                → renders docker-compose.yml and Dockerfile from //go:embed
                                templates; also writes .devcontainer/devcontainer.json
    docker.go                 → Runner: all docker compose interactions (Build, Run, Up,
                                Down, Exec, ExecSilent, ExecOutputInDir, PS, Logs, etc.)
    frappe_api.go             → Runner.GenerateAdminAPIKeys(): bench execute + JSON parse
    port.go                   → port allocation: webBase=8000, socketIOBase=9000, step=10
    templates/
      docker-compose.yml.tmpl → Go text/template, embedded via //go:embed
      Dockerfile.tmpl         → Go text/template, embedded via //go:embed

  proxy/
    proxy.go                  → all Traefik lifecycle: EnsureNetwork(), Start(), Stop(),
                                IsRunning(), Status(); createContainer() with docker run

  config/
    paths.go                  → BenchesDir(), ConfigDir(), BenchDir(), StateFile(),
                                EnsureDataDir(); respects FFM_BENCHES_DIR, FFM_CONFIG_DIR

  state/
    store.go                  → JSON-file state store (~/.config/ffm/benches.json);
                                Bench struct; Store with Load/Save/Add/Remove/Get/Update

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
            // 1. Resolve bench name (interactive picker if omitted)
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

    // 3. Create runner for docker compose operations
    runner := bench.NewRunner(b.Name, b.Dir, verbose)

    // 4. Do work (exec into container, update state, etc.)
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
- **`resolveBenchName(args, title)`** — call this in every command that takes an optional `[name]` argument. It auto-selects if only one bench exists, or shows an interactive picker via `pickBench()`.
- **`RunE`, not `Run`**: Always return errors; cobra handles printing and exit code 1.
- **`bench.Runner`** wraps all docker compose operations. Always construct via `bench.NewRunner(b.Name, b.Dir, verbose)`.

## bench.Runner — Docker Compose Abstraction

The `Runner` type in `internal/bench/docker.go` is the central abstraction for all Docker interactions. Every CLI command that touches Docker goes through it.

### Four output modes

| Method | IO | Use case |
|--------|-----|----------|
| `ExecSilent(service, args...)` | Captures output, returns `(string, error)` | Commands that need to parse output or should be quiet |
| `ExecOutputInDir(service, workdir, args...)` | Streams stdout/stderr to terminal, no TTY | `ffm shell --exec` (non-interactive, show output) |
| `ExecInDir(service, workdir, args...)` | Full stdin/stdout/stderr + TTY | `ffm shell` (interactive shell) |
| `Exec(service, args...)` | Full IO, no workdir override | Interactive exec without workdir |

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

| Method | Description |
|--------|-------------|
| `Up()` | `docker compose up -d` |
| `Down(removeVolumes)` | `docker compose down --remove-orphans [-v]` |
| `Start()` | `docker compose start` (existing stopped containers) |
| `Stop()` | `docker compose stop` |
| `Logs(follow, service)` | `docker compose logs [-f] [service]` |
| `PS(format)` | `docker compose ps [--format X]` |
| `WaitForMariaDB(pw, timeout, writer)` | Polls until MariaDB accepts connections |
| `ConfigureGitHubToken(token)` / `CleanupGitHubToken()` | Temp git credential helper |
| `GenerateAdminAPIKeys(siteName)` | Runs bench execute + parses JSON API keys |

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
    SiteName      string    `json:"site_name"`
    Apps          []string  `json:"apps"`
    ProxyHost     string    `json:"proxy_host,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
}
```

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

### ComposeData (template input)

```go
type ComposeData struct {
    Name                string   // bench name, used as Traefik router ID
    BenchDir            string
    WebPort             int      // first port in the range (e.g. 8000)
    WebPortEnd          int      // last port in range (WebPort + 5)
    SocketIOPort        int      // first port (e.g. 9000)
    SocketIOPortEnd     int      // SocketIOPort + 5
    MariaDBRootPassword string
    ForwardSSHAgent     bool     // mount SSH_AUTH_SOCK into container
}
```

### Rendering

```go
bench.WriteCompose(benchDir, data)       // → docker-compose.yml
bench.WriteDockerfile(benchDir, data)     // → Dockerfile
bench.WriteDevcontainer(benchDir, data)   // → .devcontainer/devcontainer.json
```

`WriteDevcontainer` uses `json.MarshalIndent` (not a template); the other two use `text/template`.

When modifying templates, test by creating a new bench with `ffm create testbench --verbose` and inspecting the generated files at `~/frappe/testbench/`.

## Port Allocation

`internal/bench/port.go` — sequential scan with host probe.

```
webBase      = 8000
socketIOBase = 9000
portStep     = 10
maxBenches   = 50
```

Each bench gets a port pair: (8000, 9000), (8010, 9010), (8020, 9020), etc. Each pair allocates a *range* of 6 ports (e.g. 8000–8005) in the compose file for Frappe's multi-process dev server.

`AllocatePorts()` checks both the state store and probes the host via `net.Listen` to detect external conflicts.

## Config Paths

`internal/config/paths.go` — all path resolution is here.

| Function | Default | Env override |
|----------|---------|--------------|
| `BenchesDir()` | `~/frappe` | `FFM_BENCHES_DIR` |
| `ConfigDir()` | `~/.config/ffm` | `FFM_CONFIG_DIR` |
| `BenchDir(name)` | `~/frappe/<name>` | inherits from BenchesDir |
| `StateFile()` | `~/.config/ffm/benches.json` | inherits from ConfigDir |
| `EnsureDataDir()` | creates both dirs | — |

## Proxy Package

`internal/proxy/proxy.go` — manages the shared Traefik container.

The proxy is a standalone `docker run` container (not compose). Key constants:

```go
NetworkName   = "ffm-proxy"   // shared Docker bridge network
ContainerName = "ffm-proxy"   // Traefik container name
Image         = "traefik:3"
WebPort       = 80
DashboardPort = 8080           // bound to 127.0.0.1 only
```

All Traefik configuration is CLI flags — no config file on disk. The `createContainer()` function in proxy.go is the single source of truth for the Traefik setup.

Each bench's compose template declares the frappe service as attached to the `ffm-proxy` network with Traefik labels for `<name>.localhost` routing. `proxy.EnsureNetwork()` is called before `docker compose up` during `ffm create` so the external network always exists (even if the proxy container isn't running).

## The `create` Command — Pipeline Architecture

`create.go` is the most complex command: a multi-step sequential pipeline with **automatic rollback on failure**. Understanding it is essential.

### Rollback mechanism

```go
func runCreate(...) (createErr error) {
    // Named return — defer sees whatever error the function returns
    defer func() {
        if createErr == nil { return }
        // 1. docker compose down -v (if compose file exists)
        // 2. os.RemoveAll(benchDir)
    }()
    // ... 11+ steps that may return error ...
}
```

The named-return pattern (`createErr error`) means the defer sees whatever error the function returns without any extra assignment at each `return` site. The compose file existence is checked before calling `Down` because docker compose requires it.

### Step sequence

1. Validate name + check for duplicates
2. Allocate ports (`bench.AllocatePorts`)
3. Create bench directory
4. Ensure ffm-proxy network (`proxy.EnsureNetwork`)
5. Write compose + Dockerfile + devcontainer templates
6. Build Docker image (tools only — cached after first build)
7. Create workspace directory
8. `docker compose run --rm` bench init (to `/tmp`, copy to bind-mount, patch venv paths)
9. `docker compose up -d`
10. Wait for MariaDB
11. Configure common_site_config
12. `bench new-site`
13. Enable developer mode
14. (Optional) Set host_name for proxy mode
15. (Optional) Configure GitHub token for private repos
16. Install apps (loop: `bench get-app` + `bench install-app`)
17. Start dev server via nohup
18. Wait for HTTP
19. Generate API keys + write ffc config
20. Save state

State is saved **only on success** (last step before the success message).

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

## AppSpec — `--apps` Value Parsing

`internal/bench/app.go` — the `ParseAppSpec(raw, frappeBranch)` function handles all `--apps` formats:

| Input | Source | Branch |
|-------|--------|--------|
| `erpnext` | `erpnext` | `frappeBranch` (default) |
| `erpnext@version-16` | `erpnext` | `version-16` |
| `git@github.com:org/app.git` | full SSH URL | `""` (git default) |
| `git@github.com:org/app.git@main` | SSH URL without `@main` | `main` |
| `https://github.com/org/app` | full HTTPS URL | `""` |
| `https://github.com/org/app@main` | HTTPS URL without `@main` | `main` |

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
5. If it modifies compose or Dockerfile templates, edit `internal/bench/templates/*.tmpl`
6. If it touches config paths, update `internal/config/paths.go`
7. Run `make vet && make fmt && make build` to verify
8. Update README.md, CLAUDE.md, and the skill files under `.agents/skills/`
9. Run `make skills-init` to sync skills across agent directories

## Testing

**There are no tests yet.** No test framework or test files exist. To verify changes, create a test bench with `ffm create testbench --verbose` and manually exercise the new functionality.
