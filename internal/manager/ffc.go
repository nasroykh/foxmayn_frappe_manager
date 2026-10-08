package manager

import (
	"fmt"
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
	who := "Administrator"
	if b.Agent {
		who = agentEmail(b)
	}
	pw.Printf("  [1] Minting API keys for %s and configuring ffc, .mcp.json and AGENTS.md\n", who)
	keys, err := setupBenchAccess(runner, b)
	if err != nil {
		return err
	}

	pw.Println("  [2] Verifying ffc connectivity (ffc ping)")
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
