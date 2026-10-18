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
dashboard handlers, security and the parity pages (`internal/dashboard/handler_test.go`, `pages_test.go`,
`security_test.go`; every one isolates FFM_CONFIG_DIR / FFM_BENCHES_DIR / FFM_BACKUPS_DIR to
a temp dir and fakes docker), the server's headers and listen check
(`internal/server/server_test.go`), the archive format
(`internal/archive/archive_test.go` — hostile tars built in memory), the archived app
source validator (`internal/archive/appsource_test.go`), the backup/restore
manifest and preflight gates (`internal/manager/backup_manifest_test.go`,
`internal/manager/backup_preflight_test.go`), app provenance and restore source planning
(`internal/manager/appsource_test.go` — runs the git probe script against real repos when
bash and git exist), the backup failure formatter
(`internal/manager/backup_failure_test.go`), the HTTP readiness wait and the
database-probe classifier (`internal/bench/waithttp_test.go`,
`internal/bench/dbprobe_test.go`) and the log tail helper
(`internal/manager/lifecycle_test.go`), the restore sources and Frappe backup import
(`internal/manager/restore_source_test.go` — an httptest TLS server and hostile responses —,
`restore_target_test.go`, `restore_dump_test.go`, `restore_dump_real_test.go`,
`restore_frappe_test.go`, `restore_frappe_pipeline_test.go`), the compose secret file and rendering
(`internal/bench/secret_test.go`), the no-secret-on-argv tests
(`internal/manager/secrets_argv_test.go`) and rotation (`internal/manager/rotate_test.go`).
The site administration commands are covered by `internal/manager/siteadmin_test.go` (typed values, key guard,
masking, every command's secret off argv, the Mailpit marker) and `internal/cli/siteadmin_test.go` (the real
command tree: ffm.config/v1, ffm.users/v1, ffm.mail/v1, bare `ffm mail` still routing to Mailpit).
All of those run without Docker. The create
pipeline and most of the CLI are still untested.

**Testing without Docker.** Every external command goes through `execx.Command` (never
`exec.Command` directly). Tests replace it with `internal/execx/fakeexec`: `fakeexec.Install(t,
Rule{Match: " ps ", Stdout: …})` scripts answers by argv substring and records every call
(`Calls`, `Called`); a package using it needs `func TestHelperProcess(t *testing.T) {
fakeexec.HelperMain() }`. `internal/manager/reconcile_pipeline_test.go` is the model for pipeline
tests; `internal/manager/projectmount_test.go` runs the whole create pipeline on it (a `Rule` can also
`Mkdir`, `Write` a file and `Copy` one, e.g. to snapshot docker-compose.yml at a given call).
`internal/cli/testdata/help.golden` pins every command's usage and flags: after an intended
CLI change run `go test ./internal/cli -run TestHelpGolden -update` and review the diff.

`.github/workflows/test.yml` runs `go vet` + `go test` on every push and PR — before it
existed, neither ran anywhere. `.github/workflows/backup-roundtrip.yml` is a
weekly (Monday 03:17 UTC) and on-demand end-to-end proof: create → plant a marker row →
snapshot rollback → clone through the seed → backup → delete → restore
under a different name → assert the marker came back. It asserts on data, not exit codes,
because every interesting failure in this area exits 0. `.github/workflows/arm64-smoke.yml`
(on demand, monthly, `ubuntu-24.04-arm`) checks that every pinned image has an arm64 manifest,
creates a dev bench, asserts `uname -m` is aarch64 in the frappe container and runs `ffm doctor`;
whether the dev image's curl installers (ffc, Claude Code) ship arm64 builds is only proven by
that run, not by anything local.

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
    root.go               → registers all 48 subcommands; global --verbose and --non-interactive;
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
    backup_scheduler.go   → ffm backup scheduler install|uninstall|status|print (install and print take
                            --backend cron|systemd|launchd|schtasks); syncSchedulerJob installs the
                            job with the first schedule, removes it with the last, both through
                            scheduler.Install / scheduler.Uninstall
    restore.go            → ffm restore <archive|url|target://NAME/BENCH[/FILE]> [name]: --dry-run /
                            --no-files / --pin-apps /
                            --allow-missing-encryption-key / --encryption-key / --domain /
                            --no-ssl / --acme-email / --reallocate-ports / --web-port /
                            --socketio-port / --admin-password / --github-token /
                            --skip-migrate / --keep-on-failure / --skip-space-check /
                            --app <app>=<git-url>[@branch] (repeatable; frappe=… too) /
                            --keep-download / --header-url. arg0 is the ARCHIVE, arg1 the new
                            bench name — so it does NOT call resolveBenchName: the target must
                            not exist yet. --frappe-backup <db.sql.gz> (a string flag; the one
                            positional is then the NAME, required) switches to Frappe's own
                            backup files, with --files / --private-files / --config / --apps
                            (StringSlice) / --mode / --frappe-branch / --db-type; those flags are
                            usage errors without it, and --identity / --pin-apps /
                            --encryption-key / --keep-download / --header-url are usage errors
                            with it. The CLI is the only caller that sets AllowRemote, and it
                            passes a signal context so Ctrl-C stops a download
    rotate.go             → ffm rotate-credentials [bench] [--db-root] [--admin] [--show-secrets] [--json
                            ffm.credentials/v1]; behaviour in manager/rotate.go. Progress goes to stderr
                            with --json
    secretenv.go          → envFallback / preferEnv: FFM_ADMIN_PASSWORD, FFM_DB_PASSWORD,
                            FFM_GITHUB_TOKEN (then GITHUB_TOKEN), FFM_ENCRYPTION_KEY stand in for the
                            matching flags (create, restore, recreate, app add); a flag wins
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
    worktree.go           → ffm worktree add <branch> (--from/--base/--path/--name/--no-files/--json) /
                            remove <branch|path|bench> (--force/--keep-worktree/--delete-branch/--no-backup) /
                            list (--json ffm.worktrees/v1). worktree_hook.go: ffm worktree hook
                            create|remove|print (Claude Code WorktreeCreate/WorktreeRemove; create prints
                            the path as the only stdout line, everything else goes to stderr)
    agent.go              → ffm agent on|off [--read-only], bare = state; create takes --agent /
                            --agent-read-only
    doctor.go             → ffm doctor [bench] (--json ffm.doctor/v1, --notify)
    mcp.go                → ffm mcp [--allow-write] [--allow-prod]: stdio MCP server (mark3labs/mcp-go) for
                            a host-side agent. Tools call manager.Service and return the --json
                            documents (listDoc/statusDoc/snapshotsDoc, shared with list/status/snapshot);
                            os.Stdout is pointed at stderr so ffm output never reaches the protocol;
                            write tools refuse prod benches without --allow-prod; one write tool at a time
    project.go            → ffm up / trust / run (ffm.yaml): loadProject, ensureTrusted (trust prompt per
                            file hash), runPostUpdateHooks after ffm app update; ffm up prints the mount
    mcp_install.go        → ffm mcp install|uninstall --client … [--allow-write --allow-prod --print --yes]
    mcp_confirm.go        → delete's elicitation confirmation (HMAC-bound, single-use, 10 min request
                            state; same design as ffc's)
    site.go               → ffm site [bench] / migrate / maintenance on|off / scheduler on|off|pause|resume
    config.go             → ffm config list|get|set|unset [bench] [key] [value]: the bench is the optional FIRST
                            argument, so configArgs splits by argument count; set takes --string / --json /
                            --stdin / --force; ffm.config/v1 (secret-looking values only with --show-secrets)
    user.go               → ffm user list|add|disable|enable|password; ffm.users/v1; the password comes from
                            secretinput.go secretInput (--password-stdin, $FFM_USER_PASSWORD, hidden prompt twice;
                            never a flag)
    mailset.go            → ffm mail set|show|unset, hung under the existing `ffm mail` (bare = Mailpit inbox, so
                            set/show/unset are in bench.reservedNames); ffm.mail/v1 never has the SMTP password
    app.go                → ffm app list|add|remove|update (update ≠ `ffm update`, which is ffm's
                            self-update; --to-branch, --dry-run, --no-rollback, --yes)
    notify.go             → ffm notify add|list|remove|test (Slack URL / Telegram token from stdin)
    backup_key.go / backup_target.go / backup_verify.go → ffm backup key|target|pull|verify
    shell.go / logs.go    → the last commands that drive bench.Runner directly.
                            shell: zsh for dev frappe, bash otherwise; --exec for one-shot,
                            --service to target another container. logs: --follow defaults TRUE
    status.go             → per-container status + credentials; also prints a clean-logs tip
                            for benches older than 7 days
    clean_logs.go         → ffm clean-logs: --days 30 / --dry-run / --yes
    scale.go              → ffm scale [bench] [--gunicorn-workers N] [--worker-long-replicas N]
                            [--worker-short-replicas N] [--dry-run] [--json ffm.scale/v1]; no count flag prints
                            the current counts; ErrProdOnly becomes a usage error. Behaviour in manager/scale.go
    trim.go               → ffm trim [bench] [--tables] [--database] [--dry-run] [--yes] [--no-snapshot]: dry
                            run listing unless --yes, a typed confirm at a terminal; behaviour in manager/trim.go
    pick.go               → resolveBenchName() + benchNameFromCWD() + pickBench();
                            benchPickKeyMap() is what makes Esc abort every form in the package
    ffc.go                → ffm ffc: regenerate Frappe API keys + ffc config; --host [--force] adds the bench to
                            the HOST's ffc instead, reusing the keys in home/ffc/config.yaml (behaviour in
                            manager/ffc.go FfcHost; secret on stdin, never argv)
    proxy.go              → ffm proxy start/stop/status (bare 'ffm proxy' shows status)
    setproxy.go           → ffm set-proxy flags (--port default 443, --host, --no-ssl, --reset,
                            --print-caddy, --print-nginx); behaviour in manager/setproxy.go
    domain.go             → ffm domain list/add/remove (bare 'ffm domain' lists);
                            'add' takes --tls. Behaviour in manager/domains.go
    tunnel.go             → ffm tunnel flags; bare 'ffm tunnel' PRINTS STATUS, it does not enable
    tunnel_server.go      → ffm tunnel server list/add/set/remove/use
    dashboard.go          → ffm dashboard start/stop/status/logs; --daemon re-execs the binary
                            with the hidden __dashboard-daemon argv. --tls-cert / --tls-key /
                            --insecure-http / --allow-exec are persisted in dashboard.json (a flag
                            only overrides when given; =false clears); server.CheckListen runs
                            before anything is saved or spawned
    dashboard_daemon.go   → maybeRunDashboardDaemon(): that hidden entrypoint (reads TLS/exec
                            settings from dashboard.json, never argv)
    dashboard_sys_unix.go / dashboard_sys_windows.go → SysProcAttr for the detached daemon (Windows:
                            HideWindow + CreationFlags DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP so the
                            daemon outlives the console — UNVERIFIED on a real Windows host)
    update.go             → ffm update: releases API, semver compare, atomic self-replace;
                            removeStaleBinary() (called at the start of Execute) deletes the
                            ffm.exe.old a Windows update leaves behind, best effort
    update_check.go       → background update notice (24 h TTL); returns immediately when
                            $FFM_NO_UPDATE_CHECK or $CI is set

  manager/                → the shared service layer. All bench operations live here. Output goes
                            through ProgressWriter, so the CLI and the dashboard's job runner share
                            one pipeline. Warnings and failure dumps (create, start, restart, domains, tunnel,
                            TeardownBenchFiles, the Build/Run dump via Runner.ErrOut) go to pw.Stderr(), so a
                            dashboard job carries them. The one deliberate os.Stderr write left is rotate.go's
                            tellNewValue: a freshly generated password that exists nowhere else, which a masked
                            job record would destroy.
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
                            bench only, so it borrows Create's rollback. Two front doors, one
                            core: Restore (archive: resolve source, decrypt, extract, verify)
                            and restoreFrappeBackup both fill a restoreRun {manifest, staging,
                            restoreMembers (the db / public / private files `bench restore` is
                            handed; an empty half is not restored)} and call restoreCore
                            (gates, plan, Create, applySiteConfig, runBenchRestore,
                            reconcileAfterRestore)
    restore_source.go     → parseRestoreSource (local path / https URL / target://NAME/BENCH[/FILE];
                            http:// and user:pass@ are refused, other schemes point at
                            `ffm backup target add`) / resolveRestoreSource (a 0700 .download-*
                            dir under config.BackupsDir, removed afterwards unless KeepDownload) /
                            httpGet (https-only redirects, MaxDownloadBytes cap = 64 GiB default
                            checked on Content-Length and on the bytes, stall timeout, truncation
                            check, restoreHTTPClient is the test hook) / redactURL. An age .age
                            URL archive fetches <url>.header.json (or HeaderURL) FIRST and checks
                            the name before the archive. Targets go through pullArchiveTo (shared
                            with PullArchive, which keeps its old behaviour)
    restore_frappe.go     → FrappeBackupInput import: inspectFrappeFiles (every file checked
                            without Docker: dump, file tars via archive/tar for traversal and
                            truncation, config) / chooseDBEngine / chooseFrappeBranch /
                            planDumpApps (an app the dump lists with no --apps / --app source is a
                            Problem before Create) / the manifest built for restoreCore. Config
                            contributes ONLY encryption_key (+ backup_encryption_key)
    restore_dump.go       → inspectDump: one streaming pass over a Frappe dump (mysqldump and
                            pg_dump layouts): engine, __Auth / tabUser, the `tabInstalled
                            Application` rows, the "-- begin frappe metadata" block
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
    site.go               → SiteStatus (maintenance_mode from site_config, `scheduler status
                            --format json`), SiteMigrate/migrateLocked, setMaintenance, setScheduler
                            (on/off = enable/disable, pause/resume keep the user's setting)
    apps.go               → ListApps (listAppsScript), AppAdd (get-app; the app's name is what get-app
                            added to apps.txt, not the repo's), AppRemove (snapshot first, uninstall-app
                            --yes --no-backup --force, remove-app), restartAppProcesses (dev honcho /
                            prod app containers, then ping), withoutApp
    update.go             → Update: planUpdate (branch+commit per app, dirty trees refused except
                            ffmPatchedPaths) → backup (db) + snapshot → maintenance on + scheduler pause
                            → applyUpdate (revertFfmPatches first: bench's pull --rebase refuses ffm's
                            edits; bench update --pull --reset, or switchBranchScript (shallow git fetch +
                            checkout -B, not bench switch-to-branch) + pruneStaleScript + changeToolchain +
                            old env moved to env.ffm-prev + setup env; requirements, clear-cache, migrate,
                            build, reapplyPatches, restart) → maintenance off + resume. Failure
                            → rollbackUpdate (git checkout -B branch commit, prune, toolchain back + env.ffm-prev
                            moved back, requirements, clear-cache, restoreSnapshotLocked, build). Snapshot
                            create/restore have *Locked variants
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
    diskfree_unix.go / diskfree_windows.go → freeBytes() (both implemented)
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
                            GetBenchDetail / DashboardOverview(Views). StatusAll: one `docker ps -a
                            --filter label=com.docker.compose.project` for all benches, same
                            classification (benchStatus), unknown also when the compose file is gone.
                            Used ONLY by the view paths (list, home, MCP list); StatusTTL (dashboard sets
                            2 s, CLI/MCP leave 0) caches it, jobs invalidate it. Gating callers (backup,
                            update, apps…) keep LiveStatus. One proxy check per page
    setproxy.go           → SetProxy, mode-aware reset defaults, Caddy/Nginx snippets
    domains.go            → DomainList/Add/Remove + composeDataFor (rebuilds ComposeData from
                            state) + applyDomainChange (re-render compose, `up -d`, no data loss).
                            Also tlsModeFor / prodNoSSL / hostLANIP / domainNameWarning
    tunnel_ops.go         → TunnelEnable / TunnelDisable
    proxy_ops.go          → ProxyStatus / ProxyStart / ProxyStop
    ffc.go                → SetupFFC: setupBenchAccess + ffc ping; refuses prod benches (no ffc there).
                            FfcHost: host `ffc site add` with the bench's own key (read from
                            home/ffc/config.yaml; setupBenchAccess first if missing), then host `ffc ping`.
                            FfcVersion: `ffc --version` in a running dev container, "" on any failure
    claude_mcp.go         → writes workspace/frappe-bench/.mcp.json (ffc MCP server)
    rotate.go             → RotateCredentials: lockBench, running bench only (ErrBenchStopped), template >= 7 for
                            the root password (PreflightError). MariaDB: SELECT every root Host, one ALTER USER
                            batch on stdin with localhost (the session's own host) last, a revert batch on
                            failure; Postgres: ALTER ROLE via psql on stdin. Then bench.WriteDBRootSecret in
                            place, UpdateBench, and a pymysql/psycopg2 connection from the frappe container.
                            After the ALTER, any failure prints the new value to stderr (it exists nowhere
                            else). The site's own db user is out of scope
    admin.go              → setAdminPassword: `bench set-admin-password` with the password on stdin
    siteops.go            → runSiteRequest: runs a Python script in the frappe container with the request (JSON,
                            secrets included) on STDIN and only the site name on argv; scriptPrelude turns an
                            uncaught exception into one `FFM_ERROR=` line (Frappe's message, HTML stripped, no
                            traceback); result is the `FFM_RESULT=` line. runningBench = GetBench + ErrBenchStopped
    siteconfig.go         → ConfigList/Get/Set/Unset on site_config.json (read-modify-write in the container,
                            indent 1 + sorted keys like Frappe, via a temp file; no frappe import). ParseConfigValue
                            (auto: bool/int/float/JSON else string; json.Number keeps big ints exact),
                            IsSecretConfigKey, configGuard + configOwnedKeys (db_*, redis_*, socketio_*,
                            webserver_port, installed_apps refused outright; host_name on prod / behind a proxy
                            host; encryption_key unless --force). Writes take lockBench like SiteMigrate
    users.go              → UserList/Add/SetEnabled/SetPassword through the User doctype (not `bench add-user`
                            or `set-password`, which take the password as an argument). Administrator's password
                            goes through setAdminPassword and UpdateBench(AdminPassword) like rotate.go;
                            Administrator, Guest and the agent user cannot be disabled
    mail.go               → MailSet/Show/Unset: the default outgoing Email Account through a stdin script. Frappe
                            (v16 email_account.py find_default_outgoing) uses that account first and only falls
                            back to site_config mail_server, so Mailpit never beat it; MailSet still removes
                            Mailpit's keys (only while mail_server is "mailpit") and writes the `ffm_mail_account`
                            marker to site_config, which ensureDevMail honours (stateless, travels in a backup).
                            Frappe allows ONE outgoing-only account per address (unique_email_account_type), so
                            an existing one is reused whatever --name says. MailDNSRecords: SPF include for known
                            providers, DKIM pointer, DMARC p=none
    clean_logs.go         → deletes old rows from 7 Frappe log tables
    scale.go              → Scale / ScaleStatus (prod only, ErrProdOnly; counts 1-32 because the record stores 0 as
                            "unset"): lockBench, persist the counts, then applyScaleLocked = render compose + `up -d`
                            + WaitForHTTP only after a gunicorn change (it replaces the frappe container) + poll `ps`
                            until the worker-long/worker-short container counts match. Not applyDomainChange: that
                            restarts socketio and re-patches. No template change, so no TemplateVersion bump
    trim.go               → Trim: lockBench, running bench only. Frappe's own `--dry-run` first (trim-tables as
                            --format json; trim-database as text, its json dry run prints {} even with ghost tables),
                            applies only with Apply: createSnapshotLocked ("pre-trim-<time>") then the real commands
                            with --no-backup (trim-database also --yes, else click prompts); --no-snapshot leaves
                            Frappe's own backup on. trim-database is refused on PostgreSQL (PreflightError): live on
                            version-16 it dies with a SQL error (UndefinedTable) on postgres; trim-tables works there
                            (tabFile.module reappears after every run, so it is always listed)
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
                            migrate, the recorded Administrator password re-applied, then
                            clear-cache) is shared with DBImport
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
    project.go            → Up: create (or start) the dev bench ffm.yaml describes; detects the app the
                            checkout holds and sets the project mount (projectCheckout); RunHooks / RunTool
    projectmount.go       → project mount helpers: projectMountOf, checkProjectMount (preflight for Start,
                            Recreate, applyDomainChange), wslMountWarning (readOSRelease is injectable),
                            registerProjectApp. See "Project mount" under Key patterns
    worktree.go           → WorktreeAdd / WorktreeRemove / WorktreeList / WorktreeSource: a bench per git
                            worktree. See "Worktree benches" under Key patterns. attachProjectMount
                            (projectmount.go) mounts the worktree into the restored clone;
                            bench/worktree.go has the slug / bench name / alias rules
    hostuser.go           → hostUserIDs() / composeUserIDs() backing --match-host-user
    jobs.go               → JobStore: async jobs persisted to jobs.json. StartAction(js, JobType, key, fn)
                            runs any dashboard action as a job (key = bench name or ProxyJobKey "_proxy";
                            a held key → ErrBenchBusy). jobProgress appends only the new line, keeps the
                            last maxJobLines (5000, Job.LinesDropped counts the cut ones) and scrubs every
                            step/line/error with the bench's benchSecrets (+ create/recreate inputs):
                            jobs.json is plain text. SSE (handler_jobs) sends the log once, then only
                            lines past an absolute offset
    progress.go           → ProgressWriter + CLIProgress{Reveal} / DiscardProgress / BufferProgress; SecretFor
                            (ProgressWriter) and Secret (plain CLI output) mask a secret as
                            "******** (hint)" unless stdout is a terminal or Reveal (--show-secrets).
                            Job, MCP, hook and log writers never reveal. New secret-bearing prints use these.
                            secretAdder / addSecrets(pw, vals...): a writer that masks (jobProgress) takes secrets
                            learned late; Restore registers the parsed manifest's secrets this way, which is how an
                            age-encrypted archive's are masked (ArchiveSecrets cannot read those)

  archive/                → the ffm backup archive format. Plain tar; members compressed
                            individually. Header FIRST (cheap preflight), manifest LAST (its
                            absence IS the definition of a truncated archive). Stdlib only.
    archive.go            → Writer (atomic .partial→rename, 0600 from creation), PeekHeader
    safe_extract.go       → Extract/ExtractReader with traversal, type and size guards
    appsource.go          → ValidateAppSource: an archived app's .tar.gz must stay inside
                            <app>/ (links included) before tar unpacks it in the container

  dashboard/              → the /admin web UI. Stdlib only: html/template, embed, net/http.
    handler.go            → //go:embed templates + static, basic auth behind the limiter, rendering;
                            BenchReveal (POST-only password fetch, no-store JSON), BenchExec (404
                            unless Handler.AllowExec; 403 on bad CSRF; service checked against
                            the bench's compose services; audited BEFORE it runs), serviceAllowed
    handler_actions.go    → POST endpoints for every bench operation (each is a job via runJob)
    handler.go (pageBase) → pageBase embeds Meta and implements metaHolder: a new page's
                            data struct embeds pageBase and renderPage needs no type switch
    handler_create.go     → create form: parseCreateForm (every CreateInput field the CLI has a flag for;
                            only parsing + port-pair check here, manager.Create validates in the job),
                            form re-rendered with its values minus secrets on error (renderPageStatus 400)
    handler_backups.go    → /admin/backups, /admin/benches/{n}/backups (+ prune preview GET then POST with
                            expect_removed/expect_partials that must match a fresh dry run, + schedule);
                            findArchive: "<bench>/<file>" accepted only if ScanArchives lists it;
                            schedulerStatus (var, stubbed in tests) is read-only: the dashboard never installs
                            the hourly job (CLI --no-install semantics)
    handler_restore.go    → /admin/restore: archive from the listing only, new bench name, job keyed by target
    handler_benchpages.go → domains (add/remove = jobs), app update (preview + run = jobs), snapshots
                            (create/restore = jobs, delete sync)
    handler_tools.go      → doctor (job: unbounded probes, no ctx) and clone (job keyed by the target)
    templates/pages/ui.html → shared Tailwind class strings as named templates (cls_*): the CSS build scans
                            templates, not Go, so classes must live there
    handler_jobs.go       → job list/detail + SSE progress stream
    handler_logs.go       → SSE docker compose log stream (?service= validated)
    handler_csrf.go       → HMAC double-submit CSRF tokens; cookie Secure when r.TLS != nil
    auth_limit.go         → authLimiter: per TCP-peer address 5 wrong passwords/min -> 429 +
                            Retry-After, backoff 30 s doubling to 15 min; global 30/min; a request
                            with no credentials is not a failure; X-Forwarded-For deliberately
                            ignored (behind a reverse proxy all clients share one budget)
    audit.go              → h.Audited wraps every POST route: JSONL to config.DashboardAuditFile
                            (0600), action/bench/status/result, exec service+command verbatim,
                            reveal field name (never the value). Handlers add outcomes through
                            auditNote (redirectWithFlash does it for flashes)
    config.go             → dashboard.json; DefaultListenAddr 127.0.0.1:8787; TLSCert / TLSKey /
                            InsecureHTTP / AllowExec
    tailwind.config.js / tailwind.in.css → input of `make dashboard-css`
    templates/ static/    → layout + 10 pages; tailwind.css is GENERATED AND COMMITTED (pinned
                            Tailwind v3.4.19 standalone CLI, sha256-checked in the Makefile; CI
                            fails on a diff), admin.css, admin.js (reveal/copy/confirm/menu),
                            logs.js, jobs.js. No inline script, no on*= attribute, no style=
                            attribute, no external origin: the CSP forbids all of them
                            (TestRenderedPageLoadsOnlyOwnFiles). Edit a template -> run
                            `make dashboard-css` and commit the CSS. Page data goes to JS
                            through data-* attributes; fetch() takes the CSRF token from
                            <meta name="csrf-token">

  server/server.go        → net/http server (Go 1.22 method+pattern routes). GET /health always;
                            /admin/* only when an admin password is set; WriteTimeout 0 for SSE.
                            securityHeaders (outermost, so 401/403 carry them: CSP, XFO DENY,
                            nosniff, Referrer-Policy no-referrer, COOP; HSTS only under TLS) around
                            http.CrossOriginProtection around the mux. Run enforces checkListen
                            (non-loopback needs TLS or --insecure-http; that covers the daemon),
                            ListenAndServeTLS when a cert is set, ReadHeaderTimeout 10 s.
                            POST routes go through h.Audited

  bench/                  → core bench logic, no CLI concerns
    bench.go              → name validation, ProjectName/container helpers
    app.go                → AppSpec + ParseAppSpec(): short names, SSH/HTTPS URLs, url@branch
    compose.go            → ComposeData + renders docker-compose.yml / Dockerfile /
                            devcontainer.json. ALSO REWRITES FRAPPE SOURCE — see Key patterns:
                            WriteWsgiWrapper, PatchAuthenticateJs, PatchUtilsJs, PatchProcfileWorker
    docker.go             → Runner: build/up/down/exec/logs/ps, UpServices, RestartService,
                            ExecDetached, LogsString, WaitForMariaDB/WaitForPostgres, WaitForHTTP,
                            ConfigureGitHubToken/CleanupGitHubToken, ExecStream (unbuffered
                            stdout — never ExecSilent for a multi-GB dump), CopyTo.
                            Runner.ErrOut (nil = os.Stderr) receives Build/Run's failure dump; the
                            manager sets it from pw.Stderr()
    frappe_api.go         → Runner.GenerateAdminAPIKeys(siteName)
    port.go               → AllocatePorts (web 8000 / socketio 9000, +10 per bench, max 50) plus
                            ValidBenchPortPair / CheckTCPPortsFree for --web-port/--socketio-port,
                            and CheckBenchPortRangeFree, which probes all 13 published ports
                            (dev: web and socketio ranges plus Mailpit on web + 6)
    templates/
      dev/
        docker-compose.yml.tmpl  → 5 services (DB, redis×2, frappe, mailpit); DB conditional on DBType, its root
                                   password a compose secret (./secrets/db_root_password), not inline;
                                   bind-mounts ./workspace, one pip-cache volume that also holds
                                   uv's and yarn's caches (UV_CACHE_DIR, YARN_CACHE_FOLDER; a new
                                   volume over a path the image lacks is root-owned), Traefik labels
                                   for <name>.localhost, conditional SSH agent socket, and (v5)
                                   a conditional long-syntax bind of a project checkout over
                                   apps/<app> (create_host_path: false)
        Dockerfile.tmpl          → full dev image: zsh/zinit/starship/ffc/pnpm/Claude Code +
                                   pre-fetched Frappe skills; optional HostUID/HostGID remap layer
      prod/
        docker-compose.yml.tmpl  → (v4: healthchecks on frappe/socketio/redis/DB, no-new-privileges
                                   on all, redis-queue volume + AOF, workers depend on frappe healthy,
                                   bench's gunicorn flags, flush_log_at_trx_commit 1 unless
                                   MariaDBFastCommit; site routers get <name>-headers (HSTS on TLS,
                                   nosniff, SAMEORIGIN, referrer policy) and <name>-compress; socket.io
                                   keeps the browser's Origin) 8 services (DB, redis-cache, redis-queue, frappe/gunicorn,
                                   socketio, worker-long, worker-short, scheduler) + an x-logging
                                   anchor (json-file 10m×3); Traefik labels + per-bench
                                   HTTP→HTTPS redirect; optional ./mysql-logs bind
        Dockerfile.tmpl          → minimal: frappe/bench + corepack pnpm + optional remap layer

  lock/                   → TryAcquire: non-blocking exclusive file lock (flock / LockFileEx via
                            golang.org/x/sys). The OS drops it when the process dies — no
                            stale-lock handling exists or is needed
  scheduler/              → the single hourly job for run-due, behind one Backend interface
                            (backend.go: Name/Available/Where/Install/Uninstall/Installed/Render/Print).
                            Backends: cronBackend (scheduler.go: CurrentJob / Job.Line / Merge / Remove
                            / Find; line tagged `# ffm-backup-tick`, other crontab lines kept byte
                            for byte; refuses a line whose --log (LogOf) is another configuration's
                            (ForeignJobError) unless forced), systemdBackend (systemd.go: oneshot
                            .service + .timer in $XDG_CONFIG_HOME/systemd/user, `OnCalendar=*-*-*
                            *:MM:00`, Persistent=true, % and $ escaped; linger=no gives a warning
                            and the `loginctl enable-linger` command, NEVER run),
                            launchdBackend (launchd.go: plist in ~/Library/LaunchAgents,
                            bootout then bootstrap gui/<uid>), schtasks (schtasks.go: a
                            backup-run-due.cmd in the config dir that the task runs, because /TR is
                            limited to 261 chars and has no environment).
                            Resolve(flag) order: --backend > backup-scheduler.json
                            ({backend,name,ffm}; config.BackupSchedulerFile) > an ffm line already in
                            the crontab (cron) > Default() (windows schtasks, darwin launchd, linux
                            cron if crontab on PATH AND cron/crond in /proc, else systemd --user if
                            `systemctl --user show-environment` works, else ErrNoBackend with the
                            `print --backend cron` hint). Install(job, flag, force) installs the new
                            backend FIRST, records it, then uninstalls the previous one (worst case
                            two jobs; the run-due lock makes the second a no-op). With FFM_CONFIG_DIR
                            set, names carry fnv32(abs config dir) in hex (JobName, launchd label,
                            task name), so configurations do not collide; ForeignJobError is cron
                            only. Tests inject goos/lookPath/homeDir/getenv/procDir/uid and use
                            fakeexec; they must set FFM_CONFIG_DIR and never reach the real crontab,
                            systemd, launchctl or schtasks. The Windows backend is untested on a real
                            Windows host (cmd file and schtasks argv are unit-tested only)
  project/                → ffm.yaml: File (version 1, strict YAML via go.yaml.in/yaml/v3 KnownFields),
                            Find (cwd up to the .git root), Load/Parse/validate, Commands, Hash;
                            trust.go = trust.json {path: sha256} (0600), Trusted/Trust/Revoke;
                            detect.go = DetectApp (the <pkg>/hooks.py with app_name = "<pkg>")
  mcpinstall/             → client config writer for ffm mcp install (Claude Code via the claude CLI,
                            Claude Desktop, Cursor, VS Code JSONC via hujson, Codex TOML line edits;
                            diff, backup, atomic write). COPIED from ffc's internal/mcpinstall
                            (v1.13.0): port fixes both ways
  proxy/proxy.go          → Traefik lifecycle: EnsureNetwork / IsNetworkPresent / Start / Stop /
                            IsRunning / Status / Healthy / DashboardURL / SupportsHTTPS / EnsureHTTPS(email).
                            One `docker inspect` JSON (inspectContainer) feeds status, health and Start. Start
                            self-repairs: a running container with no network endpoint or no published 80 (and
                            443 for HTTPS) is `rm -f` + recreated, as is a stopped one on an image other than
                            Image or one that is still unhealthy after `docker start`; an HTTPS proxy is
                            recreated with the ACME email read from its args, or Start errors if there is none.
                            Status says "running but broken" / names an outdated image; doctor fails a "proxy"
                            check for a running-but-broken proxy on dev benches. Serving()/Probe() (one inspect:
                            running AND Healthy; Probe also returns the "broken" reason) is what every caller
                            deciding a *.localhost URL uses (list, status, home/bench views, SiteURL/LoginURL,
                            create, start, domains); a broken proxy then falls back to the direct published
                            port and prints one stderr warning naming 'ffm proxy start' (manager
                            proxyServingWarn). IsRunning stays for poweroff (must stop a broken proxy), proxy
                            status/start and doctor
  proxy/socket.go         → dockerSocket(): the host path mounted at /var/run/docker.sock. The
                            context/DOCKER_HOST socket is used ONLY when `docker info` lists
                            "rootless" (Docker Desktop's context socket cannot be bind-mounted);
                            a non-unix endpoint under rootless is an error. checkUnprivilegedPorts:
                            rootless and net.ipv4.ip_unprivileged_port_start above 80 (443 too for
                            HTTPS) fails before `docker run` with the sysctl fix; portHint also
                            recognises "privileged port"/"permission denied" after a failed run
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
  The CLI passes `CLIProgress{}`; the dashboard runs the action through `Service.StartAction`, whose
  job ProgressWriter (stdout lines, `Stderr()` lines) streams into the job so the same pipeline can
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
- **The venv links to a patch release the image may not have.** `env/bin/python` is an absolute
  link to `/home/frappe/.pyenv/versions/3.14.7/...`; an image from a newer base breaks it and every
  bench command dies in `os.execv` with `FileNotFoundError`. `Service.Start` (so restart and
  `restart --rebuild` too) runs `bench.RepairVenvCmd` in a `compose run --rm --no-deps` container
  BEFORE `up` (prod's gunicorn would crash-loop): it re-links to the newest patch of the same minor
  and fixes `pyvenv.cfg`; another minor fails Start pointing at `ffm recreate`. Costs ~1 s per start.
  Seeds are keyed by `BenchImage`, so a seed's venv always matches its image.
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
  landed. `PatchDeskSocketio` inserts one line after `frappe.boot = {{ … }};` in the Desk template
  (`frappe/www/desk.html` on v16, `app.html` on v15; Jinja, so no `bench build`): see Domain
  aliases. It errors when no template has that line. Start, `restart --rebuild`, restore and update
  apply it only from template `DeskSocketioTemplate` (10) on (`manager.patchDeskSocketio`): on an
  older compose a port-less page would send socket.io to Traefik, which has no route for it there.
  Create and `applyDomainChange` (reconcile, domains) apply it unconditionally: they render the current compose. `ffmPatchedPaths` (backup) and
  `revertFfmPatches` (update, one `git checkout` per file) list all four files. `PatchProcfileWorker` wraps the dev `worker:` line in a self-restarting loop so an
  idle-Redis worker exit doesn't make honcho tear down the whole stack. All are idempotent;
  `Service.Start` re-applies the realtime and Desk patches in **both** modes (prod needs them too),
  and `restart --rebuild` re-applies them as well. If you are wondering why a bench's
  `realtime/utils.js` differs from upstream, this is why.
- **Project mount** (`ffm up` in an app repository; dev only; template v5). When the directory
  holding ffm.yaml is a Frappe app, the bench bind-mounts that checkout over `apps/<app>` so host
  edits are live. The app is *detected*, not configured: `project.DetectApp` wants exactly one
  top-level `<pkg>/hooks.py` containing `app_name = "<pkg>"` (cross-checked with pyproject's
  `[project] name`, hyphens as underscores; not `frappe`, not among ffm.yaml `apps`); none means a
  config-only file and nothing is mounted. Persisted as `state.Bench.ProjectApp` / `ProjectAppPath`
  (canonical path; NOT in `Apps`, or recreate would `get-app` it) and `CreateInput` fields of the
  same names; `composeDataFor` carries them (dev only), so reconcile, domain, set-proxy and tunnel
  keep the mount. `bench.ValidateProjectMount` refuses `$ " \` and control characters in the path
  (compose interpolation / YAML), and `RenderCompose` enforces it. The checkout must survive
  every ffm operation, which is why:
  - **Two-pass render.** `Create` writes the compose file WITHOUT the mount, runs bench init and the
    seed copy (`compose run` uses the same service definition, and bench init ends in
    `rm -rf /workspace/frappe-bench` — through a mount, that wipes the checkout), and only then
    `MkdirAll`s `apps/<app>` as the host user (Docker would create a missing mountpoint root-owned),
    re-renders with the mount and runs the first `up`. The bind has `create_host_path: false`, so a
    moved checkout fails the start instead of Docker creating a root dir at the source.
  - Registration reuses restore's route: `syncAppsTxt`, `bench setup requirements --python` and
    `--node` (bare form opens the app as a git repo), `install-app`; a seeded create also runs
    `bench build --app` (a seed skips the whole-bench build). No seed is ever captured from it.
  - **`bench remove-app` deletes `apps/<app>`** — through a mount, the checkout. `AppRemove` refuses
    the app (even with `--keep-code`), `AppAdd` refuses its name. `Update` leaves the app's git
    alone (no dirty/ahead check, pull, switch, rollback checkout or prune; `plan.Mounted`) but
    requirements (the `--python`/`--node` halves, `setupRequirementsCmd`), migrate and build still
    cover it; `--apps <it>` is refused. `ffm app list --json`
    gets an additive `mounted` boolean.
  - `Start`, `Recreate` (before it tears anything down) and `applyDomainChange` check the path and
    `<app>/hooks.py` first and name the path if the checkout moved. Delete/rollback only see an empty
    mountpoint host-side — which holds only because `checkMountOutsideFFM` refuses a checkout that
    overlaps the benches, backups or seeds directory in either direction (a checkout cloned to
    `~/frappe/<name>` would otherwise BE the bench directory, and the first rollback would delete it). Backup archives the mounted app's working tree when it has uncommitted
    changes (`dirty-mounted`, via `planAppSources`' `mounted` argument); restore never sets the
    fields (`restoreCreateInput`, and `Create` refuses a mount with `SkipAppInstall`).
  - `Up` refuses to mount as root on Linux (the container user cannot write a root-owned
    checkout, and the failure came after the whole build), notes when an existing bench of the file
    has no mount, and sets `MatchHostUser` on Linux when the uid is not 1000 (git dubious ownership, egg-info and
    DocType exports written from the container) and warns on WSL2 when the checkout is under
    `/mnt/<drive>/` (9p is slow, Windows-side edits may not reload, modes are ignored).
  - The bench's AGENTS.md names the mounted checkout and tells an in-bench agent never to run
    `bench remove-app`, `bench update` or `bench get-app` on it, nor discard work in it with git.
    It is not added to `sites/excluded_apps.txt`: that would also drop it from bare
    `bench setup requirements --dev`, which `ffm test` relies on.
  An existing bench is not changed by `ffm up`: delete it and run `ffm up` again to get a mount.
- **Worktree benches** (`ffm worktree add <branch>`; dev only; needs a project-mount bench).
  `WorktreeAdd` = `Clone` (backup + restore, unchanged) → `git worktree add` → `attachProjectMount`
  under the new bench's lock. Restore never mounts, so the clone first has a plain copy of the app:
  attach removes that copy (`apps/<app>` inside the bench dir only, after checking it is a real
  directory, not a symlink, and the bench has no mount), records `ProjectApp`/`ProjectAppPath` +
  `WorktreeOf`/`WorktreeBranch`/`WorktreeCreatedBranch`, drops the app from `Apps`, adds the alias
  `<slug>.<from>.localhost`, re-renders compose (`applyDomainChange`), runs the two `setup
  requirements` halves, migrate and `bench build --app`. The restored clone gets `MatchHostUser`
  from the host (`needsHostUserRemap`), so the worktree is writable. Bench name `<from>-<slug>`
  (hash suffix past 63); worktree at `<repo-parent>/<repo>.worktrees/<slug>`, never inside the repo.
  **Invariant: ffm never deletes the user's worktree/checkout or changes its git state**, except
  `git worktree remove` WITHOUT `--force` (git refuses dirty/untracked; `--force` only when the user
  passes it) and `git branch -d` (never -D) only for a branch ffm created and only with
  `--delete-branch`. Every failure after the git step deletes the bench, keeps the worktree and
  prints the `git -C <repo> worktree remove <path>` (+ `branch -d`) commands (`ffm worktree remove`
  cannot be used then: the bench record is gone). Remove checks `git status --porcelain` first so a
  dirty worktree is refused before the backup, runs git before deleting the bench, and keeps both on
  any refusal. Recreate carries the Worktree* fields. The Claude Code hooks: create is synchronous
  (`"timeout": 1800`), remove never forces, deletes the branch only if ffm created it, refuses an
  unknown `worktree_path`. `.worktreeinclude` matches (untracked files) are copied, never over a file.
- **Domain aliases** (`ffm domain add`, `ffm create --domain-alias`) route extra hostnames — LAN
  names like `erp.internal` — to a bench alongside its primary host. ffm only configures Traefik;
  pointing DNS at the host is the user's job and `domain add` prints the records to create.
  - **dev** folds aliases into the single existing router rule (`Host(a) || Host(b)`), and a
    `<name>-socketio` router (template 10) sends `/socket.io` on the same hosts to port 9000 with
    `X-Frappe-Site-Name`. Under `bench start` the Desk client appends `socketio_port` to the page
    host, so `PatchDeskSocketio` makes a page loaded *without* a port (Traefik, a tunnel, any
    reverse proxy) use its scheme's default port, i.e. its own origin; `http://localhost:<web-port>`
    keeps the published 9000-9005 range. So aliases need no `--lan`: a loopback bench serves any
    alias, a tunnel's name included (the `--lan` rule was dropped in v1.2.1).
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

- **Restore sources and Frappe's own backup files.** Both feed `restoreCore`, so every gate,
  Create's rollback and the reconcile steps are the proven ones.
  - A source is a local path, an https URL or `target://NAME/BENCH[/FILE]` (URL-style on purpose: a
    bare `name:path` collides with `C:\` and bench names). Remote sources need
    `RestoreInput.AllowRemote`, set only by the CLI: the dashboard, clone and verify pass local
    paths, and a request-driven caller must never be able to make ffm fetch a URL. The download
    is deleted when the restore ends; `--dry-run` downloads too. Never print a `*url.Error`: its
    text carries the whole URL, query string included; `redactURL` / `unwrapURLError` exist for
    that. Plain http, user info in a URL, a redirect to http, a body over the cap, a truncated body
    and a silent server are all errors (`TestHTTPGet`).
  - `--frappe-backup` needs the name, builds a `Manifest` from the dump (Header.Kind
    `frappe-backup`, random DB and (unless given) Administrator password, Members = the dump) and
    copies the user's files into staging under canonical names (copies, never links: the copy
    into the container chmods the staged file). `bench restore` picks tar vs tgz by extension, so
    `.tar` stays `.tar` and `.tar.gz` becomes `.tgz`. Frappe's dump layout (verified on a live v16
    bench): a `-- begin frappe metadata` block (version, branch) first, and
    `INSERT INTO \`tabInstalled Application\` VALUES` with the rows on the FOLLOWING lines.
    The dump decides MariaDB vs Postgres and the Frappe major version; a wrong `--db-type` or
    `--frappe-branch` is refused. An app the dump lists that nobody sourced is refused before
    Create. Encrypted (GPG) Frappe backups are refused by design. The Administrator password is
    always reset (the dump's own is a hash ffm cannot keep). Absolute URLs in the data keep the
    old site name; the summary says so.

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
  redacted either way. version-16 heads the dump "Traceback with variables (most recent call
  last)"; the marker matches both forms, and `user:password@` in URLs (Frappe's postgres pg_dump
  argument) is redacted too.

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
  `.claude/skills/ffc-core/SKILL.md` is missing (ffc skills come from a pinned ffc release, `bench.FfcRef`), and also rewrites `.mcp.json` (wiring
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

- **Secrets never go on an argv; the root password is a file secret.** `<bench>/secrets/db_root_password`
  (dir 0700, file 0600) is written by `bench.WriteDBRootSecret`, called from `WriteCompose`, and mounted as a
  compose secret (`MARIADB_ROOT_PASSWORD_FILE` / `POSTGRES_PASSWORD_FILE`); template 7. **It is rewritten IN PLACE
  (O_TRUNC), never temp + rename**: a file secret is a bind mount that pins the inode, and a rename would leave the
  running database reading the unlinked file (rotation depends on this too). `MariaDBRootArgs` reads the file with
  `$MYSQL_ROOT_PASSWORD` as the fallback for older benches. Not live-verified here: userns-remap hosts (the entrypoint
  and healthcheck read it as root). Everything else goes through stdin, because `bench` execs the frappe interpreter
  and an argument stays on that process's argv: `bench new-site` (no `--*-root-password`, no `--admin-password`; stdin
  `<root>\n<admin>\n<admin>\n`: version-15's `get_admin_password` asks twice, to confirm, and a missing
  second line aborts new-site; v16 asks once), `bench restore` (stdin `<root>\n`), `set-admin-password` (no positional), the GitHub token
  (`ExecStdin`, and `Runner.RunStdin` for bench init), and the backup encryption key (written to site_config as
  `backup_encryption_key`; `bench restore` reads it when `--encryption-key` is absent). Frappe asks with `getpass`
  when a value is missing and `getpass` reads a line from stdin without a terminal (v15 and v16 source: `get_root_connection`,
  `get_admin_password`, `set_user_password`; live-verified on both); root is asked first, so a swapped order fails at the root connect, not
  silently. Values must be one line. `TestCreateKeepsSecretsOffArgv` and its siblings assert on every recorded fakeexec
  argv, keep them passing when adding a command that touches a credential.

- **Site administration never puts a value on an argv** (`config`, `user`, `mail set`): the request goes to a
  Python script on stdin (`runSiteRequest`), so a `mail_password`, an SMTP password or a user's password is not on
  the container's or the host's process list. `bench set-config` and `bench add-user` would put it there. The
  tests for it are in `internal/manager/siteadmin_test.go` (`noSecretOnArgv` on every command).
- **Published ports** bind to `Bench.PublishHost()`: `127.0.0.1` for new benches (`Bind: loopback`),
  all interfaces with `--lan`. Records without `Bind` keep their old behaviour: prod → loopback,
  dev → all interfaces. `--lan` is refused with the default admin password. Domain aliases do not need `--lan`
  (socket.io goes through Traefik, see Domain aliases). `ffm tunnel` refuses the
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
- **Postgres benches carry the matching client.** The compose templates run
  `docker.io/postgres:{{.PostgresMajor}}` and `frappe/bench` ships an older `pg_dump` (17 on Debian trixie), which
  refuses a newer server ("aborting because of server version mismatch"): backup, snapshot, clone,
  worktree add, update's and delete's pre-backup all failed on every postgres bench. Both Dockerfiles
  therefore install `postgresql-client-<PostgresMajor>` from PGDG when `DBType` is postgres
  (postgresql-common's `apt.postgresql.org.sh -y`, keyring bundled in the package) and fail the build
  unless `pg_dump`/`psql --version` say that major. `/usr/bin/pg_dump` is Debian's `pg_wrapper`, which
  with no cluster selected runs the newest installed client: verified with 17 and 18 installed. One
  constant, `bench.PostgresMajor`, feeds the image tag and the client;
  `TestPostgresClientMatchesServerImage` guards it. Existing postgres benches get the client through
  `ffm restart --rebuild` (template 8); `backup-roundtrip.yml` has a postgres job.
- **Pinned build inputs** live in `internal/bench/compose.go`: `BenchImage` (frappe/bench base
  image: `BenchImageTag` + `BenchImageDigest`, the multi-arch index digest — upstream re-pushes tags,
  and one re-push moved Python 3.14.7 to 3.14.8 under every v5.31.0 bench), `FrappeSkillsCommit` (skill pack) and `FfcRef` (ffc: the binary AND its skills). The dev Dockerfile
  installs ffc, then `ffc update --version <FfcRef> --yes` and a `ffc --version` grep fail the build if
  the pin did not take (install.sh alone installs the latest); `ffm status` shows the version in a
  running container. Redis is
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
`log/slog`, `crypto/hmac`. `go.mod` declares Go 1.26.9; the router uses Go 1.22 method+pattern
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
  backup-scheduler.json  # which scheduler backend holds the hourly job ({backend,name,ffm})
  backup-run-due.cmd     # Windows only: what the Task Scheduler task runs (hashed name with FFM_CONFIG_DIR)
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
