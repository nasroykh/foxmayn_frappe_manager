package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newTunnelCmd() *cobra.Command {
	var (
		serverName string
		subdomain  string
		off        bool
		printOnly  bool
	)

	cmd := &cobra.Command{
		Use:   "tunnel [name]",
		Short: "Expose a bench over a secure public URL via a VPS tunnel",
		Long: `Expose a local Frappe bench over a secure public URL using a user-owned VPS.

Configure tunnel servers first:

  ffm tunnel server add myvps

Then enable a tunnel:

  ffm tunnel <name>                   enable with the default server
  ffm tunnel <name> --server myvps   switch to a specific server profile
  ffm tunnel <name> --off             disable tunnel; restore direct-access settings
  ffm tunnel <name> --print           show the frpc.toml config without applying`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := manager.New(verbose)
			switch {
			case off:
				name, err := resolveBenchName(args, "Select a bench to disable tunnel")
				if err != nil {
					return err
				}
				return svc.TunnelDisable(name, manager.CLIProgress{})
			case printOnly:
				name, err := resolveBenchName(args, "Select a bench to print tunnel config")
				if err != nil {
					return err
				}
				out, err := svc.TunnelRenderFrpc(name, serverName, subdomain)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			case len(args) == 0 && serverName == "" && subdomain == "":
				name, err := resolveBenchName(args, "Select a bench")
				if err != nil {
					return err
				}
				out, err := svc.TunnelStatusText(name)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			default:
				name, err := resolveBenchName(args, "Select a bench to tunnel")
				if err != nil {
					return err
				}
				return svc.TunnelEnable(manager.TunnelEnableInput{
					BenchName: name, ServerName: serverName, Subdomain: subdomain,
				}, manager.CLIProgress{})
			}
		},
	}

	cmd.Flags().StringVar(&serverName, "server", "", "Tunnel server profile name (default: configured default)")
	cmd.Flags().StringVar(&subdomain, "subdomain", "", "Public subdomain slug (default: bench name)")
	cmd.Flags().BoolVar(&off, "off", false, "Disable the tunnel and restore direct-access Frappe settings")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the rendered frpc.toml without applying anything")

	cmd.AddCommand(newTunnelServerCmd())

	return cmd
}
