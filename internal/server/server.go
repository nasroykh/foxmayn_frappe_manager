package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/dashboard"
)

// DashboardServer is the ffm web dashboard HTTP server.
type DashboardServer struct {
	addr          string
	adminPassword string
	logger        *slog.Logger
	tlsCert       string
	tlsKey        string
	insecureHTTP  bool
	allowExec     bool
}

// Option configures DashboardServer.
type Option func(*DashboardServer)

// WithAddr sets listen address.
func WithAddr(addr string) Option {
	return func(s *DashboardServer) { s.addr = addr }
}

// WithAdminPassword enables /admin routes.
func WithAdminPassword(p string) Option {
	return func(s *DashboardServer) { s.adminPassword = p }
}

// WithTLS serves HTTPS from a PEM certificate and key file.
func WithTLS(certFile, keyFile string) Option {
	return func(s *DashboardServer) { s.tlsCert, s.tlsKey = certFile, keyFile }
}

// WithInsecureHTTP allows a non-loopback listen address without TLS.
func WithInsecureHTTP(v bool) Option {
	return func(s *DashboardServer) { s.insecureHTTP = v }
}

// WithAllowExec enables the dashboard's one-shot command page.
func WithAllowExec(v bool) Option {
	return func(s *DashboardServer) { s.allowExec = v }
}

// WithLogger sets logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *DashboardServer) { s.logger = l }
}

// New creates a DashboardServer.
func New(opts ...Option) *DashboardServer {
	s := &DashboardServer{
		addr:   dashboard.DefaultListenAddr,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CheckListen reports whether the dashboard may listen on addr. A
// non-loopback address needs TLS or an explicit insecure-HTTP opt-in: the
// admin password and the CSRF cookie would otherwise cross the network in
// clear. Run enforces it, so the background daemon is covered too; the CLI
// calls it first to fail before spawning one.
func CheckListen(addr string, haveTLS, insecure bool) error {
	return checkListen(addr, haveTLS, insecure)
}

func checkListen(addr string, haveTLS, insecure bool) error {
	if haveTLS || insecure {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to listen on %q without TLS: the admin password would cross the network in clear. "+
		"Pass --tls-cert and --tls-key, or --insecure-http to accept that (or bind 127.0.0.1 and put a TLS reverse proxy in front)", addr)
}

// contentSecurityPolicy has no 'unsafe-inline' and no third-party origin:
// every script and stylesheet is a file served by the dashboard itself.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

// securityHeaders sets the browser hardening headers on every response,
// including 401s and CrossOriginProtection's 403s, which is why it is the
// outermost wrapper. HSTS is sent only over TLS: on plain HTTP it is ignored
// at best and, through a proxy, a lockout at worst.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// Handler builds the dashboard's full handler: routes, cross-origin check and
// security headers.
func (s *DashboardServer) Handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if s.adminPassword != "" {
		ah, err := dashboard.NewHandler(s.adminPassword, s.logger)
		if err != nil {
			return nil, fmt.Errorf("admin handler: %w", err)
		}
		ah.AllowExec = s.allowExec
		registerAdminRoutes(mux, ah)
	}

	// Cross-origin unsafe requests (browser Sec-Fetch-Site / Origin) are
	// refused before any handler runs; the HMAC CSRF token stays as the
	// second layer.
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux)), nil
}

// Run starts the server (HTTPS when a certificate is configured) and blocks
// until ctx is cancelled.
func (s *DashboardServer) Run(ctx context.Context) error {
	if (s.tlsCert == "") != (s.tlsKey == "") {
		return errors.New("TLS needs both a certificate and a key")
	}
	useTLS := s.tlsCert != ""
	if err := checkListen(s.addr, useTLS, s.insecureHTTP); err != nil {
		return err
	}
	handler, err := s.Handler()
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE
		IdleTimeout:       120 * time.Second,
	}
	if useTLS {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("dashboard listening", "addr", s.addr, "tls", useTLS, "exec", s.allowExec)
		if useTLS {
			errCh <- srv.ListenAndServeTLS(s.tlsCert, s.tlsKey)
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func registerAdminRoutes(mux *http.ServeMux, h *dashboard.Handler) {
	mux.HandleFunc("GET /admin/static/", h.Static)
	mux.HandleFunc("GET /admin", h.Dashboard)
	mux.HandleFunc("GET /admin/benches", h.BenchesList)
	mux.HandleFunc("GET /admin/benches/new", h.BenchNew)
	mux.HandleFunc("POST /admin/benches", h.Audited(h.BenchCreate))
	mux.HandleFunc("GET /admin/benches/{name}", h.BenchDetail)
	mux.HandleFunc("POST /admin/benches/{name}/start", h.Audited(h.BenchStart))
	mux.HandleFunc("POST /admin/benches/{name}/stop", h.Audited(h.BenchStop))
	mux.HandleFunc("POST /admin/benches/{name}/restart", h.Audited(h.BenchRestart))
	mux.HandleFunc("POST /admin/benches/{name}/delete", h.Audited(h.BenchDelete))
	mux.HandleFunc("POST /admin/benches/{name}/recreate", h.Audited(h.BenchRecreate))
	mux.HandleFunc("POST /admin/benches/{name}/ffc", h.Audited(h.BenchFFC))
	mux.HandleFunc("POST /admin/benches/{name}/set-proxy", h.Audited(h.BenchSetProxy))
	mux.HandleFunc("POST /admin/benches/{name}/tunnel/enable", h.Audited(h.BenchTunnelEnable))
	mux.HandleFunc("POST /admin/benches/{name}/tunnel/disable", h.Audited(h.BenchTunnelDisable))
	mux.HandleFunc("POST /admin/benches/{name}/clean-logs", h.Audited(h.BenchCleanLogs))
	mux.HandleFunc("GET /admin/benches/{name}/logs", h.BenchLogs)
	mux.HandleFunc("GET /admin/benches/{name}/logs/stream", h.LogsStream)
	mux.HandleFunc("POST /admin/benches/{name}/reveal", h.Audited(h.BenchReveal))
	mux.HandleFunc("GET /admin/benches/{name}/exec", h.BenchExec)
	mux.HandleFunc("POST /admin/benches/{name}/exec", h.Audited(h.BenchExec))
	mux.HandleFunc("GET /admin/proxy", h.ProxyPage)
	mux.HandleFunc("POST /admin/proxy/start", h.Audited(h.ProxyStart))
	mux.HandleFunc("POST /admin/proxy/stop", h.Audited(h.ProxyStop))
	mux.HandleFunc("GET /admin/tunnel-servers", h.TunnelServers)
	mux.HandleFunc("POST /admin/tunnel-servers", h.Audited(h.TunnelServerAdd))
	mux.HandleFunc("POST /admin/tunnel-servers/{name}/remove", h.Audited(h.TunnelServerRemove))
	mux.HandleFunc("POST /admin/tunnel-servers/{name}/use", h.Audited(h.TunnelServerUse))
	mux.HandleFunc("GET /admin/jobs", h.JobsList)
	mux.HandleFunc("GET /admin/jobs/{id}", h.JobDetail)
	mux.HandleFunc("GET /admin/jobs/{id}/events", h.JobEvents)
}
