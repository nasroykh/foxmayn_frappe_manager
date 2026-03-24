package cli

import (
	"github.com/spf13/cobra"
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
	}

	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Show docker compose output")

	root.AddCommand(
		newCreateCmd(),
		newListCmd(),
		newStartCmd(),
		newStopCmd(),
		newDeleteCmd(),
		newLogsCmd(),
		newShellCmd(),
		newStatusCmd(),
		newPresetCmd(),
		newVersionCmd(),
	)

	return root
}
