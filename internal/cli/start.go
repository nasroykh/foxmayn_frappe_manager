package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start [name]",
		Short: "Start a stopped bench",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to start")
			if err != nil {
				return err
			}
			return runStart(name)
		},
	}
}

func runStart(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, verbose)

	fmt.Printf("Starting bench %q...\n", name)

	// Bring containers back up (they may have been stopped, not downed).
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	if b.IsDev() {
		// Ensure Claude/agent skills are present (missing on benches created before this feature).
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"[ -f /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli/SKILL.md ] ||"+
				" (mkdir -p /workspace/frappe-bench/.agents/skills /workspace/frappe-bench/.claude/skills"+
				" && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.agents/skills/"+
				" && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.claude/skills/"+
				" && mkdir -p /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli"+
				" && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli/"+
				" && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli/)"); err != nil && verbose {
			fmt.Printf("warning: could not install frappe skills: %v\n", err)
		}

		// Start the Frappe dev server in the background via nohup.
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
			return fmt.Errorf("bench start: %w", err)
		}

		// Wait for the web port to respond.
		url := fmt.Sprintf("http://localhost:%d", b.WebPort)
		if err := bench.WaitForHTTP(url, 30*time.Second); err != nil {
			fmt.Printf("warning: %v\n", err)
		}
	}
	// Prod: services start automatically via compose command: directives.

	fmt.Printf("Bench %q is running.\n", name)
	if b.IsProd() {
		if b.ProxyHost != "" {
			fmt.Printf("  URL: %s\n", b.ProxyHost)
		} else if b.Domain != "" {
			fmt.Printf("  URL: https://%s\n", b.Domain)
		}
	} else {
		fmt.Printf("  URL (port):    http://localhost:%d\n", b.WebPort)
		if proxy.IsRunning() {
			fmt.Printf("  URL (domain):  http://%s\n", b.SiteName)
		} else {
			fmt.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", b.SiteName)
		}
	}
	return nil
}
