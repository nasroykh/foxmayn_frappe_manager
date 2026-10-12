package dashboard

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/tunnel"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

//go:embed templates static
var embedFS embed.FS

// PageMeta is shared layout metadata.
type PageMeta struct {
	Title     string
	ActiveNav string
	FlashOK   string
	FlashErr  string
	CSRFToken string
	Version   string
}

// Handler serves the /admin web UI.
type Handler struct {
	Svc      *manager.Service
	Jobs     *manager.JobStore
	Password string
	Logger   *slog.Logger
	// AllowExec enables the exec page and POST. Off by default; when off the
	// routes answer 404 and the bench page hides the link.
	AllowExec bool
	tmpl      *template.Template
	static    http.Handler
	limiter   *authLimiter
	audit     *auditLog
}

// NewHandler builds a Handler with parsed templates.
func NewHandler(password string, logger *slog.Logger) (*Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	funcs := template.FuncMap{
		"mask": func(s string) string {
			if len(s) <= 4 {
				return "****"
			}
			return s[:2] + "****" + s[len(s)-2:]
		},
		"statusClass": func(status string) string {
			if status == "running" {
				return "badge-active"
			}
			return "badge-expired"
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(embedFS,
		"templates/layout.html",
		"templates/pages/*.html",
	)
	if err != nil {
		return nil, err
	}
	staticSub, err := fs.Sub(embedFS, "static")
	if err != nil {
		return nil, err
	}
	svc := manager.New(false)
	// Two tabs polling, or a page that asks for the statuses twice, share one
	// `docker ps`.
	svc.StatusTTL = 2 * time.Second
	return &Handler{
		Svc:      svc,
		Jobs:     manager.NewJobStore(),
		Password: password,
		Logger:   logger,
		tmpl:     tmpl,
		static:   http.FileServer(http.FS(staticSub)),
		limiter:  newAuthLimiter(),
		audit:    &auditLog{},
	}, nil
}

// auth checks HTTP Basic credentials (the user name is ignored) behind the
// failed-login limiter. A request without credentials is only the browser's
// first probe and is answered 401 without counting as a failure.
func (h *Handler) auth(w http.ResponseWriter, r *http.Request) bool {
	ip := clientIP(r)
	if ok, retry := h.limiter.check(ip); !ok {
		secs := int(retry.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		http.Error(w, "Too many failed logins. Try again later.", http.StatusTooManyRequests)
		return false
	}
	_, pass, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="ffm admin"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	// Hash first so the comparison does not leak the password's length.
	got, want := sha256.Sum256([]byte(pass)), sha256.Sum256([]byte(h.Password))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		h.limiter.fail(ip)
		w.Header().Set("WWW-Authenticate", `Basic realm="ffm admin"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	h.limiter.success(ip)
	return true
}

// pageBase is embedded by every page's data struct. It carries the layout
// metadata and makes the page a metaHolder, so renderPage needs no per-page
// case: a new page embeds pageBase and is done.
type pageBase struct {
	Meta PageMeta
}

func (p *pageBase) pageMeta() *PageMeta { return &p.Meta }

// metaHolder is what renderPage needs of a page's data: access to its layout
// metadata, to set the CSRF token and read the title back.
type metaHolder interface {
	pageMeta() *PageMeta
}

func (h *Handler) base(r *http.Request, title, nav string) pageBase {
	return pageBase{Meta: h.meta(r, title, nav)}
}

func flashFromQuery(r *http.Request) (ok, err string) {
	return flashes.take(r.URL.Query().Get("flash"))
}

func (h *Handler) meta(r *http.Request, title, nav string) PageMeta {
	ok, err := flashFromQuery(r)
	return PageMeta{
		Title:     title,
		ActiveNav: nav,
		FlashOK:   ok,
		FlashErr:  err,
		Version:   version.Version,
	}
}

type pageData struct {
	Meta     PageMeta
	BodyHTML template.HTML
}

func (h *Handler) renderPage(w http.ResponseWriter, r *http.Request, bodyTpl string, data any) {
	h.renderPageStatus(w, r, http.StatusOK, bodyTpl, data)
}

// renderPageStatus is renderPage with a response status, for a form shown
// again with its error. Headers and the CSRF cookie are set before the status
// is written.
func (h *Handler) renderPageStatus(w http.ResponseWriter, r *http.Request, status int, bodyTpl string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	token := h.ensureCSRF(w, r)
	var meta PageMeta
	if m, ok := data.(metaHolder); ok {
		pm := m.pageMeta()
		pm.CSRFToken = token
		meta = *pm
	}
	var bodyBuf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&bodyBuf, bodyTpl, data); err != nil {
		h.Logger.Error("dashboard: render body", "tpl", bodyTpl, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	meta.Version = version.Version
	pd := pageData{Meta: meta, BodyHTML: template.HTML(bodyBuf.String())}
	w.WriteHeader(status)
	if err := h.tmpl.ExecuteTemplate(w, "layout.html", pd); err != nil {
		h.Logger.Error("dashboard: render layout", "err", err)
	}
}

// Static serves embedded assets.
func (h *Handler) Static(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/admin/static")
	h.static.ServeHTTP(w, r)
}

type dashboardPageData struct {
	pageBase
	Stats   manager.DashboardStats
	Benches []manager.BenchView
}

// Dashboard is the home page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	stats, benches, _ := h.Svc.DashboardOverviewViews(h.Jobs.FailedCount())
	if len(benches) > 10 {
		benches = benches[:10]
	}
	h.renderPage(w, r, "dashboard_body", &dashboardPageData{
		pageBase: h.base(r, "Dashboard", "dashboard"),
		Stats:    stats,
		Benches:  benches,
	})
}

type benchesListData struct {
	pageBase
	Benches []manager.BenchView
}

func (h *Handler) BenchesList(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	benches, err := h.Svc.ListBenchViews()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderPage(w, r, "benches_list_body", &benchesListData{
		pageBase: h.base(r, "Benches", "benches"),
		Benches:  benches,
	})
}

type benchDetailData struct {
	pageBase
	Bench     manager.BenchDetail
	AllowExec bool
}

func (h *Handler) BenchDetail(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	detail, err := h.Svc.GetBenchDetail(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// Passwords never go into the page: the Reveal and Copy buttons fetch
	// them on demand through BenchReveal, which audits each read.
	detail.AdminPassword = ""
	detail.DBPassword = ""
	h.renderPage(w, r, "bench_detail_body", &benchDetailData{
		pageBase:  h.base(r, name, "benches"),
		Bench:     detail,
		AllowExec: h.AllowExec,
	})
}

// BenchReveal answers POST /admin/benches/{name}/reveal with one stored
// password as JSON. It is POST-only with a CSRF token (a GET could be fetched
// cross-site or prefetched), never cached, and audited.
func (h *Handler) BenchReveal(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, err := h.Svc.GetBench(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var value string
	switch r.FormValue("field") {
	case "admin":
		value = b.AdminPassword
	case "db":
		value = b.DBPassword
	default:
		http.Error(w, "field must be admin or db", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"value": value})
}

type proxyPageData struct {
	pageBase
	Proxy manager.ProxyStatusView
}

func (h *Handler) ProxyPage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	h.renderPage(w, r, "proxy_body", &proxyPageData{
		pageBase: h.base(r, "Proxy", "proxy"),
		Proxy:    h.Svc.ProxyStatus(),
	})
}

type tunnelServersData struct {
	pageBase
	Config  tunnel.Config
	MaskTok bool
}

func (h *Handler) TunnelServers(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	cfg, err := tunnel.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderPage(w, r, "tunnel_servers_body", &tunnelServersData{
		pageBase: h.base(r, "Tunnel servers", "tunnels"),
		Config:   cfg,
		MaskTok:  true,
	})
}

type jobsListData struct {
	pageBase
	Jobs []*manager.Job
}

func (h *Handler) JobsList(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	h.renderPage(w, r, "jobs_list_body", &jobsListData{
		pageBase: h.base(r, "Jobs", "jobs"),
		Jobs:     h.Jobs.ListJobs(),
	})
}

type jobDetailData struct {
	pageBase
	Job *manager.Job
}

func (h *Handler) JobDetail(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	id := r.PathValue("id")
	job, ok := h.Jobs.GetJob(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.renderPage(w, r, "job_detail_body", &jobDetailData{
		pageBase: h.base(r, "Job "+id, "jobs"),
		Job:      job,
	})
}

type logsPageData struct {
	pageBase
	Bench   string
	Service string
}

func (h *Handler) BenchLogs(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	service := r.URL.Query().Get("service")
	if !h.serviceAllowed(w, name, service) {
		return
	}
	h.renderPage(w, r, "logs_body", &logsPageData{
		pageBase: h.base(r, "Logs — "+name, "benches"),
		Bench:    name,
		Service:  service,
	})
}

// serviceAllowed checks service against the bench's compose services and
// writes the error response itself. An empty service means "all" (logs) and
// passes. The name reaches a docker argument list and a container exec, so it
// must be one the bench defines.
func (h *Handler) serviceAllowed(w http.ResponseWriter, bench, service string) bool {
	if service == "" {
		return true
	}
	err := h.Svc.ValidateService(bench, service)
	switch {
	case err == nil:
		return true
	case errors.Is(err, manager.ErrUnknownService):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, state.ErrNotFound):
		http.Error(w, "bench not found", http.StatusNotFound)
	default:
		h.Logger.Error("dashboard: list compose services", "bench", bench, "err", err)
		http.Error(w, "could not list the bench's services", http.StatusBadGateway)
	}
	return false
}

type execPageData struct {
	pageBase
	Bench   string
	Service string
	Output  string
	Command string
}

// BenchExec is the one-shot command page. It is off unless the dashboard was
// started with --allow-exec: running commands in a container is remote code
// execution behind a single shared password. Every attempt is audited, and the
// command is written to the log before it runs.
func (h *Handler) BenchExec(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	if !h.AllowExec {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	data := &execPageData{
		pageBase: h.base(r, "Exec — "+name, "benches"),
		Bench:    name,
		Service:  "frappe",
	}
	if r.Method == http.MethodPost {
		if !h.validateCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		data.Service = r.FormValue("service")
		data.Command = r.FormValue("command")
		if data.Service == "" {
			data.Service = "frappe"
		}
		if !h.serviceAllowed(w, name, data.Service) {
			return
		}
		if err := h.audit.write(auditEntry{
			Time: time.Now().UTC(), IP: clientIP(r), Action: "/admin/benches/{name}/exec",
			Bench: name, Service: data.Service, Command: data.Command, Result: "started",
		}); err != nil {
			h.Logger.Error("dashboard: audit write failed, refusing exec", "err", err)
			http.Error(w, "audit log unavailable; command not run", http.StatusInternalServerError)
			return
		}
		out, err := h.Svc.Exec(manager.ExecInput{
			BenchName: name,
			Service:   data.Service,
			Command:   data.Command,
		})
		if err != nil {
			data.Output = err.Error()
			auditNote(r, "error: "+clip(err.Error(), 300))
		} else {
			data.Output = out
		}
	}
	h.renderPage(w, r, "exec_body", data)
}

// redirectWithFlash redirects to path and shows okMsg or errMsg once on the
// next page. The messages stay on the server and the URL carries only a random
// id: error text often embeds command output, and in a query string it would
// land in browser history, proxy logs and Referer headers.
func redirectWithFlash(w http.ResponseWriter, r *http.Request, path, okMsg, errMsg string) {
	switch {
	case errMsg != "":
		auditNote(r, "error: "+clip(errMsg, 300))
	case okMsg != "":
		auditNote(r, "ok")
	}
	u, _ := url.Parse(path)
	if okMsg != "" || errMsg != "" {
		q := u.Query()
		q.Set("flash", flashes.put(okMsg, errMsg))
		u.RawQuery = q.Encode()
	}
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func benchPath(name string) string {
	return "/admin/benches/" + url.PathEscape(name)
}
