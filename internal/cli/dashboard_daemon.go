package cli

import (
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/dashboard"
)

// maybeRunDashboardDaemon handles the hidden __dashboard-daemon argv entrypoint.
func maybeRunDashboardDaemon() bool {
	if len(os.Args) < 2 || os.Args[1] != "__dashboard-daemon" {
		return false
	}
	listen := dashboard.DefaultListenAddr
	adminPassword := ""
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--listen":
			if i+1 < len(os.Args) {
				i++
				listen = os.Args[i]
			}
		case "--admin-password":
			if i+1 < len(os.Args) {
				i++
				adminPassword = os.Args[i]
			}
		}
	}
	if err := RunDashboardDaemon(listen, adminPassword); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
