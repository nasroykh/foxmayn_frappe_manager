package manager

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
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
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)

	pw.Printf("Setting up ffc for bench %q...\n", name)
	pw.Println("  [1] Generating Frappe API keys")
	keys, err := runner.GenerateAdminAPIKeys(b.SiteName)
	if err != nil {
		return fmt.Errorf("generate API keys: %w", err)
	}

	pw.Println("  [2] Writing ~/.config/ffc/config.yaml inside the container")
	cfg := fmt.Sprintf(
		"default_site: %s\nnumber_format: french\ndate_format: yyyy-mm-dd\nsites:\n  %s:\n    url: \"http://localhost:8000\"\n    api_key: \"%s\"\n    api_secret: \"%s\"\n",
		name, name, keys.Key, keys.Secret,
	)
	encoded := base64.StdEncoding.EncodeToString([]byte(cfg))
	writeCmd := fmt.Sprintf(
		"mkdir -p /home/frappe/.config/ffc && echo '%s' | base64 -d > /home/frappe/.config/ffc/config.yaml",
		encoded,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", writeCmd); err != nil {
		return fmt.Errorf("write ffc config: %w", err)
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
