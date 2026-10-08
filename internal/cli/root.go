package cli

import (
	"fmt"

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
		Long: `ffm creates and manages Frappe benches with Docker Compose: development
benches with Claude Code and ffc built in, production benches behind Traefik with
Let's Encrypt, backups with scheduled retention and restore, domain aliases,
a VPS tunnel and a web dashboard.`,
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
		// Skip when the user is already running `ffm update` to avoid duplicate
		// output, and for the hourly `ffm backup run-due`, which would otherwise
		// make a network call every hour on every host.
		if cmd.Name() != "update" && cmd.CommandPath() != "ffm backup run-due" {
			runUpdateCheck()
		}
		return nil
	}

	root.AddCommand(
		newCreateCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newListCmd(),
		newStartCmd(),
		newStopCmd(),
		newRestartCmd(),
		newDeleteCmd(),
		newRecreateCmd(),
		newReconcileCmd(),
		newLogsCmd(),
		newShellCmd(),
		newStatusCmd(),
		newCleanLogsCmd(),
		newProxyCmd(),
		newSetProxyCmd(),
		newDomainCmd(),
		newFfcCmd(),
		newTunnelCmd(),
		newUpdateCmd(),
		newDashboardCmd(),
		newVersionCmd(),
	)

	return root
}

// Execute runs the root command and waits for any background update-check
// goroutine to finish writing its state file before the process exits.
func Execute() error {
	if maybeRunDashboardDaemon() {
		return nil
	}
	root := NewRootCmd()
	markUsageErrors(root)
	err := root.Execute()
	waitForUpdateCheck()
	// cobra has already written the error to stderr — SilenceErrors is not set —
	// so printing it here too showed every failure twice. Return it for the exit
	// code alone.
	return err
}
