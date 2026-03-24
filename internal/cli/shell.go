package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const frappeBenchDir = "/home/frappe/frappe-bench"

func newShellCmd() *cobra.Command {
	var service, execCmd string

	cmd := &cobra.Command{
		Use:   "shell [name]",
		Short: "Open an interactive shell inside a bench container",
		Long: `Open an interactive zsh shell inside the specified bench's frappe container,
landing directly in the frappe-bench directory.

Use --exec to run a single command and return its output without opening an
interactive shell. The command runs with the bench PATH already set (Go, ffc, etc.).

Use --service to target a different container (e.g. mariadb).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to shell into")
			if err != nil {
				return err
			}
			return runShell(name, service, execCmd)
		},
	}

	cmd.Flags().StringVar(&service, "service", "frappe", "Container service to shell into")
	cmd.Flags().StringVar(&execCmd, "exec", "", "Run a command non-interactively and print its output")
	return cmd
}

func runShell(name, service, execCmd string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, false)

	if execCmd != "" {
		// Non-interactive: run the command with PATH set, stream output to terminal.
		script := `export PATH="$HOME/go/bin:/usr/local/go/bin:$PATH"; ` + execCmd
		return runner.ExecOutputInDir(service, frappeBenchDir, "bash", "-c", script)
	}

	// Interactive shell.
	if service == "frappe" {
		return runner.ExecInDir(service, frappeBenchDir, "zsh")
	}
	return runner.Exec(service, "bash")
}
