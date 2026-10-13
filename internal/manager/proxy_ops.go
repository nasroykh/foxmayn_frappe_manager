package manager

import (
	"fmt"

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

// proxyServingWarn reports whether the proxy serves *.localhost and, when the
// container runs but is broken, prints one warning naming the fix. It never
// fails: the caller falls back to the direct published port.
func proxyServingWarn(pw ProgressWriter) bool {
	serving, broken := proxy.Probe()
	if broken != "" {
		fmt.Fprintf(pw.Stderr(), "warning: the Traefik proxy is running but broken (%s); *.localhost URLs will not answer. Run 'ffm proxy start' to recreate it.\n", broken)
	}
	return serving
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
