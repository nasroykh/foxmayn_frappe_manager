package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newConsoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "console [name]",
		Short: "Open bench console (Python REPL with the site loaded)",
		Long: `Run 'bench --site <site> console': an IPython session with frappe
initialised and connected to the bench's site.

It needs a terminal. For one-shot calls use
'ffm shell <bench> --exec "bench --site <site> execute <dotted.path>"'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isInteractive() {
				return usageError{errors.New("ffm console needs an interactive terminal; use 'ffm shell <bench> --exec \"bench --site <site> execute …\"' instead")}
			}
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			svc := manager.New(verbose)
			argv, err := svc.SiteCommand(name, "console")
			if err != nil {
				return err
			}
			return svc.Interactive(name, argv...)
		},
	}
}
