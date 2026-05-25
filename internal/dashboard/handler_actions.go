package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/tunnel"
)

func (h *Handler) requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !h.validateCSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return false
	}
	_ = r.ParseForm()
	return true
}

func (h *Handler) BenchStart(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	err := h.Svc.Start(name, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Bench started", "")
}

func (h *Handler) BenchStop(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	err := h.Svc.Stop(name, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Bench stopped", "")
}

func (h *Handler) BenchRestart(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	rebuild := r.FormValue("rebuild") == "1"
	err := h.Svc.Restart(manager.RestartInput{Name: name, Rebuild: rebuild}, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Bench restarted", "")
}

func (h *Handler) BenchDelete(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	err := h.Svc.Delete(name, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/benches", "Bench deleted", "")
}

func (h *Handler) BenchRecreate(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	id, err := h.Svc.StartRecreate(context.Background(), h.Jobs, manager.RecreateInput{
		Name:            name,
		Force:           true,
		ReallocatePorts: r.FormValue("reallocate_ports") == "1",
		GithubToken:     r.FormValue("github_token"),
	})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+id, http.StatusSeeOther)
}

func (h *Handler) BenchFFC(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	err := h.Svc.SetupFFC(name, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "ffc configured", "")
}

func (h *Handler) BenchSetProxy(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	port, _ := strconv.Atoi(r.FormValue("port"))
	if port == 0 {
		port = 443
	}
	err := h.Svc.SetProxy(manager.SetProxyInput{
		Name:  name,
		Port:  port,
		Host:  r.FormValue("host"),
		NoSSL: r.FormValue("no_ssl") == "1",
		Reset: r.FormValue("reset") == "1",
	}, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Proxy settings updated", "")
}

func (h *Handler) BenchTunnelEnable(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	err := h.Svc.TunnelEnable(manager.TunnelEnableInput{
		BenchName:  name,
		ServerName: r.FormValue("server"),
		Subdomain:  r.FormValue("subdomain"),
	}, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Tunnel enabled", "")
}

func (h *Handler) BenchTunnelDisable(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	err := h.Svc.TunnelDisable(name, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Tunnel disabled", "")
}

func (h *Handler) BenchCleanLogs(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	days, _ := strconv.Atoi(r.FormValue("days"))
	if days <= 0 {
		days = 30
	}
	err := h.Svc.CleanLogs(manager.CleanLogsInput{
		BenchName: name,
		Days:      days,
		DryRun:    r.FormValue("dry_run") == "1",
	}, &manager.BufferProgress{})
	if err != nil {
		redirectWithFlash(w, r, benchPath(name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, benchPath(name), "Log cleanup finished", "")
}

func (h *Handler) ProxyStart(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	if err := h.Svc.ProxyStart(&manager.BufferProgress{}); err != nil {
		redirectWithFlash(w, r, "/admin/proxy", "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/proxy", "Proxy started", "")
}

func (h *Handler) ProxyStop(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	if err := h.Svc.ProxyStop(&manager.BufferProgress{}); err != nil {
		redirectWithFlash(w, r, "/admin/proxy", "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/proxy", "Proxy stopped", "")
}

func (h *Handler) BenchCreate(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	in := parseCreateForm(r)
	id, err := h.Svc.StartCreate(context.Background(), h.Jobs, in)
	if err != nil {
		redirectWithFlash(w, r, "/admin/benches/new", "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+id, http.StatusSeeOther)
}

func parseCreateForm(r *http.Request) manager.CreateInput {
	gw, _ := strconv.Atoi(r.FormValue("gunicorn_workers"))
	wl, _ := strconv.Atoi(r.FormValue("worker_long"))
	ws, _ := strconv.Atoi(r.FormValue("worker_short"))
	apps := strings.FieldsFunc(r.FormValue("apps"), func(c rune) bool {
		return c == ',' || c == '\n'
	})
	var trimmed []string
	for _, a := range apps {
		if t := strings.TrimSpace(a); t != "" {
			trimmed = append(trimmed, t)
		}
	}
	return manager.CreateInput{
		Name:              r.FormValue("name"),
		Mode:              r.FormValue("mode"),
		FrappeBranch:      r.FormValue("frappe_branch"),
		Apps:              trimmed,
		AdminPassword:     r.FormValue("admin_password"),
		DBPassword:        r.FormValue("db_password"),
		DBType:            r.FormValue("db_type"),
		GithubToken:       r.FormValue("github_token"),
		Domain:            r.FormValue("domain"),
		NoSSL:             r.FormValue("no_ssl") == "1",
		AcmeEmail:         r.FormValue("acme_email"),
		MariaDBBufferPool: r.FormValue("mariadb_buffer_pool"),
		GunicornWorkers:   gw,
		WorkerLongCount:   wl,
		WorkerShortCount:  ws,
		RedisCacheMaxmem:  r.FormValue("redis_cache_maxmem"),
		RedisQueueMaxmem:  r.FormValue("redis_queue_maxmem"),
		SlowQueryLog:      r.FormValue("slow_query_log") == "1",
	}
}

func (h *Handler) TunnelServerAdd(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	cfg, err := tunnel.Load()
	if err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	name := r.FormValue("name")
	port, _ := strconv.Atoi(r.FormValue("port"))
	if port == 0 {
		port = 7000
	}
	cfg.Servers[name] = tunnel.Server{
		Name:       name,
		Host:       r.FormValue("host"),
		Port:       port,
		Token:      r.FormValue("token"),
		BaseDomain: r.FormValue("base_domain"),
		TLS:        r.FormValue("tls") != "0",
	}
	if r.FormValue("default") == "1" {
		cfg.Default = name
	}
	if err := tunnel.Save(cfg); err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/tunnel-servers", fmt.Sprintf("Server %q saved", name), "")
}

func (h *Handler) TunnelServerRemove(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	cfg, err := tunnel.Load()
	if err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	delete(cfg.Servers, name)
	if cfg.Default == name {
		cfg.Default = ""
	}
	if err := tunnel.Save(cfg); err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/tunnel-servers", "Server removed", "")
}

func (h *Handler) TunnelServerUse(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	cfg, err := tunnel.Load()
	if err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	if _, ok := cfg.Servers[name]; !ok {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", "server not found")
		return
	}
	cfg.Default = name
	if err := tunnel.Save(cfg); err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/tunnel-servers", "Default server updated", "")
}
