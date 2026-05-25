package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newSetProxyCmd() *cobra.Command {
	var (
		port       int
		host       string
		noSSL      bool
		reset      bool
		printCaddy bool
		printNginx bool
	)

	cmd := &cobra.Command{
		Use:   "set-proxy [name]",
		Short: "Configure a bench to run behind a reverse proxy",
		Long: `Configure Frappe's socket.io and SSL settings for reverse-proxy deployments.

By default applies settings for an HTTPS proxy on port 443. Use --reset to
restore creation-time defaults.

The bench must be running. For dev benches the dev server restarts automatically;
for prod benches run 'ffm restart <name>' to apply changes.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to configure")
			if err != nil {
				return err
			}
			return manager.New(verbose).SetProxy(manager.SetProxyInput{
				Name: name, Port: port, Host: host, NoSSL: noSSL, Reset: reset,
				PrintCaddy: printCaddy, PrintNginx: printNginx,
			}, manager.CLIProgress{})
		},
	}

	cmd.Flags().IntVar(&port, "port", 443, "Public port the reverse proxy listens on (sets socketio_port)")
	cmd.Flags().StringVar(&host, "host", "", "Public domain, e.g. frappe.example.com (sets per-site host_name)")
	cmd.Flags().BoolVar(&noSSL, "no-ssl", false, "Disable SSL mode even when --port 443 is used")
	cmd.Flags().BoolVar(&reset, "reset", false, "Restore creation-time defaults (dev: published socketio port, no ssl; prod: port 443, ssl on)")
	cmd.Flags().BoolVar(&printCaddy, "print-caddy", false, "Print a Caddy config snippet for this bench")
	cmd.Flags().BoolVar(&printNginx, "print-nginx", false, "Print an Nginx config snippet for this bench")

	return cmd
}
