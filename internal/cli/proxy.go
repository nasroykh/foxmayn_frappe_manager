package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
)

func newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage the shared Traefik reverse proxy (sitename.localhost routing)",
		// Running `ffm proxy` with no subcommand shows the current status.
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
			fmt.Println("Starting Traefik proxy...")
			if err := proxy.Start(); err != nil {
				return err
			}
			fmt.Println("Proxy is running.")
			fmt.Printf("  HTTP:      http://<bench>.localhost\n")
			fmt.Printf("  Dashboard: %s\n", proxy.DashboardURL())
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
			fmt.Println("Stopping Traefik proxy...")
			if err := proxy.Stop(); err != nil {
				return err
			}
			fmt.Println("Proxy stopped.")
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
	status := proxy.Status()
	network := "absent"
	if proxy.IsNetworkPresent() {
		network = "present"
	}

	fmt.Printf("Proxy status:  %s\n", status)
	fmt.Printf("Network (%s):  %s\n", proxy.NetworkName, network)
	if proxy.IsRunning() {
		fmt.Printf("Dashboard:     %s\n", proxy.DashboardURL())
		fmt.Printf("Routing:       http://<bench>.localhost\n")
	}
	return nil
}

// printWSL2Note prints a one-time reminder for WSL2 users about .localhost
// resolution on the Windows host.
func printWSL2Note() {
	fmt.Println()
	fmt.Println("  Note (WSL2): .localhost subdomains resolve correctly inside WSL2.")
	fmt.Println("  To access them from a Windows browser, add entries to")
	fmt.Println("  C:\\Windows\\System32\\drivers\\etc\\hosts, e.g.:")
	fmt.Println("    127.0.0.1  mybench.localhost")
	fmt.Println("  Or use the direct port URL shown by 'ffm list'.")
}
