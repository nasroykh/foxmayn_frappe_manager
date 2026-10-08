package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the ffm version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				return writeJSON(jsonVersion{
					Schema: "ffm.version/v1", Version: version.Version, Commit: version.Commit,
					Date: version.Date, OS: runtime.GOOS, Arch: runtime.GOARCH,
				})
			}
			fmt.Printf("ffm %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON (schema ffm.version/v1)")
	return cmd
}
