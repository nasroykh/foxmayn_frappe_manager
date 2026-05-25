package manager

import (
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// LiveStatus queries docker compose for a bench's running state.
func (s *Service) LiveStatus(b state.Bench) string {
	runner := bench.NewRunner(b.Name, b.Dir, false)
	out, err := runner.PS("table")
	if err != nil || out == "" {
		return "unknown"
	}
	for _, line := range strings.Split(out, "\n")[1:] {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "running") || strings.Contains(lower, "up") {
			return "running"
		}
	}
	return "stopped"
}

// ListBenchViews returns all benches with live docker status.
func (s *Service) ListBenchViews() ([]BenchView, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return nil, err
	}
	out := make([]BenchView, 0, len(benches))
	for _, b := range benches {
		out = append(out, s.benchToView(b))
	}
	return out, nil
}

func (s *Service) benchToView(b state.Bench) BenchView {
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
		Name:         b.Name,
		Mode:         mode,
		DBEngine:     db,
		Status:       s.LiveStatus(b),
		WebPort:      b.WebPort,
		SocketIOPort: b.SocketIOPort,
		SiteName:     b.SiteName,
		Domain:       b.Domain,
		ProxyHost:    b.ProxyHost,
		FrappeBranch: b.FrappeBranch,
		TunnelOn:     tunnelOn,
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
	runner := bench.NewRunner(b.Name, b.Dir, false)
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
	if proxy.IsRunning() {
		return "http://" + b.SiteName
	}
	return fmt.Sprintf("http://localhost:%d", b.WebPort)
}

// DashboardOverview returns stats for the admin home page.
func (s *Service) DashboardOverview(failedJobs int) (DashboardStats, error) {
	views, err := s.ListBenchViews()
	if err != nil {
		return DashboardStats{}, err
	}
	stats := DashboardStats{
		TotalBenches:  len(views),
		ProxyRunning:  proxy.IsRunning(),
		FailedJobs:    failedJobs,
	}
	for _, v := range views {
		if v.Status == "running" {
			stats.RunningBenches++
		} else {
			stats.StoppedBenches++
		}
		if v.TunnelOn {
			stats.TunnelsActive++
		}
	}
	return stats, nil
}
