package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug [bench]",
		Short: "Run a dev bench's web server under debugpy (on / off / status)",
		Long: `'ffm debug on' installs debugpy into the bench's virtualenv, runs the web
process under it (without auto-reload) and writes .vscode/launch.json if the
bench has none. Attach on localhost:<web port + 5>, or on port 8005 from inside
the container (a devcontainer window). 'ffm debug off' restores the normal
server. Bare 'ffm debug' prints the state.

Refused on a bench whose ports are published on every interface: debugpy runs
any code it is sent.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			on, port, err := manager.New(verbose).DebugStatus(name)
			if err != nil {
				return err
			}
			if on {
				fmt.Printf("Debugging is on for %q: debugpy on localhost:%d.\n", name, port)
			} else {
				fmt.Printf("Debugging is off for %q.\n", name)
			}
			return nil
		},
	}
	for _, on := range []bool{true, false} {
		use, short := "on [bench]", "Run the web server under debugpy"
		if !on {
			use, short = "off [bench]", "Run the web server normally again"
		}
		cmd.AddCommand(&cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				name, err := resolveBenchName(args, "Select a bench")
				if err != nil {
					return err
				}
				return manager.New(verbose).Debug(name, on, manager.CLIProgress{})
			},
		})
	}
	return cmd
}
