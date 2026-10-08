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
rendering (`internal/bench/hostuid_render_test.go`, `internal/bench/renderout_test.go`),
dashboard handlers (`internal/dashboard/handler_test.go`), the archive format
(`internal/archive/archive_test.go` — hostile tars built in memory), the archived app
source validator (`internal/archive/appsource_test.go`), the backup/restore
manifest and preflight gates (`internal/manager/backup_manifest_test.go`,
`internal/manager/backup_preflight_test.go`), app provenance and restore source planning
(`internal/manager/appsource_test.go` — runs the git probe script against real repos when
bash and git exist), the backup failure formatter
(`internal/manager/backup_failure_test.go`), the HTTP readiness wait and the
database-probe classifier (`internal/bench/waithttp_test.go`,
`internal/bench/dbprobe_test.go`) and the log tail helper
(`internal/manager/lifecycle_test.go`). All of those run without Docker. The create
pipeline and most of the CLI are still untested.

**Testing without Docker.** Every external command goes through `execx.Command` (never
`exec.Command` directly). Tests replace it with `internal/execx/fakeexec`: `fakeexec.Install(t,
Rule{Match: " ps ", Stdout: …})` scripts answers by argv substring and records every call
(`Calls`, `Called`); a package using it needs `func TestHelperProcess(t *testing.T) {
fakeexec.HelperMain() }`. `internal/manager/reconcile_pipeline_test.go` is the model for pipeline
tests. `internal/cli/testdata/help.golden` pins every command's usage and flags: after an intended
CLI change run `go test ./internal/cli -run TestHelpGolden -update` and review the diff.

`.github/workflows/test.yml` runs `go vet` + `go test` on every push and PR — before it
existed, neither ran anywhere. `.github/workflows/backup-roundtrip.yml` is a
weekly (Monday 03:17 UTC) and on-demand end-to-end proof: create → plant a marker row →
snapshot rollback → clone through the seed → backup → delete → restore
under a different name → assert the marker came back. It asserts on data, not exit codes,
because every interesting failure in this area exits 0.

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
    root.go               → registers all 36 subcommands; global --verbose and --non-interactive;
                            PersistentPreRunE runs the update check (skipped for 'update' and
                            for the hourly 'backup run-due');
                            Execute() dispatches the hidden __dashboard-daemon argv BEFORE cobra
                            parses anything, then runs cobra, then waitForUpdateCheck(); it
                            returns cobra's error unprinted (cobra already wrote it — printing
                            it again showed every failure twice)
    interactive.go        → isInteractive() / mustNotPrompt() / cancelled() / withSpinner().
                            $CI and $FFM_NON_INTERACTIVE imply non-interactive; $FFM_INTERACTIVE
                            forces prompting back on; --non-interactive always wins
    interactive_unix.go / interactive_windows.go → hasControllingTerminal() per platform
    create.go             → create flags + the interactive forms (runCreateForm,
                            runCreateFormFull); calls manager.Service.Create
    backup.go             → ffm backup: --out / --label / --no-files / --skip-space-check /
                            --vendor-apps.
                            Has subcommands, so `ffm backup list` is the subcommand, never a
                            bench called "list" — hence bench.ValidateNewName's reserved names
    backup_manage.go      → ffm backup list [bench] / ffm backup prune <bench> [--dry-run]
    backup_schedule.go    → ffm backup schedule [bench] (presets, --keep shorthand, per-tier
                            counts, --files, --off, --no-install) and ffm backup run-due
                            [--dry-run] [--log FILE] (rotated at 1 MiB, one old generation)
    backup_scheduler.go   → ffm backup scheduler install|uninstall|status|print; syncSchedulerJob
                            installs the crontab line with the first schedule, removes it with
                            the last
    restore.go            → ffm restore <archive> [name]: --dry-run / --no-files / --pin-apps /
                            --allow-missing-encryption-key / --encryption-key / --domain /
                            --no-ssl / --acme-email / --reallocate-ports / --web-port /
                            --socketio-port / --admin-password / --github-token /
                            --skip-migrate / --keep-on-failure / --skip-space-check /
                            --app <app>=<git-url>[@branch] (repeatable; frappe=… too).
                            arg0 is the ARCHIVE, arg1 the new bench name — so it does NOT
                            call resolveBenchName: the target must not exist yet
    reconcile.go          → ffm reconcile: --dry-run / --lan / --loopback / --ssh-agent /
                            --no-ssh-agent; behaviour in manager/reconcile.go
    recreate.go           → ffm recreate: --force / --reallocate-ports / --github-token /
                            --proxy-port / --proxy-host
    delete.go             → confirmation prompt (--force skips), then manager.Service.Delete
    list.go               → lipgloss table over manager.ListBenchViews; alias 'ls'
                            columns: NAME MODE DB STATUS PORT DOMAIN BRANCH
    start.go / stop.go / restart.go → ~25-line wrappers; restart carries --rebuild
    open.go               → ffm open (--mail / --traefik), ffm mail, ffm login (--user); --print
                            prints instead of opening, --json gives ffm.url/v1. browser.go opens
                            the URL, or fails where there is no desktop and the URL is printed
    console.go / db.go    → ffm console and ffm db (db-console, --export, --import [--migrate]);
                            interactive ones go through manager.Service.Interactive
    test.go / debug.go    → ffm test <app> [bench] (--module/--doctype/--test/--junit/--failfast) and
                            ffm debug on|off|(status)
    poweroff.go / clean.go → ffm poweroff; ffm clean (--dry-run/--yes/--json, --build-cache,
                            --dangling)
    snapshot.go           → ffm snapshot create|list|restore|delete (--name, --files, --yes,
                            --migrate, --json ffm.snapshots/v1)
    clone.go              → ffm clone <source> <new-name> (--no-files, --vendor-apps, --domain)
    agent.go              → ffm agent on|off [--read-only], bare = state; create takes --agent /
                            --agent-read-only
    doctor.go             → ffm doctor [bench] (--json ffm.doctor/v1, --notify)
    notify.go             → ffm notify add|list|remove|test (Slack URL / Telegram token from stdin)
    backup_key.go / backup_target.go / backup_verify.go → ffm backup key|target|pull|verify
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
    domain.go             → ffm domain list/add/remove (bare 'ffm domain' lists);
                            'add' takes --tls. Behaviour in manager/domains.go
    tunnel.go             → ffm tunnel flags; bare 'ffm tunnel' PRINTS STATUS, it does not enable
    tunnel_server.go      → ffm tunnel server list/add/set/remove/use
    dashboard.go          → ffm dashboard start/stop/status/logs; --daemon re-execs the binary
                            with the hidden __dashboard-daemon argv
    dashboard_daemon.go   → maybeRunDashboardDaemon(): that hidden entrypoint
    dashboard_sys_unix.go / dashboard_sys_windows.go → SysProcAttr for the detached daemon
    update.go             → ffm update: releases API, semver compare, atomic self-replace
    update_check.go       → background update notice (24 h TTL); returns immediately when
                            $FFM_NO_UPDATE_CHECK or $CI is set

  manager/                → the shared service layer. All bench operations live here. Output goes
                            through ProgressWriter, so the CLI and the dashboard's job runner share
                            one pipeline. Warnings and failure dumps still go straight to os.Stderr
                            (create, lifecycle, bench/docker.go), so in the dashboard they land in
                            dashboard.log, not in the job.
    service.go            → Service{Store, Verbose, mu, now}; serialises all store
                            access behind a mutex because the dashboard is concurrent; clock()
                            is the test-overridable time source
    benchlock.go          → lockBench: exclusive, non-blocking, and deliberately NOT re-entrant —
                            the dashboard shares one Service across requests and jobs, so
                            re-entry per Service would let a Delete pass a running Recreate.
                            Callers already holding the lock use backupLocked / deleteLocked
                            (run-due, Restore's rollback). Taken by Backup, Restore (target),
                            Delete, Recreate, PruneBackups and run-due. ErrBenchBusy,
                            ErrBenchStopped
    types.go              → CreateInput / RecreateInput / RestartInput / SetProxyInput /
                            ExecInput / CleanLogsInput / BenchView / BenchDetail / DashboardStats
    create.go             → THE create pipeline (see Key patterns). Also ReadSavedAcmeEmail /
                            SaveAcmeEmail. SkipAppInstall / SkipAssetBuild are set only by
                            Restore
    backup.go             → Service.Backup: reads site identity host-side, runs `bench backup`
                            into an in-container staging dir, streams members out
    restore.go            → Service.Restore: preflight → Create → data → reconcile. Fresh
                            bench only, so it borrows Create's rollback
    backup_manifest.go    → Header / Manifest / SiteInfo / AppInfo / Secrets + schema gates
    backup_preflight.go   → checkArchive / verifyMembers / scrubSecrets / checkNameFree /
                            honoursFileModes
    backup_appsource.go   → appProbeScript (one exec per app: remote, tracking branch, refs
                            containing HEAD) / applyProbe / cloneableRemote / vendorReason /
                            planAppSources / appSourceTarCmd
    restore_appsource.go  → planRestoreApps (where each app's code comes from) / planFrappe /
                            checkRestorePlan / checkBareNames (GitHub lookup before Create) /
                            installArchivedApps / syncAppsTxt
    backup_keys.go        → age encryption (internal/agecrypt): InitBackupKey / AddBackupRecipient
                            (config.BackupRecipientsFile — public keys only), encryptArchive
                            (<archive>.age + cleartext <archive>.age.header.json sidecar, plaintext
                            removed after), decryptForRestore (identity from --identity or
                            FFM_AGE_IDENTITY_FILE, into a 0700 temp dir under the backups dir).
                            ScanArchives reads encrypted archives' headers from the sidecar;
                            pruning removes both files
    backup_targets.go     → targets (internal/backuptarget: s3 via minio-go, sftp via x/crypto/ssh +
                            pkg/sftp with a PINNED host key only, local dir, rclone exec) stored in
                            config.BackupTargetsFile (0600). AddTarget tests before saving
                            (testTarget: put/list/get/delete). BackupInput.To forces Encrypt and
                            fails the backup when an upload fails; uploadArchive confirms both
                            files by listed size. Keys are "<bench>/<file>". pruneRemote reuses
                            prunable + selectRetained; run-due prunes remotely only after the
                            upload succeeded. PullArchive downloads into the bench backups dir
    doctor.go             → Doctor: read-only checks (containers + RestartCount, ping direct and via
                            Traefik, doctorJobsScript = DB connect + is_scheduler_inactive + per-queue
                            backlog and RQ workers listening on it, TLS leaf verified for the domain,
                            backup age vs EveryHours+2h, freeBytes, template version). Check.Status is
                            ok/warn/fail/skip; the CLI exits 1 on any fail
    backup_verify.go      → VerifyBackup: decrypt → archive.Extract (restore's guards) →
                            verifyMembers → dumpHasAuthTable (streams the gzip); with Restore,
                            verifyByRestore: Restore under verify-<hex> (prod: <name>.verify.invalid,
                            NoSSL), GET /api/method/ping, Delete in a defer whatever happened
    notify.go             → notifiers (internal/notify: webhook, ntfy, telegram, slack,
                            healthchecks) in config.NotifyFile (0600). Notify never fails the
                            caller. runOne sends the healthchecks start ping, then the result with
                            the error scrubbed by benchSecrets; skipped-stopped/busy count as OK
    backup_policy.go      → PresetPolicy / ParseEvery / ValidatePolicy / ApplyKeepShorthand
    backup_retention.go   → ScanArchives / selectRetained (tiered + RetentionFloor) /
                            PruneBackups; only readable, trigger=scheduled, same-bench archives
                            are ever candidates
    backup_schedule.go    → RunDue / runOne / ScheduleStatuses / SetBackupSchedule; RunState in
                            <backups>/<bench>/.schedule.json
    diskfree_unix.go / diskfree_windows.go → freeBytes(); no-op on Windows
    reconcile.go          → Reconcile: re-render docker-compose.yml from the record (composeDataFor)
                            and apply it with applyDomainChange (up -d, no data loss). The way
                            template fixes reach existing benches; recreate is destructive
    recreate.go           → backupBeforeDestroy, teardown + Create with stored inputs; reuses the
                            old port pair; re-enables the tunnel; a failed Create names the archive
    lifecycle.go          → Start / Stop / Delete / TeardownBenchFiles; Start also back-fills
                            skills, .mcp.json, the JS/Procfile patches, dev server, tunnel
    restart.go            → Restart; --rebuild re-renders the Dockerfile (carrying MatchHostUser),
                            rewrites wsgi.py for prod, re-applies the JS patches, rebuilds
    benches.go            → LiveStatus (running = the frappe container runs; partial = only other
                            containers do; stopped; unknown = docker unreachable) / ListBenchViews /
                            GetBenchDetail / DashboardOverview
    setproxy.go           → SetProxy, mode-aware reset defaults, Caddy/Nginx snippets
    domains.go            → DomainList/Add/Remove + composeDataFor (rebuilds ComposeData from
                            state) + applyDomainChange (re-render compose, `up -d`, no data loss).
                            Also tlsModeFor / prodNoSSL / hostLANIP / domainNameWarning
    tunnel_ops.go         → TunnelEnable / TunnelDisable
    proxy_ops.go          → ProxyStatus / ProxyStart / ProxyStop
    ffc.go                → SetupFFC: setupBenchAccess + ffc ping; refuses prod benches (no ffc there)
    claude_mcp.go         → writes workspace/frappe-bench/.mcp.json (ffc MCP server)
    clean_logs.go         → deletes old rows from 7 Frappe log tables
    exec.go               → Exec / ExecOrError: one-shot command in a container
    devtools.go           → SiteURL / MailURL / LoginURL (a sid URL: a credential, shown only on
                            request) / DBExport / DBImport (root password via stdin) /
                            Interactive; ensureDevMail points site_config's mail_server at the
                            bench's Mailpit (create, start, reconcile) unless one is already set
    devtest.go            → Test: run-tests on a dev site (sets allow_tests); the JUnit report is
                            written in the workspace and moved out through the bind mount
    debug.go              → Debug / DebugStatus: swaps the Procfile web line for a debugpy one on
                            container port 8005 (= host web port + 5, already published), keeping
                            the original in a "# ffm debug, original:" comment; refused on LAN
                            binds; writes .vscode/launch.json only when absent
    poweroff.go           → Poweroff: Stop every running bench, then the proxy
    snapshot.go           → CreateSnapshot / ListSnapshots / RestoreSnapshot / DeleteSnapshot.
                            Stored in <bench>/workspace/.ffm-snapshots/<name>/ (bind mount, so
                            bench backup writes in place); snapshot.json is written LAST and
                            marks a complete snapshot. Restore is IN PLACE (bench restore with
                            the root password on stdin) — the one exception to create-only
                            restore, see the vault's ADR-005. afterDatabaseSwap (optional
                            migrate, then clear-cache) is shared with DBImport
    clone.go              → Clone = Backup (temp dir, writtenTo) + Restore (new name, PinApps,
                            ReallocatePorts)
    seed.go               → seeds: a finished frappe-bench minus sites/<site>, logs, skills,
                            .ffm-* (pruneSeedTree; logs/ and config/pids are recreated EMPTY —
                            bench refuses set-config without them). Keyed by seedKey (image,
                            frappe repo+branch, toolchain, uid/gid, apps). freshSeed (< 7 days)
                            → Create copies it (cp -a, -c on darwin, --reflink=auto on linux) and
                            skips bench init, get-app and bench build. captureSeed runs after a
                            create from scratch (never during restore: SkipAppInstall), with the
                            pristine common_site_config.json Create saved after bench init
                            (.ffm-seed-common_site_config.json) — the bench's own copy carries
                            its db/redis/socketio settings. Off on Windows and with --no-seed /
                            FFM_NO_SEED; config.SeedsDir (FFM_SEEDS_DIR)
    agent.go              → setupBenchAccess: API keys (Administrator, or agent@<site> with System
                            Manager when Bench.Agent) → `ffc site add --api-secret-stdin --force`
                            (secret on stdin, other sites kept) → .mcp.json (--read-only when
                            AgentReadOnly) → AGENTS.md. Used by create, ffm ffc, SetAgent and
                            Start (when home/ffc/config.yaml is missing). SetAgent refuses LAN
                            binds and SSH forwarding, replaces a default admin password.
                            AGENTS.md is rewritten only while it starts with agentsMDMarker
    clean.go              → CleanPlan / Clean: orphans are ffm-<name> compose projects (volume
                            labels, image labels) with no record, no bench dir, no container (ps -a) and a free lock
    hostuser.go           → hostUserIDs() / composeUserIDs() backing --match-host-user
    jobs.go               → JobStore: async create/recreate/restart jobs persisted to jobs.json
    progress.go           → ProgressWriter + CLIProgress / DiscardProgress / BufferProgress

  archive/                → the ffm backup archive format. Plain tar; members compressed
                            individually. Header FIRST (cheap preflight), manifest LAST (its
                            absence IS the definition of a truncated archive). Stdlib only.
    archive.go            → Writer (atomic .partial→rename, 0600 from creation), PeekHeader
    safe_extract.go       → Extract/ExtractReader with traversal, type and size guards
    appsource.go          → ValidateAppSource: an archived app's .tar.gz must stay inside
                            <app>/ (links included) before tar unpacks it in the container

  dashboard/              → the /admin web UI. Stdlib only: html/template, embed, net/http.
    handler.go            → //go:embed templates + static, basic auth, rendering
    handler_actions.go    → POST endpoints for every bench operation
    handler_jobs.go       → job list/detail + SSE progress stream
    handler_logs.go       → SSE docker compose log stream
    handler_csrf.go       → HMAC double-submit CSRF tokens
    config.go             → dashboard.json; DefaultListenAddr 127.0.0.1:8787
    templates/ static/    → layout + 10 pages; admin.css, admin.js

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
                            ConfigureGitHubToken/CleanupGitHubToken, ExecStream (unbuffered
                            stdout — never ExecSilent for a multi-GB dump), CopyTo
    frappe_api.go         → Runner.GenerateAdminAPIKeys(siteName)
    port.go               → AllocatePorts (web 8000 / socketio 9000, +10 per bench, max 50) plus
                            ValidBenchPortPair / CheckTCPPortsFree for --web-port/--socketio-port,
                            and CheckBenchPortRangeFree, which probes all 13 published ports
                            (dev: web and socketio ranges plus Mailpit on web + 6)
    templates/
      dev/
        docker-compose.yml.tmpl  → 5 services (DB, redis×2, frappe, mailpit); DB conditional on DBType;
                                   bind-mounts ./workspace, one pip-cache volume that also holds
                                   uv's and yarn's caches (UV_CACHE_DIR, YARN_CACHE_FOLDER; a new
                                   volume over a path the image lacks is root-owned), Traefik labels
                                   for <name>.localhost, conditional SSH agent socket
        Dockerfile.tmpl          → full dev image: zsh/zinit/starship/ffc/pnpm/Claude Code +
                                   pre-fetched Frappe skills; optional HostUID/HostGID remap layer
      prod/
        docker-compose.yml.tmpl  → (v4: healthchecks on frappe/socketio/redis/DB, no-new-privileges
                                   on all, redis-queue volume + AOF, workers depend on frappe healthy,
                                   bench's gunicorn flags, flush_log_at_trx_commit 1 unless
                                   MariaDBFastCommit) 8 services (DB, redis-cache, redis-queue, frappe/gunicorn,
                                   socketio, worker-long, worker-short, scheduler) + an x-logging
                                   anchor (json-file 10m×3); Traefik labels + per-bench
                                   HTTP→HTTPS redirect; optional ./mysql-logs bind
        Dockerfile.tmpl          → minimal: frappe/bench + corepack pnpm + optional remap layer

  lock/                   → TryAcquire: non-blocking exclusive file lock (flock / LockFileEx via
                            golang.org/x/sys). The OS drops it when the process dies — no
                            stale-lock handling exists or is needed
  scheduler/              → the single crontab line for run-due: CurrentJob / Job.Line /
                            Merge / Remove / Find / Install / Uninstall / WindowsCommand. Tagged
                            `# ffm-backup-tick`; other crontab lines are kept byte for byte.
                            Install/Uninstall refuse a line whose --log (LogOf) is another
                            configuration's (ForeignJobError) unless forced: a run with another
                            FFM_CONFIG_DIR used to replace the real one's job
  proxy/proxy.go          → Traefik lifecycle: EnsureNetwork / IsNetworkPresent / Start / Stop /
                            IsRunning / Status / DashboardURL / SupportsHTTPS / EnsureHTTPS(email)
  tunnel/
    config.go             → Server + Config; ~/.config/ffm/tunnel.json (0o600)
    frpc.go               → frpc container via docker run (not compose); RenderFrpcToml (0o600)
  config/paths.go         → honours FFM_BENCHES_DIR / FFM_CONFIG_DIR / FFM_BACKUPS_DIR:
                            BenchesDir, BenchDir, StateFile, AcmeEmailFile, TunnelConfigFile,
                            DashboardConfigFile, DashboardPIDFile, DashboardLogFile, JobsFile,
                            BackupsDir / BenchBackupsDir / EnsureBenchBackupsDir, LocksDir /
                            BenchLockFile / BackupRunLockFile / BackupSchedulerLogFile /
                            BackupRunStateFile
  state/store.go          → JSON state store; Bench includes Mode, DBType, Domain, ProxyHost,
                            MatchHostUser, DomainAliases, AliasTLS, TLSMode, the prod tuning
                            knobs, Tunnel (*TunnelState), BackupSchedule (*BackupPolicy);
                            IsProd/IsDev/DBEngine/IsPostgres. Save is atomic (temp + fsync +
                            rename) and 0600 — the file holds every bench's passwords
  version/version.go      → build-time version variables
```

### Key patterns

- **`manager.Service` is the seam.** New behaviour goes in `internal/manager/`, not `internal/cli/`.
  The CLI passes `CLIProgress{}`; the dashboard passes a `BufferProgress` so the same pipeline can
  stream into an async job. `Service` serialises state-store access within a process behind a
  mutex; across processes, `state.Store`'s Add/Remove/Update hold `benches.json.lock` for their
  read-modify-write (`lock.Acquire`, 10 s), and `tunnel.json` goes through `tunnel.Update`.
  A new bench holds its bench lock for the whole of `Create`.
- **`bench.Runner`** is the low-level docker compose abstraction. Output modes: silent-capture
  (`ExecSilent`), capture-and-return (`LogsString`), verbose-conditional (`withOutput`),
  always-interactive (`composeWithIO`), stream-without-TTY (`ExecOutputInDir`), fire-and-forget
  (`ExecDetached`), and quiet-with-error-dump (`Build()` / `Run()` — capture in non-verbose mode,
  dump to stderr only on failure). `internal/proxy` and `internal/tunnel` bypass compose entirely
  and use raw `docker run`.
- **Port allocation** starts at web=8000 / socketio=9000, +10 per bench, capped at 50 benches.
  Each pair is checked against the state store and a live host probe. A dev bench publishes a
  **6-port range** (`WebPort`..`WebPort+5`; prod publishes one port each), so explicit `--web-port` values must be ≥10 apart —
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
  v15 and v16 branches — v16 rewrote `get_url()`). `PatchAuthenticateJs` makes **two** edits:
  (1) skip the Host-vs-Origin comparison when Origin is absent, and (2) drop the
  localhost/127.0.0.1 restriction on the `conf.default_site` branch of `get_site_name()`. Edit 2
  is what makes domain aliases possible at all — the browser connects to the namespace
  `/<frappe.boot.sitename>` and the server rejects it as "Invalid namespace" unless the resolved
  site matches, and without the patch the site is resolved from the request Host/Origin. Sound
  here because every ffm bench is single-site. `RealtimeAcceptsAnyHost` reports whether edit 2
  landed. `PatchProcfileWorker` wraps the dev `worker:` line in a self-restarting loop so an
  idle-Redis worker exit doesn't make honcho tear down the whole stack. All are idempotent;
  `Service.Start` re-applies the two realtime patches in **both** modes (prod needs them too),
  and `restart --rebuild` re-applies them as well. If you are wondering why a bench's
  `realtime/utils.js` differs from upstream, this is why.
- **Domain aliases** (`ffm domain add`, `ffm create --domain-alias`) route extra hostnames — LAN
  names like `erp.internal` — to a bench alongside its primary host. ffm only configures Traefik;
  pointing DNS at the host is the user's job and `domain add` prints the records to create.
  - **dev** folds aliases into the single existing router rule (`Host(a) || Host(b)`). Socket.IO
    is deliberately *not* put behind Traefik: the dev client appends `socketio_port` to the page
    origin, so it reaches the published 9000-9005 range directly, and moving it to :80 would
    break plain `http://localhost:<web-port>`. That port must be reachable from the LAN.
  - **prod** gives aliases their *own* routers (`<name>-alias`, `<name>-alias-socketio`), never
    merged into the primary rule — Traefik derives one ACME request per router, so a LAN-only
    name in the primary rule would fail the order and leave the real domain with no cert.
    Default is plain HTTP on the `web` entrypoint; `--tls` / `--alias-tls` opts into websecure +
    letsencrypt and is rejected for dev and for `--no-ssl` benches. The alias socketio router
    **strips** Origin (empty `customRequestHeaders` value deletes the header) rather than reusing
    the primary's fixed-Origin middleware, which would never match an alias Host, and injects
    `X-Frappe-Site-Name` so the namespace check passes even if the JS patch above did not land.
  - Applying a change re-renders `docker-compose.yml` and runs `up -d` — **not** `recreate`,
    which is destructive. Volumes and `./workspace` are untouched. Dev additionally relaunches
    honcho, since `up -d` replaces the frappe container and `bench start` is exec-driven.
  - Hostnames go through `bench.NormalizeDomain`, which is strict on purpose: the value lands in
    a Traefik label between backticks, so a backtick or quote would inject router config.
- **Compose re-rendering needs persisted inputs.** `state.Bench` records `TLSMode` and the prod
  tuning knobs (`GunicornWorkers`, `MariaDBBufferPool`, worker replicas, redis maxmem,
  `SlowQueryLog`) precisely so that `recreate` and `ffm domain` reproduce the bench instead of
  resetting it to defaults. Records written before these fields existed have zero values, which
  fall through to the create-time defaults. `TLSMode` also fixes an inference bug: deriving
  no-SSL from `ProxyHost`'s scheme is wrong once `ffm set-proxy --port 80` rewrites it.
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
- **Backup is logical, restore is a fresh bench.** `ffm backup` captures Frappe's own dump,
  the file tarballs, the site/common config and each app's git remote, branch and commit —
  not the venv, the built assets or, for an app that can be cloned back, its source. Measured on a frappe+erpnext dev bench that is 857 KiB versus
  ~1.7 GB for the workspace and 268 MB for the DB volume, and unlike a physical copy it
  restores across hosts, architectures and host uids. `ffm restore` therefore rebuilds by
  calling **`manager.Create`** (with `SkipAppInstall`/`SkipAssetBuild`) and then running
  `bench restore` into the new site, which is why it inherits the image build, uid remap,
  bench init, get-app, Traefik wiring and Create's rollback defer for free. It only ever
  creates a bench; there is no in-place overwrite, which is what makes the rollback sound.
  Seven Frappe/bench behaviours the pipeline exists to work around, each verified on a live bench:
  - `bench restore --admin-password` is a **no-op** — `install_app` returns early because the
    restored DB already lists frappe, so `after_install` never applies it. Restore runs
    `bench set-admin-password` separately and reconciles `state.Bench.AdminPassword`.
  - `encryption_key` is generated **lazily**, so one failed decrypt mints and persists a new
    key and orphans every Password field. Restore writes the archived key into site_config
    host-side *before anything reads the site*.
  - An encrypted backup keeps the name `…database.sql.gz` while being GPG/AES256, so members
    record an `Encoding` and readers must never infer it from the extension.
  - `bench restore` leaves `installed_apps` in site_config stale (it says `["frappe"]` while
    the DB says frappe+erpnext). Restore rewrites it, and writes the DB's list back into
    `state.Bench.Apps`, which drifts from the site's truth over a bench's life.
  - `bench get-app --branch <sha>` cannot work — git rejects a SHA where it wants a branch.
    `--pin-apps` does a post-clone `git checkout` + `bench setup requirements` instead, and
    the commits come from `git rev-parse HEAD`, never `sites/apps.json` (which records
    `commit_hash: null` for frappe itself on any ffm bench).
  - bench clones every app with **`--origin upstream`**, so there is no `origin` remote on a
    bench-made app. Backups before this was handled read only `remote.origin.url`, recorded
    no remote for any app, and restore fell back to `bench get-app <bare-name>`, which only
    searches github.com/frappe and /erpnext — so every private app failed several minutes in.
    Old archives still have no remotes: `ffm restore --app <app>=<url>` supplies them, and
    `checkBareNames` refuses up front (and in `--dry-run`) instead of failing mid-Create.
  - bare **`bench setup requirements [app]`** builds a bench `App`, which opens the directory
    with `git.Repo` and crashes on an app that is not a repository (`bench new-app --no-git`).
    The `--python` and `--node` halves do not, so restore and `--pin-apps` run those two.
  **App source.** An app that cannot be cloned back — no git, no remote another machine can
  reach (a local path, `file://`), no commits, or a HEAD on no remote branch or tag — has its
  working tree archived as `ffm-backup/apps/<app>.tar.gz` (minus node_modules, public/dist,
  bytecode, egg-info; `.git` kept). Only apps the restore needs are candidates: installed on
  the site or in the bench record. `--vendor-apps` forces others. Restore unpacks them after
  Create (frappe replaces the tree bench init cloned), adds them to `sites/apps.txt` and
  runs the two `setup requirements` halves. apps.txt order does not matter: hooks load in
  the DB's `installed_apps` order, which apps.txt only filters. Such an
  archive sets `min_reader_version` 2, so an older ffm refuses it rather than cloning bare
  names; archives without app source stay at 1. Restore precedence per app: `--app` override,
  archived source, recorded remote at `RemoteBranch`, bench-record spec, bare name. With
  `--github-token`, github.com SSH sources are rewritten to HTTPS (`withGitHubHTTPS`): the
  token never reaches an SSH clone, and a recorded remote is usually SSH. Unpacking
  archived source installs and runs its code — as the database dump already can.
  Archives live in `config.BackupsDir()`, deliberately **outside** the bench dir:
  `TeardownBenchFiles` runs `os.RemoveAll(b.Dir)`, so `ffm recreate` would otherwise destroy
  the backups that make it survivable. They hold credentials in plaintext at 0600, and
  `honoursFileModes` warns when the filesystem ignores that (a Windows drive under WSL2).

- **Scheduled backups: one hourly job, tiered retention, no second source of truth.**
  `ffm backup run-due`, run by a single tagged crontab line, backs up each bench whose
  `BackupSchedule` is due and then prunes it. Rules that are load-bearing:
  - run-due only **reads** `benches.json`. The policy is written by the user-invoked
    `ffm backup schedule`; last success is the newest `trigger=scheduled` archive on disk;
    the last attempt goes to `<backups>/<bench>/.schedule.json`. Keeping the
    hourly job read-only means it never waits on, or delays, interactive commands.
  - Pruning deletes only archives whose **header** says `trigger=scheduled` for that bench,
    only after a successful backup, and never below `RetentionFloor` (3). Manual archives,
    archives from an ffm older than scheduling (no `trigger`), foreign and unreadable files are
    never candidates.
  - The weekly tier counts the **current** week, so presets below weekly keep 5 weekly
    archives: with 4, coverage was measured at 18 days on some weekdays.
  - Daily and weekly buckets keep the newest archive **with attachments** when the bucket has
    one. Keeping plain "newest per day" under `--every 1h --files daily` left three weeks of
    database-only history, and pruning the one archive with attachments made the files
    cadence fire again at the next run (`--files weekly` ran ~daily).
  - A scheduled run never starts a stopped bench (`SkipIfStopped`), but `LiveStatus` "unknown"
    — docker unreachable, usually a cron PATH without it — is a **failure**, not a skip.
  - The crontab line carries the absolute ffm path, a PATH reaching docker and any
    FFM_*/DOCKER_* variables from install time; cron's environment is nearly empty.
  - `Recreate` re-adds the schedule after `Create` writes a fresh record; `Restore` never
    carries one over (the schedule travels in the archive's bench record but is ignored).

- **Waiting for the database means waiting from the frappe container.** `WaitForMariaDB` /
  `WaitForPostgres` exec *inside the database container* and talk to it over loopback, so they
  pass whenever the database process is up — including when nothing can reach it.
  Container-to-container traffic crosses the bridge and is filtered by the `FORWARD` chain, and
  Docker's per-bridge rules can go missing while the bridge stays up (an iptables reload, a
  firewall restart, a partially restored rule set). `manager.waitForDBReady` therefore runs both
  waits: the database-side one, then `Runner.WaitForDBFromFrappe`, a python3 TCP probe from the
  frappe container. Without the second, `create` and `backup` sail past the wait in 0s and fail
  much later with a Frappe error that blames the site.
- **`bench backup` reports every failure as corruption.** It wraps the operation in a bare
  `except Exception` and prints "Database or site_config.json may be corrupted" for a dropped
  packet, a full disk and a real corruption alike; the traceback is printed only under
  `--verbose`. `runBenchBackup` therefore always passes `--verbose` — the output is captured and
  discarded on success — and `benchBackupFailure` reduces it to Frappe's own message plus the
  traceback's final line. It never prints the frames by default: `frappe.get_traceback` runs
  `with_context=True`, so each frame dumps its locals, which includes the site's database
  password in clear. `ffm --verbose backup` shows the whole traceback, with the credentials
  redacted either way.

- **A published port that accepts is not a running server.** `docker-proxy` holds the host-side
  listener for every published port for as long as the container exists, so a TCP dial to
  `localhost:<web-port>` succeeds whether or not anything inside the container is listening — a
  bench whose `bench start` died still accepts on all six ports in its range. `WaitForHTTP`
  therefore issues a real request through `net/http` and requires a response; any status counts,
  redirects are not followed. `Service.Start` treats a failure as **fatal** and returns
  `webServerUnreachable`, which appends the tail of `bench-start.log` (dev) or the frappe
  container's log (prod) — honcho kills the whole stack when any process exits, so the log names
  the process that took it down. `create` and `restore` keep it non-fatal, with the same tail:
  failing there would trigger the rollback and destroy a bench that is otherwise complete.

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
  `.claude/skills/ffc-core/SKILL.md` is missing (ffc skills come from a pinned ffc release, `bench.FfcSkillsRef`), and also rewrites `.mcp.json` (wiring
  Claude Code to `ffc mcp --site <name>`) and re-applies the three patches above.
- **Private repos** — `--apps` takes short names, SSH URLs, HTTPS URLs, and `@branch` suffixes.
  SSH agent forwarding is opt-in with `--ssh-agent` (dev only), persisted as `Bench.SSHAgent`; the
  mount is `${SSH_AUTH_SOCK:-/dev/null}` so compose still works when the variable is unset (cron,
  sudo, the dashboard daemon). Before v0.8.1 it was automatic and a bare `${SSH_AUTH_SOCK}`, which
  made every later compose call without an agent fail. `--github-token`
  configures a credential helper; because bench init runs in a one-off `compose run` container
  that `exec`-based setup cannot reach, the credential setup is prepended into the bench init
  bash command instead. `--frappe-repo` maps to `bench init --frappe-path` with the same
  `@branch` syntax.
- **VPS tunnel** — frpc runs as a standalone container (`ffm-<name>-frpc`), not under compose, so
  it survives compose stops. `Service.Start` starts it when `b.Tunnel.Enabled` (warning and
  skipping if the named server profile is gone from tunnel.json); `TeardownBenchFiles` stops it
  before `compose down`.

### Undocumented-elsewhere gotchas

- **Published ports** bind to `Bench.PublishHost()`: `127.0.0.1` for new benches (`Bind: loopback`),
  all interfaces with `--lan`. Records without `Bind` keep their old behaviour: prod → loopback,
  dev → all interfaces. `--lan` is refused with the default admin password; a dev bench with domain
  aliases needs `--lan` (socket.io is reached on the published port). `ffm tunnel` refuses the
  default admin password unless `--allow-default-password`.
- **Prod workers** run `--queue long,default,short` and `--queue short,default`. Frappe enqueues to
  `default` unless told otherwise; before v0.8.1 nothing consumed it (confirmed live).
- Credentials default to `--admin-password admin` and `--db-password ffm123456`. Prod generates a
  random DB password when the default is left, and rejects the
  former. Failure paths interpolate `CombinedOutput` into errors, so a failed `bench new-site`
  can print the DB root password.
- **Every value in a `bash -c` string that did not come from ffm itself goes through
  `bench.ShellQuote`** — passwords, site names, app specs (`AppSpec.GetAppCmd`), the frappe
  branch and repo, proxy hosts. Passwords were once interpolated raw: a `$` in
  `--admin-password` was expanded, so the site got a different password than ffm recorded, and
  `--apps 'erpnext;cmd'` from the dashboard ran `cmd`. The GitHub token goes through
  `bench.GitCredentialsCmd`, which passes it to printf as an argument — inside the format
  string a `%` was read as a directive. The database password is also rendered into
  docker-compose.yml (double-quoted YAML, Compose `$` interpolation), so
  `bench.ValidateDBPassword` refuses `$`, `"`, `\`, whitespace and control characters; the
  Administrator password may be anything on one line not starting with `-` (set-admin-password
  takes it positionally). Restore's `checkManifestValues` applies the same two validators to
  archive credentials, so whatever create accepts also restores. A prod `--domain` becomes the
  site name, so `Create` runs it through `bench.NormalizeDomain` too (restore always did).
  `ffm exec` is the one deliberate exception: it runs the user's own command.
- **Toolchain per Frappe branch** (`internal/bench/toolchain.go`): `ToolchainFor` gives version-15
  Python 3.12 / Node 22 and everything else 3.14 / 24; `--python` / `--node` override, limited
  to `ImagePythons` / `ImageNodes` (what `BenchImageTag` ships — bump them together). Python
  reaches bench init as `--python python3.X` (pyenv shim). Node is switched in both Dockerfiles
  by `nvm alias default` + `nvm use` (PATH goes through `~/.nvm/current`), before `corepack`.
  Recorded as `Bench.Python` / `Bench.Node`; empty on older records, whose Dockerfile keeps the
  image default so a rebuild never swaps Node under existing node_modules. Restore reuses the
  archived pair only when the frappe branch is unchanged. The default branch is
  `bench.DefaultFrappeBranch` (`version-16`).
- **Pinned build inputs** live in `internal/bench/compose.go`: `BenchImageTag` (frappe/bench base
  image), `FrappeSkillsCommit` (skill pack) and `FfcSkillsRef` (ffc skills); Redis is
  `redis:8-alpine` in the templates and Traefik `traefik:v3.7` in `proxy.Image`. Do not pin Redis
  below the minor a host already runs: its RDB files may not load in an older Redis.
  `TestTemplatesPinTheirInputs` fails on floating tags. `restart --fresh` builds with
  `--pull --no-cache`.
- **Never `pkill -f '<text>'` from a `bash -c` whose command line contains `<text>`**: Linux pkill
  matches the shell itself and kills it. Dev server stop/start lives in `bench.DevServerRestartCmd` /
  `DevServerStartCmd`, which use the `'[h]oncho start'` pattern; until v0.9.1 every dev restart through
  domain changes, reconcile, set-proxy and tunnel killed honcho and never started it again.
- **Create mints the site's `encryption_key` right after `bench use`** (mintEncryptionKeyScript),
  before any server process exists. Frappe creates it lazily, and v16 request processes cache
  site_config for 60 s (`frappe/config.py`, `site_cache(ttl=60)`): the dev server minted a second
  key over the one the API-key script used, and ffc setup failed on every fresh v16 bench in
  v0.11.0 ("Encryption key is invalid"). The same cache means a site_config change made while a
  v16 server runs (ensureDevMail on reconcile) reaches it within a minute.
- **TeardownBenchFiles also removes every volume labelled with the bench's compose project**:
  `down --volumes` only knows the volumes the current file declares, so one a newer template
  dropped (yarn-cache, template 3) would outlive the bench.
- **Template versions.** `bench.TemplateVersion` names the compose and Dockerfile templates;
  create and reconcile record it on the bench, and `ffm list` flags benches below it. Any template
  change must bump it: `TestTemplateVersionTracksTemplates` fails until the new fingerprint is
  recorded. `benches.json` keeps a `.bak` of the previous version on every save.
- **Machine contract** (`internal/cli/jsonout.go`, `exitcode.go`): read commands take `--json` and
  print one object with a versioned `schema` (`ffm.list/v1` …). Only add fields within a version;
  anything else bumps it. Secrets only with `--show-secrets`. Exit codes come from typed errors
  (`state.ErrNotFound`, `ErrNoBenches`, `manager.ErrBenchBusy`, `ErrBenchStopped`,
  `*manager.PreflightError`, the cli `usageError` that `mustNotPrompt` and flag/arg errors return),
  mapped by `cli.ExitCode`. New error conditions should reuse these rather than plain strings.
- `make skills-init*` symlinks `.agents/skills/*` into `.claude/`, `.cursor/`, `.agent/` — this
  repo is itself skill-managed.
- `skills/ffm-dev/` and `skills/foxmayn-frappe-manager/` are copies of the same two skills under
  `.agents/skills/`. Edit the `.agents/skills/` file and copy it over; the two had drifted, with
  `skills/` missing the `--frappe-repo` / `--github-token` guidance.

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

Releases are created by pushing a `v*` tag. The GitHub Actions workflow (`.github/workflows/release.yml`) triggers GoReleaser, which cross-compiles for linux/darwin/windows on amd64/arm64, packages archives, and publishes the GitHub release with a `checksums.txt` and `checksums.txt.sig`.

**Signed releases.** `checksums.txt` is signed with Ed25519 (`internal/relsig`, `tools/relsign`,
GoReleaser `signs:`), using the `FFM_RELEASE_SIGNING_KEY` secret. `ffm update` refuses a release
without a valid signature from one of `relsig.ReleaseKeys`; `install.sh` checks it with OpenSSL 3
(`install.ps1` checks SHA-256 only). `ReleaseKeys` holds the signing key and an offline backup key
whose private half never touches a build machine. To rotate: sign with the backup (set it as the
secret), ship a release that adds a new backup key, keep old keys while their releases must verify.
`relsign sign` refuses a secret that the binaries being released cannot verify, and
`TestInstallScriptInSync` keeps `install.sh`'s `RELEASE_KEYS` equal to `ReleaseKeys`.

**Tag-to-release flow:**
```bash
git tag v0.1.0
git push origin v0.1.0
# → GitHub Actions runs GoReleaser → release assets published automatically
```

**Key files:**
- `.goreleaser.yaml` — build config: binary `ffm`, cmd `./cmd/ffm`, ldflags for version injection, archives named `ffm_<version>_<os>_<arch>`
- `.github/workflows/release.yml` — triggered on `v*` tags; uses `goreleaser-action@v6` with `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24: true`
- `install.sh` — `curl | sh` installer for Linux/macOS; detects OS/arch, downloads, verifies the signature (OpenSSL 3) and SHA256, installs to `/usr/local/bin` or `~/.local/bin`
- `install.ps1` — `irm | iex` installer for Windows; installs to `%LOCALAPPDATA%\Programs\ffm`, adds to user PATH, no admin rights required

## Runtime layout (on user's machine)

```
~/frappe/<bench-name>/
  docker-compose.yml     # generated per bench (dev: 5 services, prod: 8 services)
  Dockerfile             # dev: tools image; prod: minimal image
  frpc.toml              # written when tunnel is enabled (0o600 — contains token)
  mysql-logs/            # prod + MariaDB + --slow-query-log only
  home/                  # dev only (0700), bind-mounted: claude/ → CLAUDE_CONFIG_DIR,
                         #   ffc/ → ~/.config/ffc; created by bench.EnsureHomeDirs before compose up
  workspace/             # bind-mounted into container at /workspace
    .ffm-snapshots/      # ffm snapshot (0700)
    frappe-bench/
      AGENTS.md          # dev only: generated (agentsMDMarker) + CLAUDE.md "@AGENTS.md"
      .mcp.json          # Claude Code MCP config → `ffc mcp --site <bench>`
      .agents/skills/    # dev only: Frappe Claude skills + ffc skill
      .claude/skills/    # dev only: same skills for Claude Code
      sites/wsgi.py      # prod only: gunicorn entrypoint forcing single-site routing
  .devcontainer/         # dev only
    devcontainer.json

~/frappe/_backups/<bench-name>/
  <bench>_<UTC>.ffm.tar  # ffm backup archives (0600 in a 0700 dir; FFM_BACKUPS_DIR overrides)
  <bench>_<UTC>.auto.ffm.tar  # scheduled archives; only these are ever pruned
  .schedule.json         # last scheduled attempt (written by run-due only)

~/.config/ffm/
  benches.json           # state file (0600, atomic writes)
  backup-scheduler.log   # run-due results, rotated at 1 MiB (.1 kept)
  locks/                 # bench-<name>.lock and backup-run-due.lock (OS file locks)
  .update_check.json     # cached latest release tag (24 h TTL; skipped when $CI is set)
  .acme_email            # saved Let's Encrypt email
  tunnel.json            # VPS tunnel server profiles (0o600 — contains tokens)
  dashboard.json         # dashboard listen addr + admin password (0600, re-applied on every save)
  dashboard.pid          # PID of a backgrounded `ffm dashboard start --daemon`
  dashboard.log          # dashboard daemon log
  jobs.json              # async job state for the dashboard
```

All three roots are overridable: `FFM_BENCHES_DIR`, `FFM_CONFIG_DIR` and `FFM_BACKUPS_DIR`.
Seeds live in the user cache directory (`~/.cache/ffm/seeds`, `~/Library/Caches/ffm/seeds`;
`FFM_SEEDS_DIR`). Setting them per job is how
you isolate concurrent runs, so they do not share benches, ports or archives.
