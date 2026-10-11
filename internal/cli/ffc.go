package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newFfcCmd() *cobra.Command {
	var host, force bool
	cmd := &cobra.Command{
		Use:   "ffc [name]",
		Short: "Generate Frappe API keys and configure ffc inside a bench",
		Long: `Generates an API key/secret for the Administrator user and writes
~/.config/ffc/config.yaml inside the bench container.

Run this if ffc setup was skipped or failed during 'ffm create', or to
regenerate keys on an existing bench.

With --host, nothing is regenerated: the bench is added as a site to the ffc
installed on this machine, with the keys the bench already holds, so AI
clients on the host (Cursor, Claude Desktop, VS Code) can reach it through
'ffc mcp'. The bench must be running. --force replaces a host site of the
same name. Re-run it after a plain 'ffm ffc', which mints new keys.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if force && !host {
				return usageError{errors.New("--force is only meaningful with --host")}
			}
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			svc := manager.New(verbose)
			if host {
				return svc.FfcHost(name, force, manager.CLIProgress{})
			}
			return svc.SetupFFC(name, manager.CLIProgress{})
		},
	}
	cmd.Flags().BoolVar(&host, "host", false, "Add the bench to the host's ffc (reuses the bench's keys) instead of regenerating them")
	cmd.Flags().BoolVar(&force, "force", false, "With --host: replace a host ffc site of the same name")
	return cmd
}
