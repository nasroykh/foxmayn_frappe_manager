package manager

import (
	"fmt"
	"os"
	"path/filepath"
)

// SetupFFC generates API keys and writes ffc config inside the bench container.
func (s *Service) SetupFFC(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	// ffc is installed only in the dev image. On prod this would mint a new
	// Administrator API secret (breaking whatever used the old one) and then
	// report success for a tool that is not there.
	if !b.IsDev() {
		return fmt.Errorf("ffc is set up on dev benches only; %q is a %s bench", b.Name, b.Mode)
	}
	runner := s.runnerFor(b)

	pw.Printf("Setting up ffc for bench %q...\n", name)
	pw.Println("  [1] Generating Frappe API keys")
	keys, err := runner.GenerateAdminAPIKeys(b.SiteName)
	if err != nil {
		return fmt.Errorf("generate API keys: %w", err)
	}

	pw.Println("  [2] Writing ~/.config/ffc/config.yaml inside the container")
	if err := writeFfcConfig(runner, name, keys.Key, keys.Secret); err != nil {
		return err
	}

	frappeBench := filepath.Join(b.Dir, "workspace", "frappe-bench")
	if err := ensureClaudeMcpConfigHost(frappeBench, name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write Claude Code .mcp.json (ffc MCP): %v\n", err)
	}

	pw.Println("  [3] Verifying ffc connectivity (ffc ping)")
	out, pingErr := runner.ExecSilent("frappe", "bash", "-c", "ffc ping")

	pw.Printf("\nffc configured on bench %q.\n", name)
	pw.Printf("  api_key:    %s\n", keys.Key)
	pw.Printf("  api_secret: %s\n", keys.Secret)
	pw.Printf("  site:       %s\n", b.SiteName)
	if pingErr != nil {
		pw.Printf("  ping:       FAILED — start the bench and run 'ffc ping' to verify\n")
		pw.Printf("              (%s)\n", out)
	} else {
		pw.Printf("  ping:       OK — %s\n", out)
	}
	return nil
}
