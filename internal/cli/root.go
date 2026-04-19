package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

// verbose is the package-level flag shared by all commands via the root.
var verbose bool

// NewRootCmd builds and returns the root cobra command with all subcommands
// registered.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ffm",
		Short: "Foxmayn Frappe Manager — local Frappe bench lifecycle manager",
		Long: `ffm wraps frappe_docker's devcontainer compose pattern so you can
create, start, stop, and delete Frappe development benches with a single command.`,
		SilenceUsage: true,
		Version: fmt.Sprintf("%s (commit %s, built %s)",
			version.Version, version.Commit, version.Date),
	}

	root.SetVersionTemplate("ffm {{.Version}}\n")

	// --verbose (no -v shorthand; -v is reserved for --version)
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "Show docker compose output")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		// Skip when the user is already running `ffm update` to avoid duplicate output.
		if cmd.Name() != "update" {
			runUpdateCheck()
		}
		return nil
	}

	root.AddCommand(
		newCreateCmd(),
		newListCmd(),
		newStartCmd(),
		newStopCmd(),
		newRestartCmd(),
		newDeleteCmd(),
		newRecreateCmd(),
		newLogsCmd(),
		newShellCmd(),
		newStatusCmd(),
		newProxyCmd(),
		newSetProxyCmd(),
		newFfcCmd(),
		newUpdateCmd(),
	)

	return root
}

// Execute runs the root command and waits for any background update-check
// goroutine to finish writing its state file before the process exits.
func Execute() error {
	err := NewRootCmd().Execute()
	waitForUpdateCheck()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
