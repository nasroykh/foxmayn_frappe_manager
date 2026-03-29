package cli

import (
	"github.com/spf13/cobra"
)

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart [name]",
		Short: "Restart a bench (stop then start)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to restart")
			if err != nil {
				return err
			}
			if err := runStop(name); err != nil {
				return err
			}
			return runStart(name)
		},
	}
}
