package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/tunnel"
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
  ffm tunnel <name> --print           show the frpc.toml config without applying

The bench must be running. Frappe's host_name and socketio settings are updated
automatically. Re-running the command refreshes the frpc config.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case off:
				name, err := resolveBenchName(args, "Select a bench to disable tunnel")
				if err != nil {
					return err
				}
				return runTunnelOff(name)
			case printOnly:
				name, err := resolveBenchName(args, "Select a bench to print tunnel config")
				if err != nil {
					return err
				}
				return runTunnelPrint(name, serverName, subdomain)
			case len(args) == 0 && serverName == "" && subdomain == "":
				// No bench name and no action flags: pick a bench and show status.
				name, err := resolveBenchName(args, "Select a bench")
				if err != nil {
					return err
				}
				return runTunnelStatus(name)
			default:
				name, err := resolveBenchName(args, "Select a bench to tunnel")
				if err != nil {
					return err
				}
				return runTunnelEnable(name, serverName, subdomain)
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

func runTunnelEnable(name, serverName, subdomain string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	// ── Resolve server profile ─────────────────────────────────────────────
	var srv *tunnel.Server
	var resolvedServer string
	switch {
	case serverName != "":
		s, err := tunnel.Lookup(serverName)
		if err != nil {
			return err
		}
		srv = s
		resolvedServer = serverName
	case b.Tunnel != nil && b.Tunnel.Server != "":
		// Reuse currently configured server unless --server overrides it.
		s, err := tunnel.Lookup(b.Tunnel.Server)
		if err != nil {
			return err
		}
		srv = s
		resolvedServer = b.Tunnel.Server
	default:
		s, sName, err := tunnel.DefaultServer()
		if err != nil {
			return err
		}
		srv = s
		resolvedServer = sName
	}

	// ── Resolve subdomain ──────────────────────────────────────────────────
	if subdomain == "" {
		if b.Tunnel != nil && b.Tunnel.Subdomain != "" {
			subdomain = b.Tunnel.Subdomain
		} else {
			subdomain = b.Name
		}
	}

	// ── Subdomain collision check ──────────────────────────────────────────
	benches, err := store.Load()
	if err != nil {
		return err
	}
	for _, other := range benches {
		if other.Name == name || other.Tunnel == nil || !other.Tunnel.Enabled {
			continue
		}
		if other.Tunnel.Server == resolvedServer && other.Tunnel.Subdomain == subdomain {
			return fmt.Errorf("subdomain %q on server %q is already used by bench %q", subdomain, resolvedServer, other.Name)
		}
	}

	fmt.Printf("Enabling tunnel for bench %q...\n", name)
	fmt.Printf("  Server:    %s (%s:%d)\n", resolvedServer, srv.Host, srv.Port)
	fmt.Printf("  Subdomain: %s.%s\n", subdomain, srv.BaseDomain)

	// ── Write frpc.toml (mode 0o600) ──────────────────────────────────────
	if err := tunnel.WriteFrpcToml(b.Dir, b, *srv, subdomain); err != nil {
		return fmt.Errorf("write frpc.toml: %w", err)
	}
	fmt.Println("  ✓ frpc.toml written")

	// ── Start or restart the frpc container ───────────────────────────────
	if tunnel.Exists(b.Name) {
		if err := tunnel.Restart(b.Dir, b.Name); err != nil {
			return fmt.Errorf("restart frpc: %w", err)
		}
		fmt.Println("  ✓ frpc container restarted")
	} else {
		if err := tunnel.Start(b.Dir, b.Name); err != nil {
			return fmt.Errorf("start frpc: %w", err)
		}
		fmt.Println("  ✓ frpc container started")
	}

	// ── Persist state before applying Frappe config ──────────────────────
	// Save first so the bench is registered as tunnel-enabled even if the
	// Frappe config step below fails because the bench is currently stopped.
	publicURL := tunnel.PublicURLFromParts(subdomain, *srv)
	if err := store.Update(name, func(rec *state.Bench) {
		rec.Tunnel = &state.TunnelState{
			Server:    resolvedServer,
			Subdomain: subdomain,
			Enabled:   true,
		}
		rec.ProxyHost = publicURL
	}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// ── Configure Frappe ──────────────────────────────────────────────────
	// Non-fatal when the bench is stopped — frpc is already running and state
	// is saved; Frappe settings will be applied on the next 'ffm start'.
	runner := bench.NewRunner(b.Name, b.Dir, verbose)
	if err := applyTunnelFrappeConfig(b, runner, publicURL); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not update Frappe settings (bench may be stopped): %v\n", err)
		fmt.Fprintf(os.Stderr, "           Run 'ffm set-proxy %s --host %s' once the bench is running.\n", name, publicURL)
	}

	fmt.Printf("\nTunnel enabled for bench %q.\n", name)
	fmt.Printf("  Public URL: %s\n", publicURL)
	return nil
}

func runTunnelOff(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	if b.Tunnel == nil || !b.Tunnel.Enabled {
		fmt.Printf("Tunnel is not enabled for bench %q.\n", name)
		return nil
	}

	fmt.Printf("Disabling tunnel for bench %q...\n", name)

	// ── Stop and remove the frpc container ────────────────────────────────
	if err := tunnel.Stop(b.Name); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not stop frpc container: %v\n", err)
	} else {
		fmt.Println("  ✓ frpc container stopped and removed")
	}

	// ── Clear tunnel state first (before reset) so state is clean even if
	//    the bench isn't running and the reset fails non-fatally. ──────────
	if err := store.Update(name, func(rec *state.Bench) {
		rec.Tunnel = nil
		rec.ProxyHost = ""
	}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// ── Restore Frappe settings to direct-access defaults ─────────────────
	// Reload b so runSetProxyReset sees the cleared ProxyHost.
	b, _ = store.Get(name)
	runner := bench.NewRunner(b.Name, b.Dir, verbose)
	if err := runSetProxyReset(b, runner, store); err != nil {
		// Non-fatal — bench may not be running; settings can be corrected manually.
		fmt.Fprintf(os.Stderr, "  warning: could not reset Frappe proxy settings: %v\n", err)
		fmt.Fprintf(os.Stderr, "           Run 'ffm set-proxy %s --reset' once the bench is running.\n", name)
	}

	return nil
}

func runTunnelStatus(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	if b.Tunnel == nil || !b.Tunnel.Enabled {
		fmt.Printf("Tunnel: not enabled for bench %q\n", name)
		fmt.Printf("  Run 'ffm tunnel %s' to enable.\n", name)
		return nil
	}

	srv, lookupErr := tunnel.Lookup(b.Tunnel.Server)
	frpcStatus := tunnel.Status(b.Name)

	fmt.Printf("Tunnel: enabled for bench %q\n", name)
	fmt.Printf("  Server:     %s\n", b.Tunnel.Server)
	if lookupErr == nil {
		fmt.Printf("  Host:       %s:%d\n", srv.Host, srv.Port)
		fmt.Printf("  Public URL: %s\n", tunnel.PublicURLFromParts(b.Tunnel.Subdomain, *srv))
	} else {
		fmt.Printf("  Server not found in tunnel.json — run 'ffm tunnel server add %s'\n", b.Tunnel.Server)
	}
	fmt.Printf("  Subdomain:  %s\n", b.Tunnel.Subdomain)
	fmt.Printf("  frpc:       %s\n", frpcStatus)
	return nil
}

func runTunnelPrint(name, serverName, subdomain string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	var srv *tunnel.Server
	switch {
	case serverName != "":
		s, err := tunnel.Lookup(serverName)
		if err != nil {
			return err
		}
		srv = s
	case b.Tunnel != nil && b.Tunnel.Server != "":
		s, err := tunnel.Lookup(b.Tunnel.Server)
		if err != nil {
			return err
		}
		srv = s
	default:
		s, _, err := tunnel.DefaultServer()
		if err != nil {
			return err
		}
		srv = s
	}

	if subdomain == "" {
		if b.Tunnel != nil && b.Tunnel.Subdomain != "" {
			subdomain = b.Tunnel.Subdomain
		} else {
			subdomain = b.Name
		}
	}

	fmt.Print(tunnel.RenderFrpcToml(b, *srv, subdomain))
	return nil
}

// applyTunnelFrappeConfig sets host_name, socketio_port, use_ssl, and
// socketio_frappe_url inside the running bench container via bench set-config.
func applyTunnelFrappeConfig(b state.Bench, runner *bench.Runner, publicURL string) error {
	globalCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench"+
			" && bench set-config -gp socketio_port 443"+
			" && bench set-config -gp use_ssl 1"+
			" && bench set-config -g socketio_frappe_url %s",
		socketioFrappeURL(b.Mode),
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", globalCmd); err != nil {
		return fmt.Errorf("apply tunnel frappe config: %w\n%s", err, out)
	}
	fmt.Println("  ✓ socketio_port = 443, use_ssl = 1")

	siteCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
		b.SiteName, publicURL,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
		return fmt.Errorf("set host_name: %w\n%s", err, out)
	}
	fmt.Printf("  ✓ host_name = %s\n", publicURL)

	if b.IsDev() {
		fmt.Println("  Restarting dev server...")
		restartCmd := "pkill -f 'honcho start' 2>/dev/null; sleep 1" +
			" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
		if _, err := runner.ExecSilent("frappe", "bash", "-c", restartCmd); err != nil {
			fmt.Println("  (dev server restart returned non-zero — may already have been stopped)")
		}
		fmt.Println("  ✓ Dev server restarting in background")
	} else {
		fmt.Printf("  → Run 'ffm restart %s' to apply changes to all services\n", b.Name)
	}

	return nil
}
