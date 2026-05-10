package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newRestartCmd() *cobra.Command {
	var rebuild bool

	cmd := &cobra.Command{
		Use:   "restart [name]",
		Short: "Restart a bench (stop then start)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to restart")
			if err != nil {
				return err
			}
			if err := runStop(name); err != nil {
				return err
			}
			if rebuild {
				store := state.Default()
				b, err := store.Get(name)
				if err != nil {
					return err
				}
				fmt.Printf("Updating Dockerfile for bench %q...\n", name)
				if err := bench.WriteDockerfile(b.Dir, bench.ComposeData{Mode: b.Mode, DBType: b.DBEngine()}); err != nil {
					return fmt.Errorf("write Dockerfile: %w", err)
				}
				if b.IsProd() {
					fmt.Printf("Updating wsgi.py for bench %q...\n", name)
					if err := bench.WriteWsgiWrapper(b.Dir, b.SiteName); err != nil {
						return fmt.Errorf("write wsgi.py: %w", err)
					}
					if err := bench.PatchAuthenticateJs(b.Dir); err != nil {
						fmt.Fprintf(os.Stderr, "warning: could not patch authenticate.js: %v\n", err)
					}
					if err := bench.PatchUtilsJs(b.Dir); err != nil {
						fmt.Fprintf(os.Stderr, "warning: could not patch utils.js: %v\n", err)
					}
				}
				runner := bench.NewRunner(b.Name, b.Dir, verbose)
				fmt.Printf("Rebuilding image for bench %q...\n", name)
				if err := runner.Build(); err != nil {
					return fmt.Errorf("docker compose build: %w", err)
				}
			}
			return runStart(name)
		},
	}

	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "Rebuild the Docker image before starting")
	return cmd
}
