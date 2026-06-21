package manager

import (
	"fmt"
	"os"
	"path/filepath"
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
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)

	pw.Printf("Starting bench %q...\n", name)
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	if b.IsDev() {
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"[ -f /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli/SKILL.md ] ||"+
				" (mkdir -p /workspace/frappe-bench/.agents/skills /workspace/frappe-bench/.claude/skills"+
				" && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.agents/skills/"+
				" && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.claude/skills/"+
				" && mkdir -p /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli"+
				" && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli/"+
				" && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli/)"); err != nil && s.Verbose {
			pw.Printf("warning: could not install frappe skills: %v\n", err)
		}

		frappeBench := filepath.Join(b.Dir, "workspace", "frappe-bench")
		if err := ensureClaudeMcpConfigHost(frappeBench, b.Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not ensure Claude Code .mcp.json (ffc MCP): %v\n", err)
		}
		if err := bench.PatchAuthenticateJs(b.Dir); err != nil && s.Verbose {
			fmt.Fprintf(os.Stderr, "warning: could not patch authenticate.js: %v\n", err)
		}
		if err := bench.PatchUtilsJs(b.Dir); err != nil && s.Verbose {
			fmt.Fprintf(os.Stderr, "warning: could not patch utils.js: %v\n", err)
		}
		// Self-restarting worker: stops an idle-Redis-timeout worker exit (rc=0)
		// from making honcho SIGTERM the whole stack (502 Bad Gateway). Must run
		// before bench start so honcho reads the patched Procfile.
		if err := bench.PatchProcfileWorker(b.Dir); err != nil && s.Verbose {
			fmt.Fprintf(os.Stderr, "warning: could not patch Procfile worker: %v\n", err)
		}

		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
			return fmt.Errorf("bench start: %w", err)
		}

		url := fmt.Sprintf("http://localhost:%d", b.WebPort)
		if err := bench.WaitForHTTP(url, 30*time.Second); err != nil {
			pw.Printf("warning: %v\n", err)
		}
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

// Stop stops compose services for a bench.
func (s *Service) Stop(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
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
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
	if err := runner.Down(true); err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker compose down: %v\n", err)
	}
	if err := os.RemoveAll(b.Dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: remove bench dir: %v\n", err)
	}
}

// Delete removes a bench from disk and state.
func (s *Service) Delete(name string, pw ProgressWriter) error {
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
