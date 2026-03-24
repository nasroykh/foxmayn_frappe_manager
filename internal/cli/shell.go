package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const frappeBenchDir = "/home/frappe/frappe-bench"

func newShellCmd() *cobra.Command {
	var service string

	cmd := &cobra.Command{
		Use:   "shell [name]",
		Short: "Open an interactive shell inside a bench container",
		Long: `Open an interactive bash shell inside the specified bench's frappe container,
landing directly in the frappe-bench directory.

Use --service to target a different container (e.g. mariadb).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to shell into")
			if err != nil {
				return err
			}
			return runShell(name, service)
		},
	}

	cmd.Flags().StringVar(&service, "service", "frappe", "Container service to shell into")
	return cmd
}

func runShell(name, service string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, false)

	// For the frappe container, land in the bench directory using zsh.
	// For other services (e.g. mariadb) fall back to bash.
	if service == "frappe" {
		return runner.ExecInDir(service, frappeBenchDir, "zsh")
	}
	return runner.Exec(service, "bash")
}
