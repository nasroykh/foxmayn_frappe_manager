package cli

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show per-container status for a bench",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(args[0])
		},
	}
}

func runStatus(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, false)
	out, err := runner.PS("")
	if err != nil {
		return fmt.Errorf("docker compose ps: %w", err)
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	fmt.Printf("%s  (web: :%d  socketio: :%d  site: %s)\n\n",
		titleStyle.Render(b.Name),
		b.WebPort, b.SocketIOPort, b.SiteName,
	)

	if out == "" {
		fmt.Println("  No containers found.")
		return nil
	}

	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
	return nil
}
