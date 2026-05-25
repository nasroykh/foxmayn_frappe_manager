package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newFfcCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ffc [name]",
		Short: "Generate Frappe API keys and configure ffc inside a bench",
		Long: `Generates an API key/secret for the Administrator user and writes
~/.config/ffc/config.yaml inside the bench container.

Run this if ffc setup was skipped or failed during 'ffm create', or to
regenerate keys on an existing bench.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			return manager.New(verbose).SetupFFC(name, manager.CLIProgress{})
		},
	}
}
