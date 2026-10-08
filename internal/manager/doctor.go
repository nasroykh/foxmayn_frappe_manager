package manager

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Check outcomes.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
	CheckSkip = "skip"
)

// Check is one doctor finding.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// BenchHealth is a bench's doctor report.
type BenchHealth struct {
	Bench  string  `json:"bench"`
	Checks []Check `json:"checks"`
}

// Worst is the report's worst outcome.
func (h BenchHealth) Worst() string {
	worst := CheckOK
	for _, c := range h.Checks {
		switch {
		case c.Status == CheckFail:
			return CheckFail
		case c.Status == CheckWarn:
			worst = CheckWarn
		}
	}
	return worst
}

// Failures lists the failed checks, one line each.
func (h BenchHealth) Failures() []string {
	var out []string
	for _, c := range h.Checks {
		if c.Status == CheckFail {
			out = append(out, c.Name+": "+c.Detail)
		}
	}
	return out
}

// doctorTimeout bounds each network probe.
var doctorTimeout = 10 * time.Second

// Doctor checks a bench's health. It never changes anything.
//
// "Running" is not health: a bench whose containers are up can still have no
// worker on a queue (jobs wait forever), a scheduler that is off, a backup
// schedule that stopped producing archives, or a certificate about to expire.
func (s *Service) Doctor(name string) (BenchHealth, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return BenchHealth{}, err
	}
	h := BenchHealth{Bench: b.Name}
	add := func(c Check) { h.Checks = append(h.Checks, c) }

	status := s.LiveStatus(b)
	add(containersCheck(s, b, status))
	if status != StatusRunning {
		for _, n := range []string{"site", "database", "scheduler", "workers"} {
			add(Check{Name: n, Status: CheckSkip, Detail: "the bench is not running"})
		}
	} else {
		add(siteCheck(b))
		if b.IsDev() && proxy.IsRunning() {
			add(traefikCheck(b))
		}
		for _, c := range jobChecks(s, b) {
			add(c)
		}
	}
	if b.IsProd() && b.TLSMode == state.TLSLetsEncrypt {
		add(tlsCheck(b))
	}
	add(backupCheck(b, s.clock()))
	add(diskCheck(b))
	if b.TemplateVersion < bench.TemplateVersion {
		add(Check{Name: "templates", Status: CheckWarn,
			Detail: fmt.Sprintf("built from template version %d (this ffm has %d); see 'ffm reconcile %s --dry-run'", b.TemplateVersion, bench.TemplateVersion, b.Name)})
	} else {
		add(Check{Name: "templates", Status: CheckOK, Detail: fmt.Sprintf("version %d", b.TemplateVersion)})
	}
	return h, nil
}

func containersCheck(s *Service, b state.Bench, status string) Check {
	c := Check{Name: "containers"}
	out, err := s.quietRunnerFor(b).PS(`{{.Service}} {{.State}} {{.ID}}`)
	if err != nil {
		return Check{Name: "containers", Status: CheckFail, Detail: "docker could not be asked: " + err.Error()}
	}
	var running, ids []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 3 && f[1] == "running" {
			running = append(running, f[0])
			ids = append(ids, f[2])
		}
	}
	restarts := 0
	if len(ids) > 0 {
		args := append([]string{"inspect", "--format", "{{.RestartCount}}"}, ids...)
		if o, err := execx.Command("docker", args...).Output(); err == nil {
			for _, f := range strings.Fields(string(o)) {
				n, _ := strconv.Atoi(f)
				restarts += n
			}
		}
	}
	switch status {
	case StatusRunning:
		c.Status, c.Detail = CheckOK, fmt.Sprintf("%d running (%s)", len(running), strings.Join(running, ", "))
	case StatusPartial:
		c.Status, c.Detail = CheckFail, fmt.Sprintf("the frappe container is down; running: %s ('ffm restart %s')", strings.Join(running, ", "), b.Name)
	case StatusStopped:
		c.Status, c.Detail = CheckWarn, "the bench is stopped"
	default:
		c.Status, c.Detail = CheckFail, "docker could not be asked"
	}
	if restarts > 3 && c.Status == CheckOK {
		c.Status, c.Detail = CheckWarn, c.Detail+fmt.Sprintf("; %d restarts since the containers were created", restarts)
	}
	return c
}

func httpPing(url, host string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Timeout: doctorTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "pong") {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func siteCheck(b state.Bench) Check {
	host := ""
	if b.IsProd() {
		host = b.Domain
	}
	if err := httpPing(fmt.Sprintf("http://localhost:%d/api/method/ping", b.WebPort), host); err != nil {
		return Check{Name: "site", Status: CheckFail, Detail: "/api/method/ping on the web port: " + err.Error()}
	}
	return Check{Name: "site", Status: CheckOK, Detail: fmt.Sprintf("/api/method/ping answers on port %d", b.WebPort)}
}

func traefikCheck(b state.Bench) Check {
	if err := httpPing("http://127.0.0.1/api/method/ping", b.SiteName); err != nil {
		return Check{Name: "proxy route", Status: CheckWarn, Detail: "http://" + b.SiteName + " through Traefik: " + err.Error()}
	}
	return Check{Name: "proxy route", Status: CheckOK, Detail: "http://" + b.SiteName + " answers through Traefik"}
}

// doctorJobsScript reports the scheduler state and, for every queue, its
// backlog and how many RQ workers listen on it. It connects to the
// database, so its failure is the database check's.
const doctorJobsScript = `import sys, json, frappe
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
from frappe.utils.background_jobs import get_queue_list, get_queue, get_workers
from frappe.utils.scheduler import is_scheduler_inactive
workers = get_workers()
out = {"scheduler_inactive": bool(is_scheduler_inactive(verbose=False)), "queues": {}}
for q in get_queue_list():
    Q = get_queue(q)
    out["queues"][q] = {"pending": Q.count, "workers": sum(1 for w in workers if Q.name in w.queue_names())}
print("FFM_DOCTOR=" + json.dumps(out))
`

type jobsReport struct {
	SchedulerInactive bool `json:"scheduler_inactive"`
	Queues            map[string]struct {
		Pending int `json:"pending"`
		Workers int `json:"workers"`
	} `json:"queues"`
}

func jobChecks(s *Service, b state.Bench) []Check {
	out, err := runSiteScript(s.quietRunnerFor(b), b.SiteName, doctorJobsScript)
	var rep jobsReport
	found := false
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "FFM_DOCTOR="); ok {
			found = json.Unmarshal([]byte(v), &rep) == nil
		}
	}
	if err != nil || !found {
		return []Check{{Name: "database", Status: CheckFail, Detail: "could not open the site: " + lastLines(scrubSecrets(out, benchSecrets(b)...), 2)}}
	}
	checks := []Check{{Name: "database", Status: CheckOK, Detail: "the site connects"}}
	sched := Check{Name: "scheduler", Status: CheckOK, Detail: "enabled"}
	if rep.SchedulerInactive {
		sched.Status, sched.Detail = CheckFail, "disabled or paused: scheduled jobs do not run (bench --site "+b.SiteName+" enable-scheduler)"
		if b.IsDev() {
			sched.Status = CheckWarn
		}
	}
	checks = append(checks, sched)
	var missing, backlog []string
	for q, st := range rep.Queues {
		if st.Workers == 0 {
			missing = append(missing, fmt.Sprintf("%s (%d waiting)", q, st.Pending))
		} else if st.Pending > 100 {
			backlog = append(backlog, fmt.Sprintf("%s: %d waiting", q, st.Pending))
		}
	}
	w := Check{Name: "workers", Status: CheckOK, Detail: fmt.Sprintf("every queue has a worker (%d queues)", len(rep.Queues))}
	switch {
	case len(missing) > 0:
		w.Status, w.Detail = CheckFail, "no worker listens on: "+strings.Join(missing, ", ")+"; jobs there never run"
	case len(backlog) > 0:
		w.Status, w.Detail = CheckWarn, "backlog: "+strings.Join(backlog, ", ")
	}
	return append(checks, w)
}

func tlsCheck(b state.Bench) Check {
	c := Check{Name: "certificate"}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: doctorTimeout}, "tcp", "127.0.0.1:443",
		&tls.Config{ServerName: b.Domain, InsecureSkipVerify: true}) //nolint:gosec // read below, verified separately
	if err != nil {
		c.Status, c.Detail = CheckFail, "TLS on :443: "+err.Error()
		return c
	}
	certs := conn.ConnectionState().PeerCertificates
	conn.Close()
	if len(certs) == 0 {
		c.Status, c.Detail = CheckFail, "no certificate"
		return c
	}
	leaf := certs[0]
	pool := x509.NewCertPool()
	for _, ic := range certs[1:] {
		pool.AddCert(ic)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: b.Domain, Intermediates: pool}); err != nil {
		c.Status, c.Detail = CheckFail, "the certificate served for "+b.Domain+" is not valid: "+err.Error()
		return c
	}
	left := time.Until(leaf.NotAfter)
	days := int(left.Hours() / 24)
	switch {
	case left < 3*24*time.Hour:
		c.Status = CheckFail
	case left < 14*24*time.Hour:
		c.Status = CheckWarn
	default:
		c.Status = CheckOK
	}
	c.Detail = fmt.Sprintf("valid for %s, expires in %d days (%s)", b.Domain, days, leaf.NotAfter.Format("2006-01-02"))
	return c
}

func backupCheck(b state.Bench, now time.Time) Check {
	c := Check{Name: "backups"}
	if b.BackupSchedule == nil || !b.BackupSchedule.Enabled {
		c.Status, c.Detail = CheckWarn, "no schedule ('ffm backup schedule "+b.Name+"')"
		if b.IsDev() {
			c.Status = CheckSkip
		}
		return c
	}
	st, err := scheduleStatus(b)
	if err != nil {
		c.Status, c.Detail = CheckFail, err.Error()
		return c
	}
	every := time.Duration(b.BackupSchedule.EveryHours) * time.Hour
	if st.LastSuccess.IsZero() {
		c.Status, c.Detail = CheckFail, "scheduled, but no scheduled archive exists yet"
		if st.Run.Error != "" {
			c.Detail += "; last attempt: " + st.Run.Error
		}
		return c
	}
	age := now.Sub(st.LastSuccess)
	c.Detail = fmt.Sprintf("last scheduled archive %s ago (every %dh)", age.Round(time.Minute), b.BackupSchedule.EveryHours)
	switch {
	case age > every+2*time.Hour:
		c.Status = CheckFail
		c.Detail += "; overdue ('ffm backup scheduler status')"
		if st.Run.Error != "" {
			c.Detail += "; last attempt: " + st.Run.Error
		}
	default:
		c.Status = CheckOK
	}
	return c
}

func diskCheck(b state.Bench) Check {
	c := Check{Name: "disk", Status: CheckOK}
	var parts []string
	for _, d := range []struct{ label, path string }{{"bench", b.Dir}, {"backups", config.BackupsDir()}} {
		free, ok := freeBytes(d.path)
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s free", d.label, humanBytes(int64(free))))
		switch {
		case free < 2<<30:
			c.Status = CheckFail
		case free < 10<<30 && c.Status == CheckOK:
			c.Status = CheckWarn
		}
	}
	if len(parts) == 0 {
		return Check{Name: "disk", Status: CheckSkip, Detail: "free space unknown on this platform"}
	}
	c.Detail = strings.Join(parts, ", ")
	return c
}
