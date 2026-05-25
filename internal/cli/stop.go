package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
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
			return manager.New(verbose).Stop(name, manager.CLIProgress{})
		},
	}
}
