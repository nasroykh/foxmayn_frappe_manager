package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
)

func newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage the shared Traefik reverse proxy (sitename.localhost routing)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxyStatus()
		},
	}

	cmd.AddCommand(
		newProxyStartCmd(),
		newProxyStopCmd(),
		newProxyStatusCmd(),
	)

	return cmd
}

func newProxyStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the Traefik proxy (enables sitename.localhost routing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := manager.New(verbose).ProxyStart(manager.CLIProgress{}); err != nil {
				return err
			}
			printWSL2Note()
			return nil
		},
	}
}

func newProxyStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Traefik proxy (benches remain accessible on their direct ports)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := manager.New(verbose)
			if err := svc.ProxyStop(manager.CLIProgress{}); err != nil {
				return err
			}
			fmt.Println("  Benches are still reachable on their direct ports (run 'ffm list').")
			fmt.Println("  Run 'ffm proxy start' to re-enable domain routing.")
			return nil
		},
	}
}

func newProxyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current proxy status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxyStatus()
		},
	}
}

func runProxyStatus() error {
	v := manager.New(verbose).ProxyStatus()
	fmt.Printf("Proxy status:  %s\n", v.Status)
	fmt.Printf("Network (%s):  %s\n", proxy.NetworkName, v.Network)
	if v.Running {
		fmt.Printf("Dashboard:     %s\n", v.Dashboard)
		fmt.Printf("Routing:       http://<bench>.localhost\n")
	}
	return nil
}

func printWSL2Note() {
	fmt.Println()
	fmt.Println("  Note (WSL2): .localhost subdomains resolve correctly inside WSL2.")
	fmt.Println("  To access them from a Windows browser, add entries to")
	fmt.Println("  C:\\Windows\\System32\\drivers\\etc\\hosts, e.g.:")
	fmt.Println("    127.0.0.1  mybench.localhost")
	fmt.Println("  Or use the direct port URL shown by 'ffm list'.")
}
