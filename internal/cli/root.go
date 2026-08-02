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
	root.PersistentFlags().BoolVar(&nonInteractive, "non-interactive", false,
		"Never open interactive prompts; fail with a message naming the missing flag instead. "+
			"Implied when $CI or $FFM_NON_INTERACTIVE is set, or when there is no controlling terminal "+
			"(set $FFM_INTERACTIVE=1 to force prompting back on)")

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
		newCleanLogsCmd(),
		newProxyCmd(),
		newSetProxyCmd(),
		newFfcCmd(),
		newTunnelCmd(),
		newUpdateCmd(),
		newDashboardCmd(),
	)

	return root
}

// Execute runs the root command and waits for any background update-check
// goroutine to finish writing its state file before the process exits.
func Execute() error {
	if maybeRunDashboardDaemon() {
		return nil
	}
	err := NewRootCmd().Execute()
	waitForUpdateCheck()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
