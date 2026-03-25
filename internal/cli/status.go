package cli

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Show per-container status for a bench",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to inspect")
			if err != nil {
				return err
			}
			return runStatus(name)
		},
	}
}

func runStatus(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("7"))

	label := func(k, v string) {
		fmt.Printf("  %s  %s\n", labelStyle.Render(fmt.Sprintf("%-12s", k)), valStyle.Render(v))
	}

	fmt.Println(titleStyle.Render(b.Name))
	label("site", b.SiteName)
	label("url (port)", fmt.Sprintf("http://localhost:%d", b.WebPort))
	if b.ProxyHost != "" {
		label("url (proxy)", b.ProxyHost)
	} else if proxy.IsRunning() {
		label("url (domain)", fmt.Sprintf("http://%s", b.SiteName))
	} else {
		label("url (domain)", fmt.Sprintf("http://%s  (run 'ffm proxy start')", b.SiteName))
	}
	label("branch", b.FrappeBranch)
	label("admin", fmt.Sprintf("administrator / %s", b.AdminPassword))
	if b.DBPassword != "" {
		label("db root", b.DBPassword)
	}
	if len(b.Apps) > 0 {
		label("apps", strings.Join(b.Apps, ", "))
	}
	label("web port", fmt.Sprintf("%d", b.WebPort))
	label("socketio", fmt.Sprintf("%d", b.SocketIOPort))
	fmt.Println()

	runner := bench.NewRunner(b.Name, b.Dir, false)
	out, err := runner.PS("")
	if err != nil {
		return fmt.Errorf("docker compose ps: %w", err)
	}

	if out == "" {
		fmt.Println("  No containers found.")
		return nil
	}

	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
	return nil
}
