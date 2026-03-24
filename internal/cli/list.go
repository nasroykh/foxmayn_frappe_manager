package cli

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
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

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	runningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stoppedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	header := fmt.Sprintf("%-20s  %-10s  %-8s  %-20s  %s",
		headerStyle.Render("NAME"),
		headerStyle.Render("STATUS"),
		headerStyle.Render("PORT"),
		headerStyle.Render("SITE"),
		headerStyle.Render("BRANCH"),
	)
	fmt.Println(header)
	fmt.Println(strings.Repeat("─", 80))

	for _, b := range benches {
		status := liveStatus(b)

		statusRendered := runningStyle.Render(status)
		if strings.ToLower(status) != "running" {
			statusRendered = stoppedStyle.Render(status)
		}

		fmt.Printf("%-20s  %-10s  %-8d  %-20s  %s\n",
			nameStyle.Render(b.Name),
			statusRendered,
			b.WebPort,
			dimStyle.Render(b.SiteName),
			dimStyle.Render(b.FrappeBranch),
		)
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
