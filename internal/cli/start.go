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

	// Start the Frappe dev server in the background via nohup so it survives
	// after the exec session exits.
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
		return fmt.Errorf("bench start: %w", err)
	}

	// Wait for the web port to respond.
	url := fmt.Sprintf("http://localhost:%d", b.WebPort)
	if err := bench.WaitForHTTP(url, 30*time.Second); err != nil {
		fmt.Printf("warning: %v\n", err)
	}

	fmt.Printf("Bench %q is running.\n", name)
	fmt.Printf("  URL (port):    http://localhost:%d\n", b.WebPort)
	if proxy.IsRunning() {
		fmt.Printf("  URL (domain):  http://%s\n", b.SiteName)
	} else {
		fmt.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", b.SiteName)
	}
	return nil
}
