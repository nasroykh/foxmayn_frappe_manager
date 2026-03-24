package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [name]",
		Short: "Stop a running bench (containers remain, data preserved)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to stop")
			if err != nil {
				return err
			}
			return runStop(name)
		},
	}
}

func runStop(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, verbose)

	fmt.Printf("Stopping bench %q...\n", name)
	if err := runner.Stop(); err != nil {
		return fmt.Errorf("docker compose stop: %w", err)
	}
	fmt.Printf("Bench %q stopped.\n", name)
	return nil
}
