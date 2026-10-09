package cli

import (
	"fmt"
	"strings"

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
		newProxyUpgradeCmd(),
		newProxyConfigureCmd(),
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
		img := proxy.RunningImage()
		fmt.Printf("Image:         %s\n", img)
		if img != "" && img != proxy.Image {
			fmt.Printf("               this ffm pins %s: run 'ffm proxy upgrade'\n", proxy.Image)
		}
	}
	if c, err := proxy.LoadConfig(); err == nil {
		fmt.Printf("Configuration: %s\n", describeProxy(c))
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

func newProxyUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Move the proxy to the Traefik version this ffm pins (" + proxy.Image + "), keeping its configuration",
		Long: `Pull the Traefik image this ffm release pins and recreate the proxy with the
same configuration. Certificates are kept (they live in the ffm-letsencrypt
volume). Every bench's routing is interrupted for a few seconds.

Only the newest Traefik minor receives security fixes: run this after
updating ffm.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, err := proxy.Upgrade()
			if err != nil {
				return err
			}
			if before == "" {
				before = "no proxy"
			}
			fmt.Printf("Proxy recreated on %s (was %s).\n", proxy.Image, before)
			return nil
		},
	}
}

func newProxyConfigureCmd() *cobra.Command {
	var staging, cloudflare, httpChallenge bool
	var tokenFile, email string
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Change the proxy's ACME and trusted-proxy settings (recreates it)",
		Long: `Change how the shared proxy gets certificates and whom it trusts, then
recreate it (a few seconds without routing). Settings not given are kept.

  --acme-staging           Let's Encrypt's staging CA: untrusted certificates, but
                           far higher rate limits, for trying a setup. Its own
                           storage, so production certificates are untouched.
  --dns-cloudflare-token-file FILE
                           solve ACME with DNS-01 through Cloudflare: works with
                           port 80 closed or behind Cloudflare's proxy. FILE holds
                           an API token limited to Zone:DNS:Edit; it is mounted
                           read-only, never passed as an argument.
  --http-challenge         back to HTTP-01.
  --cloudflare             trust X-Forwarded-For from Cloudflare's edge ranges, so
                           Frappe sees visitors' addresses (login throttling,
                           logs) instead of Cloudflare's.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := proxy.LoadConfig()
			if err != nil {
				return err
			}
			f := cmd.Flags()
			if f.Changed("acme-staging") {
				c.ACMEStaging = staging
			}
			if f.Changed("cloudflare") {
				c.Cloudflare = cloudflare
			}
			if f.Changed("acme-email") {
				c.ACMEEmail = email
			}
			if tokenFile != "" && httpChallenge {
				return usageError{fmt.Errorf("--dns-cloudflare-token-file and --http-challenge are exclusive")}
			}
			if tokenFile != "" {
				c.DNSProvider, c.DNSTokenFile = "cloudflare", tokenFile
			}
			if httpChallenge {
				c.DNSProvider, c.DNSTokenFile = "", ""
			}
			if err := proxy.SaveConfig(c); err != nil {
				return err
			}
			if err := proxy.Recreate(c); err != nil {
				return err
			}
			fmt.Println("Proxy recreated: " + describeProxy(c))
			return nil
		},
	}
	cmd.Flags().BoolVar(&staging, "acme-staging", false, "Use Let's Encrypt's staging CA (--acme-staging=false to go back)")
	cmd.Flags().BoolVar(&cloudflare, "cloudflare", false, "Trust X-Forwarded-For from Cloudflare (--cloudflare=false to stop)")
	cmd.Flags().StringVar(&tokenFile, "dns-cloudflare-token-file", "", "Solve ACME with DNS-01 via Cloudflare using the token in this file (absolute path)")
	cmd.Flags().BoolVar(&httpChallenge, "http-challenge", false, "Solve ACME with HTTP-01 again")
	cmd.Flags().StringVar(&email, "acme-email", "", "ACME account email")
	return cmd
}

func describeProxy(c proxy.Config) string {
	parts := []string{"HTTP"}
	if c.HTTPS {
		ca := "Let's Encrypt"
		if c.ACMEStaging {
			ca += " (staging)"
		}
		ch := "HTTP-01"
		if c.DNSProvider != "" {
			ch = "DNS-01 via " + c.DNSProvider
		}
		parts = append(parts, "HTTPS with "+ca+", "+ch)
	}
	if c.Cloudflare {
		parts = append(parts, "trusting Cloudflare's forwarded addresses")
	}
	return strings.Join(parts, "; ")
}
