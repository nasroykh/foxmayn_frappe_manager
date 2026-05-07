package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/tunnel"
)

func newTunnelServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage VPS tunnel server profiles",
		Long: `Manage tunnel server profiles.

  ffm tunnel server                   list configured server profiles
  ffm tunnel server add <name>        add a new server profile (interactive)
  ffm tunnel server set <name>        edit an existing profile
  ffm tunnel server remove <name>     remove a profile
  ffm tunnel server use <name>        set as the default server`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTunnelServerList()
		},
	}
	cmd.AddCommand(
		newTunnelServerAddCmd(),
		newTunnelServerSetCmd(),
		newTunnelServerRemoveCmd(),
		newTunnelServerUseCmd(),
	)
	return cmd
}

func newTunnelServerAddCmd() *cobra.Command {
	var (
		host       string
		portStr    string
		token      string
		baseDomain string
		noTLS      bool
	)

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a new tunnel server profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := tunnel.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Servers[name]; ok {
				return fmt.Errorf("tunnel server %q already exists — use 'ffm tunnel server set %s' to edit it", name, name)
			}

			// If any required flag was omitted, show interactive form.
			if host == "" || portStr == "" || token == "" || baseDomain == "" {
				if err := runTunnelServerForm(&host, &portStr, &token, &baseDomain); err != nil {
					return err
				}
			}

			port, err := parsePort(portStr)
			if err != nil {
				return err
			}

			srv := tunnel.Server{
				Name:       name,
				Host:       host,
				Port:       port,
				Token:      token,
				BaseDomain: baseDomain,
				TLS:        !noTLS,
			}
			cfg.Servers[name] = srv
			if cfg.Default == "" {
				cfg.Default = name
			}
			if err := tunnel.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Tunnel server %q added.\n", name)
			fmt.Printf("  Host:        %s:%d\n", srv.Host, srv.Port)
			fmt.Printf("  Base domain: %s\n", srv.BaseDomain)
			fmt.Printf("  TLS:         %v\n", srv.TLS)
			if cfg.Default == name {
				fmt.Printf("  Default:     yes\n")
			}
			fmt.Println()
			printFrpsSnippet(srv)
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "VPS hostname or IP address")
	cmd.Flags().StringVar(&portStr, "port", "", "frps bind port (control channel), e.g. 7000")
	cmd.Flags().StringVar(&token, "token", "", "frps auth token")
	cmd.Flags().StringVar(&baseDomain, "base-domain", "", "Base domain for tunnels, e.g. tunnel.example.com")
	cmd.Flags().BoolVar(&noTLS, "no-tls", false, "Disable TLS on the frp control channel")
	return cmd
}

func newTunnelServerSetCmd() *cobra.Command {
	var (
		host       string
		portStr    string
		token      string
		baseDomain string
		noTLS      bool
	)

	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Edit an existing tunnel server profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := tunnel.Load()
			if err != nil {
				return err
			}
			existing, ok := cfg.Servers[name]
			if !ok {
				return fmt.Errorf("tunnel server %q not found — use 'ffm tunnel server add %s' to create it", name, name)
			}

			// Pre-fill from existing values.
			if host == "" {
				host = existing.Host
			}
			if portStr == "" {
				portStr = strconv.Itoa(existing.Port)
			}
			if token == "" {
				token = existing.Token
			}
			if baseDomain == "" {
				baseDomain = existing.BaseDomain
			}
			if !cmd.Flags().Changed("no-tls") {
				noTLS = !existing.TLS
			}

			if err := runTunnelServerForm(&host, &portStr, &token, &baseDomain); err != nil {
				return err
			}

			port, err := parsePort(portStr)
			if err != nil {
				return err
			}

			cfg.Servers[name] = tunnel.Server{
				Name:       name,
				Host:       host,
				Port:       port,
				Token:      token,
				BaseDomain: baseDomain,
				TLS:        !noTLS,
			}
			if err := tunnel.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Tunnel server %q updated.\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "VPS hostname or IP address")
	cmd.Flags().StringVar(&portStr, "port", "", "frps bind port")
	cmd.Flags().StringVar(&token, "token", "", "frps auth token")
	cmd.Flags().StringVar(&baseDomain, "base-domain", "", "Base domain for tunnels")
	cmd.Flags().BoolVar(&noTLS, "no-tls", false, "Disable TLS on the frp control channel")
	return cmd
}

func newTunnelServerRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a tunnel server profile",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := tunnel.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Servers[name]; !ok {
				return fmt.Errorf("tunnel server %q not found", name)
			}

			confirmed := false
			form := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Remove tunnel server %q?", name)).
						Description("Any benches using this server will fail to tunnel until reconfigured.").
						Affirmative("Yes, remove").
						Negative("Cancel").
						Value(&confirmed),
				),
			)
			if err := form.Run(); err != nil {
				return err
			}
			if !confirmed {
				fmt.Println("Cancelled.")
				return nil
			}

			delete(cfg.Servers, name)
			if cfg.Default == name {
				cfg.Default = ""
				for n := range cfg.Servers {
					cfg.Default = n
					break
				}
			}
			if err := tunnel.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Tunnel server %q removed.\n", name)
			if cfg.Default != "" {
				fmt.Printf("  Default server is now %q.\n", cfg.Default)
			}
			return nil
		},
	}
}

func newTunnelServerUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the default tunnel server profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := tunnel.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Servers[name]; !ok {
				return fmt.Errorf("tunnel server %q not found", name)
			}
			cfg.Default = name
			if err := tunnel.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("Default tunnel server set to %q.\n", name)
			return nil
		},
	}
}

func runTunnelServerList() error {
	cfg, err := tunnel.Load()
	if err != nil {
		return err
	}
	if len(cfg.Servers) == 0 {
		fmt.Println("No tunnel servers configured.")
		fmt.Println("  Add one with: ffm tunnel server add <name>")
		return nil
	}

	fmt.Printf("%-16s  %-30s  %-12s  %-5s  %s\n", "NAME", "HOST:PORT", "BASE DOMAIN", "TLS", "DEFAULT")
	fmt.Println(strings.Repeat("-", 80))
	for _, srv := range cfg.Servers {
		def := ""
		if srv.Name == cfg.Default {
			def = "✓"
		}
		tlsStr := "yes"
		if !srv.TLS {
			tlsStr = "no"
		}
		fmt.Printf("%-16s  %-30s  %-12s  %-5s  %s\n",
			srv.Name,
			fmt.Sprintf("%s:%d", srv.Host, srv.Port),
			srv.BaseDomain,
			tlsStr,
			def,
		)
	}
	return nil
}

// runTunnelServerForm shows an interactive huh form to fill in server fields.
// noTLS is controlled by the --no-tls flag; the form only handles string inputs.
func runTunnelServerForm(host, portStr, token, baseDomain *string) error {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("VPS hostname or IP").
				Description("e.g. vps.example.com").
				Value(host),
			huh.NewInput().
				Title("frps bind port").
				Description("Control channel port configured in frps.toml, e.g. 7000").
				Value(portStr),
			huh.NewInput().
				Title("Auth token").
				Description("Shared secret between frpc and frps (auth.token in frps.toml)").
				EchoMode(huh.EchoModePassword).
				Value(token),
			huh.NewInput().
				Title("Base domain").
				Description("Wildcard domain pointing to your VPS, e.g. tunnel.example.com").
				Value(baseDomain),
		),
	).WithKeyMap(benchPickKeyMap()).Run()
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q: must be a number between 1 and 65535", s)
	}
	return n, nil
}

// printFrpsSnippet prints a copy-pasteable frps.toml + Caddyfile for VPS setup.
func printFrpsSnippet(srv tunnel.Server) {
	fmt.Println("── VPS setup (one-time) ─────────────────────────────────────────────────────")
	fmt.Println()
	fmt.Println("1. frps.toml  (save to /etc/frp/frps.toml, run via Docker or systemd):")
	fmt.Println()
	fmt.Printf("   bindPort      = %d\n", srv.Port)
	fmt.Printf("   vhostHTTPPort = 8880\n")
	fmt.Printf("   subdomainHost = %q\n", srv.BaseDomain)
	fmt.Printf("   auth.method   = \"token\"\n")
	fmt.Printf("   auth.token    = %q\n", srv.Token)
	fmt.Println()
	fmt.Println("   Docker: docker run -d --name frps --restart unless-stopped \\")
	fmt.Printf("             -p %d:%d -p 8880:8880 \\\n", srv.Port, srv.Port)
	fmt.Println("             -v /etc/frp/frps.toml:/etc/frp/frps.toml:ro \\")
	fmt.Println("             snowdreamtech/frps:0.61 -c /etc/frp/frps.toml")
	fmt.Println()
	fmt.Println("2. Caddyfile  (TLS terminated by Caddy on the VPS):")
	fmt.Println()
	fmt.Printf("   *.%s {\n", srv.BaseDomain)
	fmt.Printf("       tls {\n")
	fmt.Printf("           dns <your-dns-provider>\n")
	fmt.Printf("       }\n")
	fmt.Printf("       reverse_proxy 127.0.0.1:8880\n")
	fmt.Printf("   }\n")
	fmt.Println()
	fmt.Println("   Tip: pin frpc and frps to the same version (0.61). Do not use :latest.")
	fmt.Println("────────────────────────────────────────────────────────────────────────────")
}
