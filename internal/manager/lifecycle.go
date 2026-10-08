package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
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

	pw.Printf("Starting bench %q...\n", name)
	if hasMailpit(b) {
		// Same template version as the ./home bind mounts; Docker would
		// create missing sources as root.
		if err := bench.EnsureHomeDirs(b.Dir); err != nil {
			return fmt.Errorf("create home directories: %w", err)
		}
	}
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// Both modes: a `bench update` inside the bench restores the upstream realtime
	// sources, so re-apply the socket.io patches on every start. Prod needs them
	// as much as dev — the alias socket.io router strips Origin, which upstream
	// authenticate.js rejects.
	if err := bench.PatchAuthenticateJs(b.Dir); err != nil && s.Verbose {
		fmt.Fprintf(os.Stderr, "warning: could not patch authenticate.js: %v\n", err)
	}
	if err := bench.PatchUtilsJs(b.Dir); err != nil && s.Verbose {
		fmt.Fprintf(os.Stderr, "warning: could not patch utils.js: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "warning: could not point outgoing mail at Mailpit: %v\n", err)
		}
		frappeBench := filepath.Join(b.Dir, "workspace", "frappe-bench")
		if err := ensureClaudeMcpConfigHost(frappeBench, b.Name, b.Agent && b.AgentReadOnly); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not ensure Claude Code .mcp.json (ffc MCP): %v\n", err)
		}
		// Self-restarting worker: stops an idle-Redis-timeout worker exit (rc=0)
		// from making honcho SIGTERM the whole stack (502 Bad Gateway). Must run
		// before bench start so honcho reads the patched Procfile.
		if err := bench.PatchProcfileWorker(b.Dir); err != nil && s.Verbose {
			fmt.Fprintf(os.Stderr, "warning: could not patch Procfile worker: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "warning: could not write AGENTS.md: %v\n", err)
		}
		// ffc's config lives in ./home/ffc. When it is missing (a bench that
		// predates the bind mount, or a deleted file), set ffc up again.
		s.healDevAccess(b, pw)
	}

	if b.Tunnel != nil && b.Tunnel.Enabled {
		if _, err := tunnel.Lookup(b.Tunnel.Server); err != nil {
			fmt.Fprintf(os.Stderr, "warning: tunnel server %q not found — skipping frpc start (%v)\n", b.Tunnel.Server, err)
		} else if err := tunnel.Start(b.Dir, b.Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start frpc tunnel: %v\n", err)
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
		if proxy.IsRunning() {
			pw.Printf("  URL (domain):  http://%s\n", b.SiteName)
		} else {
			pw.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", b.SiteName)
		}
	}
	return nil
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
		if out, err := runner.ExecSilent("frappe", "tail", "-n", "20",
			"/home/frappe/bench-start.log"); err == nil {
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
func (s *Service) TeardownBenchFiles(b state.Bench) {
	if b.Tunnel != nil && b.Tunnel.Enabled {
		if err := tunnel.Stop(b.Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: stop frpc container: %v\n", err)
		}
	}
	runner := s.runnerFor(b)
	if err := runner.Down(true); err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker compose down: %v\n", err)
	}
	if err := os.RemoveAll(b.Dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: remove bench dir: %v\n", err)
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
	s.TeardownBenchFiles(b)
	if err := s.RemoveBench(name); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	pw.Printf("Bench %q deleted.\n", name)
	return nil
}
