package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/dashboard"
)

// DashboardServer is the ffm web dashboard HTTP server.
type DashboardServer struct {
	addr          string
	adminPassword string
	logger        *slog.Logger
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

// Run starts HTTP and blocks until ctx is cancelled.
func (s *DashboardServer) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if s.adminPassword != "" {
		ah, err := dashboard.NewHandler(s.adminPassword, s.logger)
		if err != nil {
			return fmt.Errorf("admin handler: %w", err)
		}
		registerAdminRoutes(mux, ah)
	}

	srv := &http.Server{
		Addr:         s.addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("dashboard listening", "addr", s.addr)
		errCh <- srv.ListenAndServe()
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
	mux.HandleFunc("POST /admin/benches", h.BenchCreate)
	mux.HandleFunc("GET /admin/benches/{name}", h.BenchDetail)
	mux.HandleFunc("POST /admin/benches/{name}/start", h.BenchStart)
	mux.HandleFunc("POST /admin/benches/{name}/stop", h.BenchStop)
	mux.HandleFunc("POST /admin/benches/{name}/restart", h.BenchRestart)
	mux.HandleFunc("POST /admin/benches/{name}/delete", h.BenchDelete)
	mux.HandleFunc("POST /admin/benches/{name}/recreate", h.BenchRecreate)
	mux.HandleFunc("POST /admin/benches/{name}/ffc", h.BenchFFC)
	mux.HandleFunc("POST /admin/benches/{name}/set-proxy", h.BenchSetProxy)
	mux.HandleFunc("POST /admin/benches/{name}/tunnel/enable", h.BenchTunnelEnable)
	mux.HandleFunc("POST /admin/benches/{name}/tunnel/disable", h.BenchTunnelDisable)
	mux.HandleFunc("POST /admin/benches/{name}/clean-logs", h.BenchCleanLogs)
	mux.HandleFunc("GET /admin/benches/{name}/logs", h.BenchLogs)
	mux.HandleFunc("GET /admin/benches/{name}/logs/stream", h.LogsStream)
	mux.HandleFunc("GET /admin/benches/{name}/exec", h.BenchExec)
	mux.HandleFunc("POST /admin/benches/{name}/exec", h.BenchExec)
	mux.HandleFunc("GET /admin/proxy", h.ProxyPage)
	mux.HandleFunc("POST /admin/proxy/start", h.ProxyStart)
	mux.HandleFunc("POST /admin/proxy/stop", h.ProxyStop)
	mux.HandleFunc("GET /admin/tunnel-servers", h.TunnelServers)
	mux.HandleFunc("POST /admin/tunnel-servers", h.TunnelServerAdd)
	mux.HandleFunc("POST /admin/tunnel-servers/{name}/remove", h.TunnelServerRemove)
	mux.HandleFunc("POST /admin/tunnel-servers/{name}/use", h.TunnelServerUse)
	mux.HandleFunc("GET /admin/jobs", h.JobsList)
	mux.HandleFunc("GET /admin/jobs/{id}", h.JobDetail)
	mux.HandleFunc("GET /admin/jobs/{id}/events", h.JobEvents)
}
