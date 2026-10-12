package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

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

// runJob starts fn as a background job keyed by key and redirects (303) to the
// job page, where its output streams. A refusal (the key already has a job)
// goes back to failPath with the reason.
func (h *Handler) runJob(w http.ResponseWriter, r *http.Request, typ manager.JobType, key, failPath string, fn func(manager.ProgressWriter) error) {
	id, err := h.Svc.StartAction(h.Jobs, typ, key, fn)
	if err != nil {
		redirectWithFlash(w, r, failPath, "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+id, http.StatusSeeOther)
}

func (h *Handler) BenchStart(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	h.runJob(w, r, manager.JobStart, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.Start(name, pw)
	})
}

func (h *Handler) BenchStop(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	h.runJob(w, r, manager.JobStop, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.Stop(name, pw)
	})
}

func (h *Handler) BenchRestart(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	// Same as the CLI: --fresh implies --rebuild.
	fresh := r.FormValue("fresh") == "1"
	rebuild := r.FormValue("rebuild") == "1" || fresh
	h.runJob(w, r, manager.JobRestart, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.Restart(manager.RestartInput{Name: name, Rebuild: rebuild, Fresh: fresh}, pw)
	})
}

func (h *Handler) BenchDelete(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	noBackup := r.FormValue("no_backup") == "1"
	h.runJob(w, r, manager.JobDelete, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.Delete(manager.DeleteInput{Name: name, NoBackup: noBackup}, pw)
	})
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
	h.runJob(w, r, manager.JobFFC, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.SetupFFC(name, pw)
	})
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
	in := manager.SetProxyInput{
		Name:  name,
		Port:  port,
		Host:  r.FormValue("host"),
		NoSSL: r.FormValue("no_ssl") == "1",
		Reset: r.FormValue("reset") == "1",
	}
	h.runJob(w, r, manager.JobSetProxy, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.SetProxy(in, pw)
	})
}

func (h *Handler) BenchTunnelEnable(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	in := manager.TunnelEnableInput{
		BenchName:  name,
		ServerName: r.FormValue("server"),
		Subdomain:  r.FormValue("subdomain"),
	}
	h.runJob(w, r, manager.JobTunnelOn, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.TunnelEnable(in, pw)
	})
}

func (h *Handler) BenchTunnelDisable(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	h.runJob(w, r, manager.JobTunnelOff, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.TunnelDisable(name, pw)
	})
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
	in := manager.CleanLogsInput{
		BenchName: name,
		Days:      days,
		DryRun:    r.FormValue("dry_run") == "1",
	}
	h.runJob(w, r, manager.JobCleanLogs, name, benchPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.CleanLogs(in, pw)
	})
}

func (h *Handler) ProxyStart(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	h.runJob(w, r, manager.JobProxyStart, manager.ProxyJobKey, "/admin/proxy", func(pw manager.ProgressWriter) error {
		return h.Svc.ProxyStart(pw)
	})
}

func (h *Handler) ProxyStop(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	h.runJob(w, r, manager.JobProxyStop, manager.ProxyJobKey, "/admin/proxy", func(pw manager.ProgressWriter) error {
		return h.Svc.ProxyStop(pw)
	})
}

func (h *Handler) TunnelServerAdd(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.FormValue("name")
	port, _ := strconv.Atoi(r.FormValue("port"))
	if port == 0 {
		port = 7000
	}
	if err := tunnel.PutServer(tunnel.Server{
		Name:       name,
		Host:       r.FormValue("host"),
		Port:       port,
		Token:      r.FormValue("token"),
		BaseDomain: r.FormValue("base_domain"),
		TLS:        r.FormValue("tls") != "0",
	}, r.FormValue("default") == "1"); err != nil {
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
	if _, err := tunnel.RemoveServer(name); err != nil {
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
	if err := tunnel.UseServer(name); err != nil {
		redirectWithFlash(w, r, "/admin/tunnel-servers", "", err.Error())
		return
	}
	redirectWithFlash(w, r, "/admin/tunnel-servers", "Default server updated", "")
}
