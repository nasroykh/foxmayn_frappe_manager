package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newPoweroffCmd() *cobra.Command {
	var keepProxy bool
	cmd := &cobra.Command{
		Use:   "poweroff",
		Short: "Stop every running bench and the shared proxy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return manager.New(verbose).Poweroff(keepProxy, manager.CLIProgress{})
		},
	}
	cmd.Flags().BoolVar(&keepProxy, "keep-proxy", false, "Leave the shared Traefik proxy running")
	return cmd
}
