package cli

import (
	"fmt"
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/dashboard"
)

// maybeRunDashboardDaemon handles the hidden __dashboard-daemon argv entrypoint.
// The admin password comes from dashboard.json, never from argv. Errors are
// written to stderr, which startDashboardDaemon points at dashboard.log; the
// daemon used to exit 1 without a word, after the parent had already printed
// "Dashboard started".
func maybeRunDashboardDaemon() bool {
	if len(os.Args) < 2 || os.Args[1] != "__dashboard-daemon" {
		return false
	}
	cfg, err := dashboard.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: load config: %v\n", err)
		os.Exit(1)
	}
	listen := cfg.ListenAddr
	if listen == "" {
		listen = dashboard.DefaultListenAddr
	}
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--listen" && i+1 < len(os.Args) {
			i++
			listen = os.Args[i]
		}
	}
	if cfg.AdminPassword == "" {
		fmt.Fprintln(os.Stderr, "dashboard: no admin password in dashboard.json; start it with --admin-password")
		os.Exit(1)
	}
	if err := RunDashboardDaemon(listen, cfg.AdminPassword); err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
