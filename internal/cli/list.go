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

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all managed benches",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList()
		},
	}
}

func runList() error {
	store := state.Default()
	benches, err := store.Load()
	if err != nil {
		return err
	}

	if len(benches) == 0 {
		fmt.Println("No benches found. Run `ffm create <name>` to create one.")
		return nil
	}

	proxyUp := proxy.IsRunning()

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	runningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stoppedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	domainStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)

	header := fmt.Sprintf("%-20s  %-10s  %-8s  %-30s  %s",
		headerStyle.Render("NAME"),
		headerStyle.Render("STATUS"),
		headerStyle.Render("PORT"),
		headerStyle.Render("DOMAIN"),
		headerStyle.Render("BRANCH"),
	)
	fmt.Println(header)
	fmt.Println(strings.Repeat("─", 88))

	for _, b := range benches {
		status := liveStatus(b)

		statusRendered := runningStyle.Render(status)
		if strings.ToLower(status) != "running" {
			statusRendered = stoppedStyle.Render(status)
		}

		var domainRendered string
		domain := fmt.Sprintf("http://%s", b.SiteName)
		if proxyUp {
			domainRendered = domainStyle.Render(domain)
		} else {
			domainRendered = mutedStyle.Render(domain + " (proxy off)")
		}

		fmt.Printf("%-20s  %-10s  %-8d  %-30s  %s\n",
			nameStyle.Render(b.Name),
			statusRendered,
			b.WebPort,
			domainRendered,
			dimStyle.Render(b.FrappeBranch),
		)
	}

	if !proxyUp {
		fmt.Printf("\n  %s\n", mutedStyle.Render("Run 'ffm proxy start' to enable sitename.localhost routing."))
	}
	return nil
}

// liveStatus queries docker compose for the bench's live running state.
func liveStatus(b state.Bench) string {
	runner := bench.NewRunner(b.Name, b.Dir, false)
	out, err := runner.PS("table")
	if err != nil || out == "" {
		return "unknown"
	}
	lines := strings.Split(out, "\n")
	// Count running containers (skip header line)
	for _, line := range lines[1:] {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "running") || strings.Contains(lower, "up") {
			return "running"
		}
	}
	return "stopped"
}
