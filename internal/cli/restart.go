package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newRestartCmd() *cobra.Command {
	var rebuild, fresh bool

	cmd := &cobra.Command{
		Use:   "restart [name]",
		Short: "Restart a bench (stop then start)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to restart")
			if err != nil {
				return err
			}
			return manager.New(verbose).Restart(manager.RestartInput{
				Name:    name,
				Rebuild: rebuild || fresh,
				Fresh:   fresh,
			}, manager.CLIProgress{})
		},
	}

	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "Rebuild the Docker image before starting")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "With --rebuild: ignore the build cache and re-pull the base image, updating ffc, Claude Code and the other tools (implies --rebuild)")
	return cmd
}
