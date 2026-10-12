package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// The create form carries every CreateInput field `ffm create` has a flag
// for, except the ones that make no sense behind a web form (--keep-on-failure,
// --show-secrets, the project mount) and --agent-read-only. It also omits, on
// purpose and not by oversight: --proxy-port and --proxy-host (the proxy is set
// with `ffm set-proxy`), --mariadb-fast-commit (a prod tuning knob: use the
// CLI flag), and the environment fallbacks FFM_NO_SEED,
// FFM_MATCH_HOST_USER and FFM_KEEP_ON_FAILURE, which are read from the
// dashboard process's environment, not the request. The rules themselves live
// in manager.Create and the bench validators; the handler only turns text into
// values and rejects what cannot be turned (a port that is not a number, half
// a port pair), then the pipeline's own checks run first thing in the job and
// fail it with the CLI's message before anything is built.

// splitList splits a comma- or newline-separated field and drops blanks.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == '\n' || c == '\r' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// formInt parses an optional integer field; blank is 0.
func formInt(r *http.Request, field string) (int, error) {
	v := strings.TrimSpace(r.FormValue(field))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a whole number, got %q", field, v)
	}
	return n, nil
}

// parseCreateForm builds the CreateInput a POST describes. Blank passwords get
// the CLI flags' defaults on a first submit; a resubmit of a re-rendered form
// (reentry=1) must carry them.
func parseCreateForm(r *http.Request) (manager.CreateInput, error) {
	var errs []string
	num := func(field string) int {
		n, err := formInt(r, field)
		if err != nil {
			errs = append(errs, err.Error())
		}
		return n
	}
	gw := num("gunicorn_workers")
	wl := num("worker_long")
	ws := num("worker_short")
	webPort := num("web_port")
	socketPort := num("socketio_port")
	if (webPort == 0) != (socketPort == 0) {
		errs = append(errs, "web_port and socketio_port must be given together")
	} else if webPort != 0 && !bench.ValidBenchPortPair(webPort, socketPort) {
		errs = append(errs, fmt.Sprintf("socketio_port must be web_port + 1000 (got %d and %d)", webPort, socketPort))
	}
	if len(errs) > 0 {
		return manager.CreateInput{}, fmt.Errorf("%s", strings.Join(errs, "; "))
	}

	adminPW := r.FormValue("admin_password")
	dbPW := r.FormValue("db_password")
	if r.FormValue("reentry") == "1" {
		// The form was shown again after an error, and the passwords are never
		// echoed back, so the fields came back blank. A blank here is the
		// user not having re-typed them, not a request for the defaults.
		var missing []string
		if adminPW == "" {
			missing = append(missing, "the admin password")
		}
		if dbPW == "" {
			missing = append(missing, "the database root password")
		}
		if len(missing) > 0 {
			return manager.CreateInput{}, fmt.Errorf("enter %s again: the form does not carry passwords across a resubmit", strings.Join(missing, " and "))
		}
	}
	if adminPW == "" {
		adminPW = "admin"
	}
	if dbPW == "" {
		dbPW = "ffm123456"
	}
	return manager.CreateInput{
		Name:              strings.TrimSpace(r.FormValue("name")),
		Mode:              r.FormValue("mode"),
		FrappeBranch:      strings.TrimSpace(r.FormValue("frappe_branch")),
		FrappeRepo:        strings.TrimSpace(r.FormValue("frappe_repo")),
		Python:            r.FormValue("python"),
		Node:              r.FormValue("node"),
		Apps:              splitList(r.FormValue("apps")),
		AdminPassword:     adminPW,
		DBPassword:        dbPW,
		DBType:            r.FormValue("db_type"),
		GithubToken:       r.FormValue("github_token"),
		Domain:            strings.TrimSpace(r.FormValue("domain")),
		NoSSL:             r.FormValue("no_ssl") == "1",
		AcmeEmail:         strings.TrimSpace(r.FormValue("acme_email")),
		MariaDBBufferPool: r.FormValue("mariadb_buffer_pool"),
		GunicornWorkers:   gw,
		WorkerLongCount:   wl,
		WorkerShortCount:  ws,
		RedisCacheMaxmem:  r.FormValue("redis_cache_maxmem"),
		RedisQueueMaxmem:  r.FormValue("redis_queue_maxmem"),
		SlowQueryLog:      r.FormValue("slow_query_log") == "1",
		DomainAliases:     splitList(r.FormValue("domain_aliases")),
		AliasTLS:          r.FormValue("alias_tls") == "1",
		MatchHostUser:     r.FormValue("match_host_user") == "1",
		FixedWebPort:      webPort,
		FixedSocketIOPort: socketPort,
		Bind:              manager.BindFor(r.FormValue("lan") == "1"),
		SSHAgent:          r.FormValue("ssh_agent") == "1",
		Agent:             r.FormValue("agent") == "1",
		NoSeed:            r.FormValue("no_seed") == "1",
	}, nil
}

// BenchCreate starts the create job. A form that cannot be parsed is shown
// again with what was typed (but not the passwords or token) and the reason.
func (h *Handler) BenchCreate(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	in, err := parseCreateForm(r)
	if err == nil {
		err = bench.ValidateName(in.Name)
	}
	if err != nil {
		auditNote(r, "error: "+clip(err.Error(), 300))
		h.renderCreateForm(w, r, http.StatusBadRequest, r.FormValue("name"), createFormFromRequest(r), err.Error())
		return
	}
	id, err := h.Svc.StartCreate(context.Background(), h.Jobs, in)
	if err != nil {
		redirectWithFlash(w, r, "/admin/benches/new", "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+id, http.StatusSeeOther)
}

type benchFormData struct {
	pageBase
	Form        manager.CreateInput
	Pythons     []string
	Nodes       []string
	AppsText    string
	AliasesText string
	// Reentry is set when the form is shown again after an error: the
	// passwords and the token were not echoed back and must be typed again.
	Reentry bool
}

// createDefaults are the form's initial values: the CLI flags' defaults.
func createDefaults() manager.CreateInput {
	return manager.CreateInput{
		Mode: "dev", FrappeBranch: bench.DefaultFrappeBranch, DBType: "mariadb",
		AdminPassword: "admin", DBPassword: "ffm123456",
		MariaDBBufferPool: "1G", GunicornWorkers: 2,
		WorkerLongCount: 1, WorkerShortCount: 1,
		RedisCacheMaxmem: "512mb", RedisQueueMaxmem: "512mb",
	}
}

func (h *Handler) renderCreateForm(w http.ResponseWriter, r *http.Request, status int, name string, form manager.CreateInput, errMsg string) {
	meta := h.base(r, "New bench", "benches")
	meta.Meta.FlashErr = errMsg
	form.Name = name
	h.renderPageStatus(w, r, status, "bench_form_body", &benchFormData{
		pageBase:    meta,
		Form:        form,
		Pythons:     bench.ImagePythons,
		Nodes:       bench.ImageNodes,
		AppsText:    strings.Join(form.Apps, ", "),
		AliasesText: strings.Join(form.DomainAliases, ", "),
		Reentry:     status != http.StatusOK,
	})
}

// BenchNew shows the create form.
func (h *Handler) BenchNew(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	h.renderCreateForm(w, r, http.StatusOK, "", createDefaults(), "")
}

// createFormFromRequest echoes a submitted form back without its secrets.
func createFormFromRequest(r *http.Request) manager.CreateInput {
	in := manager.CreateInput{
		Name:              r.FormValue("name"),
		Mode:              r.FormValue("mode"),
		FrappeBranch:      r.FormValue("frappe_branch"),
		FrappeRepo:        r.FormValue("frappe_repo"),
		Python:            r.FormValue("python"),
		Node:              r.FormValue("node"),
		DBType:            r.FormValue("db_type"),
		Domain:            r.FormValue("domain"),
		NoSSL:             r.FormValue("no_ssl") == "1",
		AcmeEmail:         r.FormValue("acme_email"),
		MariaDBBufferPool: r.FormValue("mariadb_buffer_pool"),
		RedisCacheMaxmem:  r.FormValue("redis_cache_maxmem"),
		RedisQueueMaxmem:  r.FormValue("redis_queue_maxmem"),
		SlowQueryLog:      r.FormValue("slow_query_log") == "1",
		AliasTLS:          r.FormValue("alias_tls") == "1",
		MatchHostUser:     r.FormValue("match_host_user") == "1",
		SSHAgent:          r.FormValue("ssh_agent") == "1",
		Agent:             r.FormValue("agent") == "1",
		NoSeed:            r.FormValue("no_seed") == "1",
		Bind:              manager.BindFor(r.FormValue("lan") == "1"),
	}
	in.GunicornWorkers, _ = formInt(r, "gunicorn_workers")
	in.WorkerLongCount, _ = formInt(r, "worker_long")
	in.WorkerShortCount, _ = formInt(r, "worker_short")
	in.FixedWebPort, _ = formInt(r, "web_port")
	in.FixedSocketIOPort, _ = formInt(r, "socketio_port")
	in.Apps = splitList(r.FormValue("apps"))
	in.DomainAliases = splitList(r.FormValue("domain_aliases"))
	return in
}
