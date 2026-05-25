package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
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
			return manager.New(verbose).Start(name, manager.CLIProgress{})
		},
	}
}

