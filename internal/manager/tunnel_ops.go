package manager

import (
	"fmt"
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/tunnel"
)

// TunnelEnable exposes a bench via the configured VPS tunnel.
func (s *Service) TunnelEnable(in TunnelEnableInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.BenchName)
	if err != nil {
		return err
	}

	var srv *tunnel.Server
	var resolvedServer string
	switch {
	case in.ServerName != "":
		srv, err = tunnel.Lookup(in.ServerName)
		if err != nil {
			return err
		}
		resolvedServer = in.ServerName
	case b.Tunnel != nil && b.Tunnel.Server != "":
		srv, err = tunnel.Lookup(b.Tunnel.Server)
		if err != nil {
			return err
		}
		resolvedServer = b.Tunnel.Server
	default:
		var sName string
		srv, sName, err = tunnel.DefaultServer()
		if err != nil {
			return err
		}
		resolvedServer = sName
	}

	subdomain := in.Subdomain
	if subdomain == "" {
		if b.Tunnel != nil && b.Tunnel.Subdomain != "" {
			subdomain = b.Tunnel.Subdomain
		} else {
			subdomain = b.Name
		}
	}

	benches, err := s.LoadBenches()
	if err != nil {
		return err
	}
	for _, other := range benches {
		if other.Name == in.BenchName || other.Tunnel == nil || !other.Tunnel.Enabled {
			continue
		}
		if other.Tunnel.Server == resolvedServer && other.Tunnel.Subdomain == subdomain {
			return fmt.Errorf("subdomain %q on server %q is already used by bench %q", subdomain, resolvedServer, other.Name)
		}
	}

	pw.Printf("Enabling tunnel for bench %q...\n", in.BenchName)
	pw.Printf("  Server:    %s (%s:%d)\n", resolvedServer, srv.Host, srv.Port)
	pw.Printf("  Subdomain: %s.%s\n", subdomain, srv.BaseDomain)

	if err := tunnel.WriteFrpcToml(b.Dir, b, *srv, subdomain); err != nil {
		return fmt.Errorf("write frpc.toml: %w", err)
	}
	pw.Println("  ✓ frpc.toml written")

	if tunnel.Exists(b.Name) {
		if err := tunnel.Restart(b.Dir, b.Name); err != nil {
			return fmt.Errorf("restart frpc: %w", err)
		}
		pw.Println("  ✓ frpc container restarted")
	} else {
		if err := tunnel.Start(b.Dir, b.Name); err != nil {
			return fmt.Errorf("start frpc: %w", err)
		}
		pw.Println("  ✓ frpc container started")
	}

	publicURL := tunnel.PublicURLFromParts(subdomain, *srv)
	if err := s.UpdateBench(in.BenchName, func(rec *state.Bench) {
		rec.Tunnel = &state.TunnelState{
			Server:    resolvedServer,
			Subdomain: subdomain,
			Enabled:   true,
		}
		rec.ProxyHost = publicURL
	}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
	if err := s.applyTunnelFrappeConfig(b, runner, publicURL, pw); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not update Frappe settings (bench may be stopped): %v\n", err)
	}

	pw.Printf("\nTunnel enabled for bench %q.\n", in.BenchName)
	pw.Printf("  Public URL: %s\n", publicURL)
	return nil
}

// TunnelDisable stops the tunnel and restores direct-access settings.
func (s *Service) TunnelDisable(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	if b.Tunnel == nil || !b.Tunnel.Enabled {
		pw.Printf("Tunnel is not enabled for bench %q.\n", name)
		return nil
	}

	pw.Printf("Disabling tunnel for bench %q...\n", name)
	if err := tunnel.Stop(b.Name); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not stop frpc container: %v\n", err)
	} else {
		pw.Println("  ✓ frpc container stopped and removed")
	}

	if err := s.UpdateBench(name, func(rec *state.Bench) {
		rec.Tunnel = nil
		rec.ProxyHost = ""
	}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	b, _ = s.GetBench(name)
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
	if err := s.runSetProxyReset(b, runner, pw); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not reset Frappe proxy settings: %v\n", err)
	}
	return nil
}

// TunnelStatusText returns human-readable tunnel status.
func (s *Service) TunnelStatusText(name string) (string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return "", err
	}
	if b.Tunnel == nil || !b.Tunnel.Enabled {
		return fmt.Sprintf("Tunnel: not enabled for bench %q", name), nil
	}
	srv, lookupErr := tunnel.Lookup(b.Tunnel.Server)
	frpcStatus := tunnel.Status(b.Name)
	var out string
	out = fmt.Sprintf("Tunnel: enabled for bench %q\n  Server:     %s\n", name, b.Tunnel.Server)
	if lookupErr == nil {
		out += fmt.Sprintf("  Host:       %s:%d\n  Public URL: %s\n", srv.Host, srv.Port, tunnel.PublicURLFromParts(b.Tunnel.Subdomain, *srv))
	}
	out += fmt.Sprintf("  Subdomain:  %s\n  frpc:       %s\n", b.Tunnel.Subdomain, frpcStatus)
	return out, nil
}

// TunnelRenderFrpc returns the frpc.toml content without applying.
func (s *Service) TunnelRenderFrpc(name, serverName, subdomain string) (string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return "", err
	}
	var srv *tunnel.Server
	switch {
	case serverName != "":
		srv, err = tunnel.Lookup(serverName)
	case b.Tunnel != nil && b.Tunnel.Server != "":
		srv, err = tunnel.Lookup(b.Tunnel.Server)
	default:
		srv, _, err = tunnel.DefaultServer()
	}
	if err != nil {
		return "", err
	}
	if subdomain == "" {
		if b.Tunnel != nil && b.Tunnel.Subdomain != "" {
			subdomain = b.Tunnel.Subdomain
		} else {
			subdomain = b.Name
		}
	}
	return tunnel.RenderFrpcToml(b, *srv, subdomain), nil
}

func (s *Service) applyTunnelFrappeConfig(b state.Bench, runner *bench.Runner, publicURL string, pw ProgressWriter) error {
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
	pw.Println("  ✓ socketio_port = 443, use_ssl = 1")

	siteCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
		b.SiteName, publicURL,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
		return fmt.Errorf("set host_name: %w\n%s", err, out)
	}
	pw.Printf("  ✓ host_name = %s\n", publicURL)

	if b.IsDev() {
		pw.Println("  Restarting dev server...")
		restartCmd := "pkill -f 'honcho start' 2>/dev/null; sleep 1" +
			" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
		if _, err := runner.ExecSilent("frappe", "bash", "-c", restartCmd); err != nil {
			pw.Println("  (dev server restart returned non-zero — may already have been stopped)")
		}
		pw.Println("  ✓ Dev server restarting in background")
	} else {
		pw.Printf("  → Run 'ffm restart %s' to apply changes to all services\n", b.Name)
	}
	return nil
}
