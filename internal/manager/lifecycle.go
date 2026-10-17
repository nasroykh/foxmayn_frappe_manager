package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/tunnel"
)

// Start brings a bench up (compose up + dev server + tunnel sidecar).
func (s *Service) Start(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	runner := s.runnerFor(b)

	if app, path, ok := projectMountOf(b); ok {
		if err := checkProjectMount(app, path); err != nil {
			return err
		}
	}
	pw.Printf("Starting bench %q...\n", name)
	if hasMailpit(b) {
		// Same template version as the ./home bind mounts; Docker would
		// create missing sources as root.
		if err := bench.EnsureHomeDirs(b.Dir); err != nil {
			return fmt.Errorf("create home directories: %w", err)
		}
	}
	// Before up: a prod frappe container runs gunicorn from the venv and
	// would only crash-loop until the venv is repaired.
	if err := repairVenv(runner, name, pw); err != nil {
		return err
	}
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// Both modes: a `bench update` inside the bench restores the upstream realtime
	// sources, so re-apply the socket.io patches on every start. Prod needs them
	// as much as dev — the alias socket.io router strips Origin, which upstream
	// authenticate.js rejects.
	if err := bench.PatchAuthenticateJs(b.Dir); err != nil && s.Verbose {
		fmt.Fprintf(pw.Stderr(), "warning: could not patch authenticate.js: %v\n", err)
	}
	if err := bench.PatchUtilsJs(b.Dir); err != nil && s.Verbose {
		fmt.Fprintf(pw.Stderr(), "warning: could not patch utils.js: %v\n", err)
	}
	if err := patchDeskSocketio(b); err != nil && s.Verbose {
		fmt.Fprintf(pw.Stderr(), "warning: could not patch the Desk template: %v\n", err)
	}

	if b.IsDev() {
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			// Images built before the ffc skills were split have no
			// /opt/ffc-skills; those benches keep the skills they have until
			// 'ffm restart --rebuild'.
			"[ -f /workspace/frappe-bench/.claude/skills/ffc-core/SKILL.md ] || [ ! -d /opt/ffc-skills ] ||"+
				" ("+bench.InstallSkillsCmd+")"); err != nil && s.Verbose {
			pw.Printf("warning: could not install frappe skills: %v\n", err)
		}

		if err := ensureDevMail(b); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not point outgoing mail at Mailpit: %v\n", err)
		}
		frappeBench := filepath.Join(b.Dir, "workspace", "frappe-bench")
		if err := ensureClaudeMcpConfigHost(frappeBench, b.Name, b.Agent && b.AgentReadOnly); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: could not ensure Claude Code .mcp.json (ffc MCP): %v\n", err)
		}
		// Self-restarting worker: stops an idle-Redis-timeout worker exit (rc=0)
		// from making honcho SIGTERM the whole stack (502 Bad Gateway). Must run
		// before bench start so honcho reads the patched Procfile.
		if err := bench.PatchProcfileWorker(b.Dir); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not patch Procfile worker: %v\n", err)
		}

		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			bench.DevServerStartCmd); err != nil {
			return fmt.Errorf("bench start: %w", err)
		}

		// Fatal, not a warning. The dev server is the bench: honcho tears the
		// whole stack down when any of its processes exits, so a bench whose
		// `bench start` died serves nothing at all. Printing "Bench is running"
		// underneath a warning is the one outcome that helps nobody.
		url := fmt.Sprintf("http://localhost:%d", b.WebPort)
		if err := bench.WaitForHTTP(url, 90*time.Second); err != nil {
			return webServerUnreachable(runner, b.IsDev(), err)
		}
		if err := writeAgentsMD(b); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not write AGENTS.md: %v\n", err)
		}
		// ffc's config lives in ./home/ffc. When it is missing (a bench that
		// predates the bind mount, or a deleted file), set ffc up again.
		s.healDevAccess(b, pw)
	}

	if b.Tunnel != nil && b.Tunnel.Enabled {
		if _, err := tunnel.Lookup(b.Tunnel.Server); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: tunnel server %q not found — skipping frpc start (%v)\n", b.Tunnel.Server, err)
		} else if err := tunnel.Start(b.Dir, b.Name); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: could not start frpc tunnel: %v\n", err)
		}
	}

	pw.Printf("Bench %q is running.\n", name)
	if b.IsProd() {
		if b.ProxyHost != "" {
			pw.Printf("  URL: %s\n", b.ProxyHost)
		} else if b.Domain != "" {
			pw.Printf("  URL: https://%s\n", b.Domain)
		}
	} else {
		pw.Printf("  URL (port):    http://localhost:%d\n", b.WebPort)
		if proxyServingWarn(pw) {
			pw.Printf("  URL (domain):  http://%s\n", b.SiteName)
		} else {
			pw.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", b.SiteName)
		}
	}
	return nil
}

// repairVenv runs bench.RepairVenvCmd, so a bench whose image now ships
// another patch release of its Python starts instead of dying in os.execv.
// A docker failure is only a warning: up reports it better.
func repairVenv(runner *bench.Runner, name string, pw ProgressWriter) error {
	out, err := runner.RunSilent("frappe", "bash", "-c", bench.RepairVenvCmd)
	var marker string
	for _, line := range strings.Split(out, "\n") {
		if m, ok := strings.CutPrefix(line, "FFM_VENV="); ok {
			marker = m
		}
	}
	f := strings.Fields(marker)
	switch {
	case len(f) == 3 && f[0] == "relinked":
		pw.Printf("The image ships Python %s, not %s: virtualenv re-pointed.\n", f[2], f[1])
	case len(f) == 2 && f[0] == "gone":
		return fmt.Errorf("the bench's virtualenv was made with Python %s, and its image has no %s release: "+
			"run 'ffm recreate %s' (it backs the bench up first)", f[1], minorOf(f[1]), name)
	case len(f) == 3 && f[0] == "failed":
		return fmt.Errorf("the bench's virtualenv needs Python %s, which its image replaced with %s, "+
			"and re-pointing it failed: is %s/workspace/frappe-bench/env writable?", f[1], f[2], runner.ComposeDir)
	case len(f) == 2 && f[0] == "unknown":
		return fmt.Errorf("the bench's virtualenv points at %s, which its image does not have", f[1])
	case err != nil:
		fmt.Fprintf(pw.Stderr(), "warning: could not check the bench's virtualenv: %v\n", err)
	}
	return nil
}

// minorOf returns "3.14" for "3.14.7".
func minorOf(v string) string {
	if i := strings.LastIndex(v, "."); i > 0 {
		return v[:i]
	}
	return v
}

// webServerUnreachable explains a WaitForHTTP failure with the bench's own
// account of it.
//
// The wait can only report that nothing answered. What the user needs is the
// reason, and the bench already has it: in dev, honcho's log names the process
// that died and took the rest down with it; in prod, the frappe container's log
// carries whatever gunicorn said on the way out.
func webServerUnreachable(runner *bench.Runner, isDev bool, waitErr error) error {
	var detail string
	if isDev {
		if out, err := runner.DevServerLogTail(20); err == nil {
			detail = strings.TrimSpace(out)
		}
	} else {
		detail = tailLines(runner.LogsString("frappe"), 20)
	}
	if detail == "" {
		return waitErr
	}
	return fmt.Errorf("%w\n\n%s", waitErr, detail)
}

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Stop stops compose services for a bench.
func (s *Service) Stop(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	runner := s.runnerFor(b)
	pw.Printf("Stopping bench %q...\n", name)
	if err := runner.Stop(); err != nil {
		return fmt.Errorf("docker compose stop: %w", err)
	}
	pw.Printf("Bench %q stopped.\n", name)
	return nil
}

// TeardownBenchFiles runs docker compose down with volumes and removes the bench directory.
// Warnings go to pw.Stderr(); CLI callers pass CLIProgress{}.
func (s *Service) TeardownBenchFiles(b state.Bench, pw ProgressWriter) {
	if b.Tunnel != nil && b.Tunnel.Enabled {
		if err := tunnel.Stop(b.Name); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: stop frpc container: %v\n", err)
		}
	}
	runner := s.runnerFor(b)
	if err := runner.Down(true); err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: docker compose down: %v\n", err)
	}
	// `down --volumes` removes only the volumes the current compose file
	// declares. A volume a later template dropped (yarn-cache, in v0.11.1)
	// still carries the project label, so it goes too.
	removeProjectVolumes(strings.ToLower(bench.ProjectName(b.Name)), pw)
	if err := os.RemoveAll(b.Dir); err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: remove bench dir: %v\n", err)
	}
}

// Delete removes a bench from disk and state, after backing it up unless
// in.NoBackup is set.
func (s *Service) Delete(in DeleteInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return err
	}
	defer release()
	if _, err := s.backupBeforeDestroy(b, "delete", in.NoBackup, pw); err != nil {
		return err
	}
	return s.deleteLocked(in.Name, pw)
}

// backupBeforeDestroy takes the automatic backup that delete and recreate make
// before removing a bench's data, and returns the archive path ("" when
// skipped). The caller holds the bench lock.
//
// The archive is an ordinary manual one, so retention never prunes it. A bench
// whose directory is already gone has nothing to save, and is let through.
func (s *Service) backupBeforeDestroy(b state.Bench, op string, skip bool, pw ProgressWriter) (string, error) {
	if skip {
		return "", nil
	}
	if _, err := os.Stat(b.Dir); err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: %s has no bench directory; nothing to back up before %s\n", b.Name, op)
		return "", nil
	}
	pw.Printf("Backing up %q before %s (skip with --no-backup)...\n", b.Name, op)
	var archivePath string
	if err := s.backupLocked(BackupInput{
		BenchName: b.Name,
		Label:     "before " + op,
		writtenTo: &archivePath,
	}, pw); err != nil {
		return "", fmt.Errorf("backup before %s failed, so nothing was changed: %w\n"+
			"(pass --no-backup to %s without a backup)", op, err, op)
	}
	return archivePath, nil
}

// deleteLocked is Delete for a caller that already holds the bench's lock
// (a failed Restore removing its own half-built target).
func (s *Service) deleteLocked(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	pw.Printf("Deleting bench %q...\n", name)
	s.TeardownBenchFiles(b, pw)
	if err := s.RemoveBench(name); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	pw.Printf("Bench %q deleted.\n", name)
	return nil
}

// removeProjectVolumes deletes the volumes labelled with a compose project.
func removeProjectVolumes(project string, pw ProgressWriter) {
	out, err := execx.Command("docker", "volume", "ls", "-q", "--filter", "label="+composeProjectLabel+"="+project).Output()
	if err != nil {
		return
	}
	for _, v := range strings.Fields(string(out)) {
		if o, err := execx.Command("docker", "volume", "rm", v).CombinedOutput(); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: remove volume %s: %v %s\n", v, err, strings.TrimSpace(string(o)))
		}
	}
}

// patchDeskSocketio applies bench.PatchDeskSocketio to a bench whose compose
// file already routes /socket.io through Traefik. An older bench gets it from
// the next reconcile or domain change, which render the current template.
func patchDeskSocketio(b state.Bench) error {
	if b.TemplateVersion < bench.DeskSocketioTemplate {
		return nil
	}
	return bench.PatchDeskSocketio(b.Dir)
}
