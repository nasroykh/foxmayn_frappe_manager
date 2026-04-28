package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
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
			return runSetProxy(name, port, host, noSSL, reset, printCaddy, printNginx)
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

func runSetProxy(name string, port int, host string, noSSL, reset, printCaddy, printNginx bool) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, verbose)

	if reset {
		return runSetProxyReset(b, runner, store)
	}

	useSSL := port == 443 && !noSSL

	// ── Build and apply bench set-config commands ──────────────────────────

	fmt.Printf("Configuring bench %q for reverse proxy (port: %d, ssl: %v)...\n", name, port, useSSL)

	// Global configs: socketio_port and use_ssl
	sslVal := 0
	if useSSL {
		sslVal = 1
	}
	globalCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench"+
			" && bench set-config -gp socketio_port %d"+
			" && bench set-config -gp use_ssl %d",
		port, sslVal,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", globalCmd); err != nil {
		return fmt.Errorf("apply global config: %w\n%s", err, out)
	}
	fmt.Printf("  ✓ socketio_port = %d\n", port)
	fmt.Printf("  ✓ use_ssl       = %d\n", sslVal)

	// Per-site host_name — tells Frappe what the public URL is for link
	// generation, email, and OAuth redirects.
	proxyHost := ""
	if host != "" {
		scheme := "http"
		if useSSL {
			scheme = "https"
		}
		proxyHost = fmt.Sprintf("%s://%s", scheme, strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"))

		siteCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
			b.SiteName, proxyHost,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
			return fmt.Errorf("set host_name: %w\n%s", err, out)
		}
		fmt.Printf("  ✓ host_name     = %s\n", proxyHost)
	}

	// Dev: restart bench start in background. Prod: instruct user to restart.
	if b.IsDev() {
		fmt.Println("  Restarting dev server...")
		restartCmd := "pkill -f 'bench start' 2>/dev/null; sleep 1" +
			" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
		if _, err := runner.ExecSilent("frappe", "bash", "-c", restartCmd); err != nil {
			// Non-fatal: pkill exits 1 when no process matched, which is fine.
			fmt.Printf("  (dev server restart returned non-zero — may already have been stopped)\n")
		}
		fmt.Println("  ✓ Dev server restarting in background")
	} else {
		fmt.Printf("  → Run 'ffm restart %s' to apply changes to all services\n", name)
	}

	// Persist proxy host in state
	if err := store.Update(name, func(rec *state.Bench) {
		rec.ProxyHost = proxyHost
	}); err != nil {
		fmt.Printf("  warning: could not update state: %v\n", err)
	}

	// ── Optional config snippets ───────────────────────────────────────────

	if printCaddy {
		fmt.Println()
		printCaddySnippet(b, host)
	}
	if printNginx {
		fmt.Println()
		printNginxSnippet(b, host, useSSL)
	}

	fmt.Printf("\nDone. Bench %q is configured for reverse proxy.\n", name)
	if host == "" {
		fmt.Printf("  Tip: pass --host <domain> to also set the per-site host_name for correct link generation.\n")
	}
	if !printCaddy && !printNginx {
		fmt.Printf("  Tip: use --print-caddy or --print-nginx to get a ready-to-use reverse proxy config.\n")
	}
	return nil
}

func runSetProxyReset(b state.Bench, runner *bench.Runner, store *state.Store) error {
	fmt.Printf("Resetting bench %q to direct-access settings...\n", b.Name)

	if b.IsProd() {
		// Prod: restore to creation-time defaults (socketio on 443, SSL on, host_name = https://<domain>).
		globalCmd := "cd /workspace/frappe-bench" +
			" && bench set-config -gp socketio_port 443" +
			" && bench set-config -gp use_ssl 1" +
			fmt.Sprintf(" && bench set-config -g socketio_frappe_url %s", socketioFrappeURL("prod"))
		if out, err := runner.ExecSilent("frappe", "bash", "-c", globalCmd); err != nil {
			return fmt.Errorf("reset global config: %w\n%s", err, out)
		}
		fmt.Println("  ✓ socketio_port       = 443")
		fmt.Println("  ✓ use_ssl             = 1")
		fmt.Printf("  ✓ socketio_frappe_url = %s\n", socketioFrappeURL("prod"))

		hostName := "https://" + b.Domain
		siteCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
			b.SiteName, hostName,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
			return fmt.Errorf("reset host_name: %w\n%s", err, out)
		}
		fmt.Printf("  ✓ host_name     = %s\n", hostName)

		if err := store.Update(b.Name, func(rec *state.Bench) {
			rec.ProxyHost = hostName
		}); err != nil {
			fmt.Printf("  warning: could not update state: %v\n", err)
		}

		fmt.Printf("\nBench %q restored to production defaults.\n", b.Name)
		fmt.Printf("  → Run 'ffm restart %s' to apply changes to all services\n", b.Name)
		return nil
	}

	// Dev: restore Socket.IO to this bench's host-published port, clear ssl + host_name.
	sio := b.SocketIOPort
	if sio == 0 {
		sio = 9000 // backward compat for old state files without socketio_port
	}
	globalCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench"+
			" && bench set-config -gp socketio_port %d"+
			" && bench set-config -gp use_ssl 0"+
			" && bench set-config -g socketio_frappe_url %s",
		sio, socketioFrappeURL("dev"),
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", globalCmd); err != nil {
		return fmt.Errorf("reset global config: %w\n%s", err, out)
	}
	fmt.Printf("  ✓ socketio_port       = %d\n", sio)
	fmt.Println("  ✓ use_ssl             = 0")
	fmt.Printf("  ✓ socketio_frappe_url = %s\n", socketioFrappeURL("dev"))

	siteCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench && bench --site %s set-config host_name http://%s",
		b.SiteName, b.SiteName,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
		return fmt.Errorf("reset host_name: %w\n%s", err, out)
	}
	fmt.Printf("  ✓ host_name     = http://%s\n", b.SiteName)

	fmt.Println("  Restarting dev server...")
	restartCmd := "pkill -f 'bench start' 2>/dev/null; sleep 1" +
		" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
	if _, err := runner.ExecSilent("frappe", "bash", "-c", restartCmd); err != nil {
		fmt.Printf("  (dev server restart returned non-zero — may already have been stopped)\n")
	}
	fmt.Println("  ✓ Dev server restarting in background")

	if err := store.Update(b.Name, func(rec *state.Bench) {
		rec.ProxyHost = ""
	}); err != nil {
		fmt.Printf("  warning: could not update state: %v\n", err)
	}

	fmt.Printf("\nBench %q restored to direct access.\n", b.Name)
	fmt.Printf("  URL: http://localhost:%d\n", b.WebPort)
	return nil
}

// ── Config snippet helpers ─────────────────────────────────────────────────

func printCaddySnippet(b state.Bench, host string) {
	domain := host
	if domain == "" {
		domain = "your-domain.example.com"
	}
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")

	fmt.Printf("# Caddy config for bench %q\n", b.Name)
	fmt.Printf("# Add to your Caddyfile\n")
	fmt.Println()
	fmt.Printf("%s {\n", domain)
	fmt.Printf("    # Socket.io — must be listed before the general reverse_proxy\n")
	fmt.Printf("    reverse_proxy /socket.io/* localhost:%d\n", b.SocketIOPort)
	fmt.Println()
	fmt.Printf("    # Frappe web server\n")
	fmt.Printf("    reverse_proxy localhost:%d\n", b.WebPort)
	fmt.Printf("}\n")
}

func printNginxSnippet(b state.Bench, host string, ssl bool) {
	domain := host
	if domain == "" {
		domain = "your-domain.example.com"
	}
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")

	fmt.Printf("# Nginx config for bench %q\n", b.Name)
	fmt.Printf("# Save to /etc/nginx/sites-available/%s and symlink to sites-enabled/\n", b.Name)
	fmt.Println()

	if ssl {
		fmt.Printf("server {\n")
		fmt.Printf("    listen 80;\n")
		fmt.Printf("    server_name %s;\n", domain)
		fmt.Printf("    return 301 https://$host$request_uri;\n")
		fmt.Printf("}\n\n")
	}

	listenPort := 80
	if ssl {
		listenPort = 443
	}

	fmt.Printf("server {\n")
	if ssl {
		fmt.Printf("    listen %d ssl;\n", listenPort)
		fmt.Printf("    ssl_certificate     /etc/letsencrypt/live/%s/fullchain.pem;\n", domain)
		fmt.Printf("    ssl_certificate_key /etc/letsencrypt/live/%s/privkey.pem;\n", domain)
	} else {
		fmt.Printf("    listen %d;\n", listenPort)
	}
	fmt.Printf("    server_name %s;\n\n", domain)

	fmt.Printf("    # Socket.io — WebSocket upgrade\n")
	fmt.Printf("    location /socket.io {\n")
	fmt.Printf("        proxy_pass http://localhost:%d;\n", b.SocketIOPort)
	fmt.Printf("        proxy_http_version 1.1;\n")
	fmt.Printf("        proxy_set_header Upgrade $http_upgrade;\n")
	fmt.Printf("        proxy_set_header Connection \"upgrade\";\n")
	fmt.Printf("        proxy_set_header Host $host;\n")
	fmt.Printf("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	fmt.Printf("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	fmt.Printf("    }\n\n")

	fmt.Printf("    # Frappe web server\n")
	fmt.Printf("    location / {\n")
	fmt.Printf("        proxy_pass http://localhost:%d;\n", b.WebPort)
	fmt.Printf("        proxy_set_header Host $host;\n")
	fmt.Printf("        proxy_set_header X-Real-IP $remote_addr;\n")
	fmt.Printf("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	fmt.Printf("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	fmt.Printf("        proxy_read_timeout 120;\n")
	fmt.Printf("    }\n")
	fmt.Printf("}\n")
}
