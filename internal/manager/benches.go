package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// LiveStatus queries docker compose for a bench's running state.
func (s *Service) LiveStatus(b state.Bench) string {
	out, err := s.quietRunnerFor(b).PS(`{{.Service}} {{.State}}`)
	if err != nil {
		return "unknown"
	}
	return benchStatus(out)
}

// Bench states reported by LiveStatus.
const (
	StatusRunning = "running"
	// StatusPartial: some containers run, but not frappe, so the bench
	// serves nothing. Until v0.11.1 this read as "running".
	StatusPartial = "partial"
	StatusStopped = "stopped"
	StatusUnknown = "unknown"
)

// benchStatus reads `docker compose ps --format '{{.Service}} {{.State}}'`,
// which lists the running containers. The bench runs when its frappe
// container does; any other container alone makes it partial.
func benchStatus(ps string) string {
	any := false
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[1] != "running" {
			continue
		}
		if f[0] == "frappe" {
			return StatusRunning
		}
		any = true
	}
	if any {
		return StatusPartial
	}
	return StatusStopped
}

// psFormat prints one line per container: compose project, service, state.
const psFormat = `{{.Label "com.docker.compose.project"}} {{.Label "com.docker.compose.service"}} {{.State}}`

// statusSnapshot is one `docker ps -a` answer, grouped by compose project in
// the "<service> <state>" form benchStatus reads.
type statusSnapshot struct {
	at       time.Time
	byProj   map[string]string
	err      error
	hasFresh bool
}

// StatusAll classifies every bench with ONE docker call, where LiveStatus runs
// one `docker compose ps` per bench. The result is keyed by bench name and
// uses the same classification as LiveStatus: running, partial, stopped, or
// unknown (docker unreachable, or the bench's compose file is missing).
//
// When StatusTTL is set the answer is reused for that long, so a page that
// asks twice, or two browser tabs polling, share one call. It is for display
// paths only: anything that decides what to do with a bench (backup, update,
// app add) keeps calling LiveStatus for a fresh answer.
func (s *Service) StatusAll() (map[string]string, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return nil, err
	}
	snap := s.psSnapshot()
	out := make(map[string]string, len(benches))
	for _, b := range benches {
		out[b.Name] = snap.statusOf(b)
	}
	return out, nil
}

func (sn statusSnapshot) statusOf(b state.Bench) string {
	if sn.err != nil {
		return StatusUnknown
	}
	// LiveStatus runs compose against the bench's file and fails without it.
	if _, err := os.Stat(filepath.Join(b.Dir, "docker-compose.yml")); err != nil {
		return StatusUnknown
	}
	return benchStatus(sn.byProj[bench.ProjectName(b.Name)])
}

// InvalidateStatus drops the cached StatusAll answer. Jobs call it when they
// finish, so the page they redirect to is not stale.
func (s *Service) InvalidateStatus() {
	s.statusMu.Lock()
	s.statusSnap = statusSnapshot{}
	s.statusMu.Unlock()
}

func (s *Service) psSnapshot() statusSnapshot {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if s.StatusTTL > 0 && s.statusSnap.hasFresh && s.clock().Sub(s.statusSnap.at) < s.StatusTTL {
		return s.statusSnap
	}
	sn := statusSnapshot{at: s.clock(), hasFresh: true, byProj: map[string]string{}}
	out, err := execx.Command("docker", "ps", "-a",
		"--filter", "label=com.docker.compose.project", "--format", psFormat).Output()
	if err != nil {
		sn.err = err
	} else {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 3 {
				continue
			}
			sn.byProj[f[0]] += f[1] + " " + f[2] + "\n"
		}
	}
	// A failure is not cached: the next call should try docker again.
	if s.StatusTTL > 0 && err == nil {
		s.statusSnap = sn
	}
	return sn
}

// ListBenchViews returns all benches with live docker status, from one docker
// call (StatusAll) and at most one proxy check.
func (s *Service) ListBenchViews() ([]BenchView, error) {
	views, _, err := s.listBenchViews()
	return views, err
}

// listBenchViews also returns the proxy check, memoized, so DashboardOverview
// does not ask docker about the proxy a second time.
func (s *Service) listBenchViews() ([]BenchView, func() bool, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return nil, nil, err
	}
	statuses, err := s.StatusAll()
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	var up bool
	proxyUp := func() bool {
		once.Do(func() { up = proxy.IsRunning() })
		return up
	}
	out := make([]BenchView, 0, len(benches))
	for _, b := range benches {
		out = append(out, s.viewOf(b, statuses[b.Name], proxyUp))
	}
	return out, proxyUp, nil
}

func (s *Service) benchToView(b state.Bench) BenchView {
	return s.viewOf(b, s.LiveStatus(b), proxy.IsRunning)
}

func (s *Service) viewOf(b state.Bench, status string, proxyUp func() bool) BenchView {
	mode := "dev"
	if b.IsProd() {
		mode = "prod"
	}
	db := "maria"
	if b.IsPostgres() {
		db = "pg"
	}
	tunnelOn := b.Tunnel != nil && b.Tunnel.Enabled
	return BenchView{
		Name:              b.Name,
		Mode:              mode,
		DBEngine:          db,
		Status:            status,
		WebPort:           b.WebPort,
		SocketIOPort:      b.SocketIOPort,
		SiteName:          b.SiteName,
		Domain:            b.Domain,
		ProxyHost:         b.ProxyHost,
		FrappeBranch:      b.FrappeBranch,
		URL:               viewSiteURL(b, proxyUp),
		TemplatesOutdated: b.TemplateVersion < bench.TemplateVersion,
		TunnelOn:          tunnelOn,
	}
}

// GetBenchDetail returns detail for one bench including container ps output.
func (s *Service) GetBenchDetail(name string) (BenchDetail, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return BenchDetail{}, err
	}
	view := s.benchToView(b)
	detail := BenchDetail{
		BenchView:     view,
		Dir:           b.Dir,
		AdminPassword: b.AdminPassword,
		DBPassword:    b.DBPassword,
		Apps:          b.Apps,
		SiteURL:       s.siteURL(b),
	}
	if b.Tunnel != nil {
		detail.TunnelServer = b.Tunnel.Server
		detail.TunnelSub = b.Tunnel.Subdomain
	}
	runner := s.quietRunnerFor(b)
	out, err := runner.PS("")
	if err != nil {
		detail.ContainersPS = fmt.Sprintf("error: %v", err)
	} else if out == "" {
		detail.ContainersPS = "No containers found."
	} else {
		detail.ContainersPS = out
	}
	return detail, nil
}

func (s *Service) siteURL(b state.Bench) string {
	return viewSiteURL(b, proxy.IsRunning)
}

// viewSiteURL is the URL shown for a bench. proxyUp is asked only for a dev
// bench without a proxy host, the one case that depends on it.
func viewSiteURL(b state.Bench, proxyUp func() bool) string {
	if b.IsProd() {
		if b.ProxyHost != "" {
			return b.ProxyHost
		}
		if b.Domain != "" {
			return "https://" + b.Domain
		}
		return ""
	}
	if b.ProxyHost != "" {
		return b.ProxyHost
	}
	if proxyUp() {
		return "http://" + b.SiteName
	}
	return fmt.Sprintf("http://localhost:%d", b.WebPort)
}

// DashboardOverview returns stats for the admin home page.
func (s *Service) DashboardOverview(failedJobs int) (DashboardStats, error) {
	stats, _, err := s.DashboardOverviewViews(failedJobs)
	return stats, err
}

// DashboardOverviewViews returns the home page's stats together with the
// bench views they were counted from: one status query and one proxy check
// for the whole page.
func (s *Service) DashboardOverviewViews(failedJobs int) (DashboardStats, []BenchView, error) {
	views, proxyUp, err := s.listBenchViews()
	if err != nil {
		return DashboardStats{}, nil, err
	}
	stats := DashboardStats{
		TotalBenches: len(views),
		ProxyRunning: proxyUp(),
		FailedJobs:   failedJobs,
	}
	for _, v := range views {
		if v.Status == StatusRunning {
			stats.RunningBenches++
		} else {
			stats.StoppedBenches++
		}
		if v.TunnelOn {
			stats.TunnelsActive++
		}
	}
	return stats, views, nil
}
