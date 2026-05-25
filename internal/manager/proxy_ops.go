package manager

import (
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
)

// ProxyStatus returns Traefik proxy status for the dashboard.
func (s *Service) ProxyStatus() ProxyStatusView {
	network := "absent"
	if proxy.IsNetworkPresent() {
		network = "present"
	}
	running := proxy.IsRunning()
	v := ProxyStatusView{
		Status:  proxy.Status(),
		Network: network,
		Running: running,
	}
	if running {
		v.Dashboard = proxy.DashboardURL()
	}
	return v
}

// ProxyStart starts the shared Traefik proxy.
func (s *Service) ProxyStart(pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	pw.Println("Starting Traefik proxy...")
	if err := proxy.Start(); err != nil {
		return err
	}
	pw.Println("Proxy is running.")
	pw.Printf("  HTTP:      http://<bench>.localhost\n")
	pw.Printf("  Dashboard: %s\n", proxy.DashboardURL())
	return nil
}

// ProxyStop stops the shared Traefik proxy.
func (s *Service) ProxyStop(pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	pw.Println("Stopping Traefik proxy...")
	if err := proxy.Stop(); err != nil {
		return err
	}
	pw.Println("Proxy stopped.")
	return nil
}
