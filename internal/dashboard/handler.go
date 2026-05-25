package dashboard

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
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
	tmpl     *template.Template
	static   http.Handler
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
	return &Handler{
		Svc:      manager.New(false),
		Jobs:     manager.NewJobStore(),
		Password: password,
		Logger:   logger,
		tmpl:     tmpl,
		static:   http.FileServer(http.FS(staticSub)),
	}, nil
}

func (h *Handler) auth(w http.ResponseWriter, r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(h.Password)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="ffm admin"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func flashFromQuery(r *http.Request) (ok, err string) {
	q := r.URL.Query()
	return q.Get("ok"), q.Get("err")
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	token := h.ensureCSRF(w, r)
	setCSRF(data, token)
	var bodyBuf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&bodyBuf, bodyTpl, data); err != nil {
		h.Logger.Error("dashboard: render body", "tpl", bodyTpl, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	meta := extractMeta(data)
	meta.CSRFToken = token
	meta.Version = version.Version
	pd := pageData{Meta: meta, BodyHTML: template.HTML(bodyBuf.String())}
	if err := h.tmpl.ExecuteTemplate(w, "layout.html", pd); err != nil {
		h.Logger.Error("dashboard: render layout", "err", err)
	}
}

func setCSRF(data any, token string) {
	switch d := data.(type) {
	case *dashboardPageData:
		d.Meta.CSRFToken = token
	case *benchesListData:
		d.Meta.CSRFToken = token
	case *benchDetailData:
		d.Meta.CSRFToken = token
	case *benchFormData:
		d.Meta.CSRFToken = token
	case *proxyPageData:
		d.Meta.CSRFToken = token
	case *tunnelServersData:
		d.Meta.CSRFToken = token
	case *jobsListData:
		d.Meta.CSRFToken = token
	case *jobDetailData:
		d.Meta.CSRFToken = token
	case *logsPageData:
		d.Meta.CSRFToken = token
	case *execPageData:
		d.Meta.CSRFToken = token
	}
}

func extractMeta(data any) PageMeta {
	switch d := data.(type) {
	case *dashboardPageData:
		return d.Meta
	case *benchesListData:
		return d.Meta
	case *benchDetailData:
		return d.Meta
	case *benchFormData:
		return d.Meta
	case *proxyPageData:
		return d.Meta
	case *tunnelServersData:
		return d.Meta
	case *jobsListData:
		return d.Meta
	case *jobDetailData:
		return d.Meta
	case *logsPageData:
		return d.Meta
	case *execPageData:
		return d.Meta
	default:
		return PageMeta{}
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
	Meta   PageMeta
	Stats  manager.DashboardStats
	Benches []manager.BenchView
}

// Dashboard is the home page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	stats, _ := h.Svc.DashboardOverview(h.Jobs.FailedCount())
	benches, _ := h.Svc.ListBenchViews()
	if len(benches) > 10 {
		benches = benches[:10]
	}
	h.renderPage(w, r, "dashboard_body", &dashboardPageData{
		Meta:    h.meta(r, "Dashboard", "dashboard"),
		Stats:   stats,
		Benches: benches,
	})
}

type benchesListData struct {
	Meta    PageMeta
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
		Meta:    h.meta(r, "Benches", "benches"),
		Benches: benches,
	})
}

type benchDetailData struct {
	Meta   PageMeta
	Bench  manager.BenchDetail
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
	h.renderPage(w, r, "bench_detail_body", &benchDetailData{
		Meta:  h.meta(r, name, "benches"),
		Bench: detail,
	})
}

type benchFormData struct {
	Meta PageMeta
	Form manager.CreateInput
}

func (h *Handler) BenchNew(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	h.renderPage(w, r, "bench_form_body", &benchFormData{
		Meta: h.meta(r, "New bench", "benches"),
		Form: manager.CreateInput{
			Mode: "dev", FrappeBranch: "version-15", DBType: "mariadb",
			AdminPassword: "admin", DBPassword: "ffm123456",
			MariaDBBufferPool: "1G", GunicornWorkers: 2,
			WorkerLongCount: 1, WorkerShortCount: 1,
			RedisCacheMaxmem: "512mb", RedisQueueMaxmem: "512mb",
		},
	})
}

type proxyPageData struct {
	Meta  PageMeta
	Proxy manager.ProxyStatusView
}

func (h *Handler) ProxyPage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	h.renderPage(w, r, "proxy_body", &proxyPageData{
		Meta:  h.meta(r, "Proxy", "proxy"),
		Proxy: h.Svc.ProxyStatus(),
	})
}

type tunnelServersData struct {
	Meta    PageMeta
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
		Meta:    h.meta(r, "Tunnel servers", "tunnels"),
		Config:  cfg,
		MaskTok: true,
	})
}

type jobsListData struct {
	Meta PageMeta
	Jobs []*manager.Job
}

func (h *Handler) JobsList(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	h.renderPage(w, r, "jobs_list_body", &jobsListData{
		Meta: h.meta(r, "Jobs", "jobs"),
		Jobs: h.Jobs.ListJobs(),
	})
}

type jobDetailData struct {
	Meta PageMeta
	Job  *manager.Job
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
		Meta: h.meta(r, "Job "+id, "jobs"),
		Job:  job,
	})
}

type logsPageData struct {
	Meta    PageMeta
	Bench   string
	Service string
}

func (h *Handler) BenchLogs(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	h.renderPage(w, r, "logs_body", &logsPageData{
		Meta:    h.meta(r, "Logs — "+name, "benches"),
		Bench:   name,
		Service: r.URL.Query().Get("service"),
	})
}

type execPageData struct {
	Meta    PageMeta
	Bench   string
	Service string
	Output  string
	Command string
}

func (h *Handler) BenchExec(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	data := &execPageData{
		Meta:    h.meta(r, "Exec — "+name, "benches"),
		Bench:   name,
		Service: "frappe",
	}
	if r.Method == http.MethodPost && h.validateCSRF(r) {
		_ = r.ParseForm()
		data.Service = r.FormValue("service")
		data.Command = r.FormValue("command")
		out, err := h.Svc.Exec(manager.ExecInput{
			BenchName: name,
			Service:   data.Service,
			Command:   data.Command,
		})
		if err != nil {
			data.Output = err.Error()
		} else {
			data.Output = out
		}
	}
	h.renderPage(w, r, "exec_body", data)
}

func redirectWithFlash(w http.ResponseWriter, r *http.Request, path, okMsg, errMsg string) {
	u, _ := url.Parse(path)
	q := u.Query()
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func benchPath(name string) string {
	return "/admin/benches/" + url.PathEscape(name)
}
