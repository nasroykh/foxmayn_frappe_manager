package manager

import (
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// SetProxy configures reverse-proxy settings for a bench.
func (s *Service) SetProxy(in SetProxyInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	return s.runSetProxy(in.Name, in.Port, in.Host, in.NoSSL, in.Reset, in.PrintCaddy, in.PrintNginx, pw)
}

func (s *Service) runSetProxy(name string, port int, host string, noSSL, reset, printCaddy, printNginx bool, pw ProgressWriter) error {
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)

	if reset {
		return s.runSetProxyReset(b, runner, pw)
	}

	useSSL := port == 443 && !noSSL

	// ── Build and apply bench set-config commands ──────────────────────────

	pw.Printf("Configuring bench %q for reverse proxy (port: %d, ssl: %v)...\n", name, port, useSSL)

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
	pw.Printf("  ✓ socketio_port = %d\n", port)
	pw.Printf("  ✓ use_ssl       = %d\n", sslVal)

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
		pw.Printf("  ✓ host_name     = %s\n", proxyHost)
	}

	if b.IsDev() {
		pw.Println("  Restarting dev server...")
		restartCmd := "pkill -f 'honcho start' 2>/dev/null; sleep 1" +
			" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
		if _, err := runner.ExecSilent("frappe", "bash", "-c", restartCmd); err != nil {
			// Non-fatal: pkill exits 1 when no process matched, which is fine.
			pw.Printf("  (dev server restart returned non-zero — may already have been stopped)\n")
		}
		pw.Println("  ✓ Dev server restarting in background")
	} else {
		pw.Printf("  → Run 'ffm restart %s' to apply changes to all services\n", name)
	}

	if err := s.UpdateBench(name, func(rec *state.Bench) {
		rec.ProxyHost = proxyHost
	}); err != nil {
		pw.Printf("  warning: could not update state: %v\n", err)
	}

	if printCaddy {
		pw.Println()
		printCaddySnippet(pw, b, host)
	}
	if printNginx {
		pw.Println()
		printNginxSnippet(pw, b, host, useSSL)
	}

	pw.Printf("\nDone. Bench %q is configured for reverse proxy.\n", name)
	if host == "" {
		pw.Printf("  Tip: pass --host <domain> to also set the per-site host_name for correct link generation.\n")
	}
	if !printCaddy && !printNginx {
		pw.Printf("  Tip: use --print-caddy or --print-nginx to get a ready-to-use reverse proxy config.\n")
	}
	return nil
}

func (s *Service) runSetProxyReset(b state.Bench, runner *bench.Runner, pw ProgressWriter) error {
	pw.Printf("Resetting bench %q to direct-access settings...\n", b.Name)

	if b.IsProd() {
		// Prod: restore to creation-time defaults (socketio on 443, SSL on, host_name = https://<domain>).
		globalCmd := "cd /workspace/frappe-bench" +
			" && bench set-config -gp socketio_port 443" +
			" && bench set-config -gp use_ssl 1" +
			fmt.Sprintf(" && bench set-config -g socketio_frappe_url %s", socketioFrappeURL("prod"))
		if out, err := runner.ExecSilent("frappe", "bash", "-c", globalCmd); err != nil {
			return fmt.Errorf("reset global config: %w\n%s", err, out)
		}
		pw.Println("  ✓ socketio_port       = 443")
		pw.Println("  ✓ use_ssl             = 1")
		pw.Printf("  ✓ socketio_frappe_url = %s\n", socketioFrappeURL("prod"))

		hostName := "https://" + b.Domain
		siteCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
			b.SiteName, hostName,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
			return fmt.Errorf("reset host_name: %w\n%s", err, out)
		}
		pw.Printf("  ✓ host_name     = %s\n", hostName)

		if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
			rec.ProxyHost = hostName
		}); err != nil {
			pw.Printf("  warning: could not update state: %v\n", err)
		}

		pw.Printf("\nBench %q restored to production defaults.\n", b.Name)
		pw.Printf("  → Run 'ffm restart %s' to apply changes to all services\n", b.Name)
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
	pw.Printf("  ✓ socketio_port       = %d\n", sio)
	pw.Println("  ✓ use_ssl             = 0")
	pw.Printf("  ✓ socketio_frappe_url = %s\n", socketioFrappeURL("dev"))

	siteCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench && bench --site %s set-config host_name http://%s",
		b.SiteName, b.SiteName,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", siteCmd); err != nil {
		return fmt.Errorf("reset host_name: %w\n%s", err, out)
	}
	pw.Printf("  ✓ host_name     = http://%s\n", b.SiteName)

	pw.Println("  Restarting dev server...")
	restartCmd := "pkill -f 'honcho start' 2>/dev/null; sleep 1" +
		" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
	if _, err := runner.ExecSilent("frappe", "bash", "-c", restartCmd); err != nil {
		pw.Printf("  (dev server restart returned non-zero — may already have been stopped)\n")
	}
	pw.Println("  ✓ Dev server restarting in background")

	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.ProxyHost = ""
	}); err != nil {
		pw.Printf("  warning: could not update state: %v\n", err)
	}

	pw.Printf("\nBench %q restored to direct access.\n", b.Name)
	pw.Printf("  URL: http://localhost:%d\n", b.WebPort)
	return nil
}

func printCaddySnippet(pw ProgressWriter, b state.Bench, host string) {
	domain := host
	if domain == "" {
		domain = "your-domain.example.com"
	}
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")

	pw.Printf("# Caddy config for bench %q\n", b.Name)
	pw.Printf("# Add to your Caddyfile\n")
	pw.Println()
	pw.Printf("%s {\n", domain)
	pw.Printf("    # Socket.io — must be listed before the general reverse_proxy\n")
	pw.Printf("    handle /socket.io/* {\n")
	pw.Printf("        header_up Origin {http.request.scheme}://{http.request.host}\n")
	pw.Printf("        reverse_proxy localhost:%d\n", b.SocketIOPort)
	pw.Printf("    }\n")
	pw.Println()
	pw.Printf("    # Frappe web server\n")
	pw.Printf("    reverse_proxy localhost:%d\n", b.WebPort)
	pw.Printf("}\n")
}

func printNginxSnippet(pw ProgressWriter, b state.Bench, host string, ssl bool) {
	domain := host
	if domain == "" {
		domain = "your-domain.example.com"
	}
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")

	pw.Printf("# Nginx config for bench %q\n", b.Name)
	pw.Printf("# Save to /etc/nginx/sites-available/%s and symlink to sites-enabled/\n", b.Name)
	pw.Println()

	if ssl {
		pw.Printf("server {\n")
		pw.Printf("    listen 80;\n")
		pw.Printf("    server_name %s;\n", domain)
		pw.Printf("    return 301 https://$host$request_uri;\n")
		pw.Printf("}\n\n")
	}

	listenPort := 80
	if ssl {
		listenPort = 443
	}

	pw.Printf("server {\n")
	if ssl {
		pw.Printf("    listen %d ssl;\n", listenPort)
		pw.Printf("    ssl_certificate     /etc/letsencrypt/live/%s/fullchain.pem;\n", domain)
		pw.Printf("    ssl_certificate_key /etc/letsencrypt/live/%s/privkey.pem;\n", domain)
	} else {
		pw.Printf("    listen %d;\n", listenPort)
	}
	pw.Printf("    server_name %s;\n\n", domain)

	pw.Printf("    # Socket.io — WebSocket upgrade\n")
	pw.Printf("    location /socket.io {\n")
	pw.Printf("        proxy_pass http://localhost:%d;\n", b.SocketIOPort)
	pw.Printf("        proxy_http_version 1.1;\n")
	pw.Printf("        proxy_set_header Upgrade $http_upgrade;\n")
	pw.Printf("        proxy_set_header Connection \"upgrade\";\n")
	pw.Printf("        proxy_set_header Host $host;\n")
	pw.Printf("        proxy_set_header Origin $scheme://$host;\n")
	pw.Printf("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	pw.Printf("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	pw.Printf("    }\n\n")

	pw.Printf("    # Frappe web server\n")
	pw.Printf("    location / {\n")
	pw.Printf("        proxy_pass http://localhost:%d;\n", b.WebPort)
	pw.Printf("        proxy_set_header Host $host;\n")
	pw.Printf("        proxy_set_header X-Real-IP $remote_addr;\n")
	pw.Printf("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	pw.Printf("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	pw.Printf("        proxy_read_timeout 120;\n")
	pw.Printf("    }\n")
	pw.Printf("}\n")
}
