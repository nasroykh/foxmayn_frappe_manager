package cli

import (
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newRecreateCmd() *cobra.Command {
	var (
		force           bool
		reallocatePorts bool
		githubToken     string
		proxyPort       int
		proxyHost       string
	)

	cmd := &cobra.Command{
		Use:   "recreate [name]",
		Short: "Delete a bench and provision it again from saved settings",
		Long: `Tears down the bench (containers, volumes, directory) then runs the same
pipeline as ffm create using parameters stored in ~/.config/ffm/benches.json.

By default the previous web and Socket.IO host ports are reused so local URLs
stay the same. Use --reallocate-ports to pick a fresh port pair like a new bench.

Production SSL: ACME email is read from the saved file (same as create) when needed.
Private app repos: pass --github-token if required for bench get-app.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to recreate")
			if err != nil {
				return err
			}
			var proxyPortPtr *int
			if cmd.Flags().Changed("proxy-port") {
				proxyPortPtr = &proxyPort
			}
			var proxyHostPtr *string
			if cmd.Flags().Changed("proxy-host") {
				proxyHostPtr = &proxyHost
			}
			return runRecreate(name, force, reallocatePorts, githubToken, proxyPortPtr, proxyHostPtr)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&reallocatePorts, "reallocate-ports", false, "Allocate a new web/socketio port pair instead of reusing stored ports")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token for private app repos (not stored in state)")
	cmd.Flags().IntVar(&proxyPort, "proxy-port", 0, "Override derived reverse-proxy public port for dev (socketio_port)")
	cmd.Flags().StringVar(&proxyHost, "proxy-host", "", "Override derived reverse-proxy hostname for dev (without scheme)")
	return cmd
}

func runRecreate(name string, force, reallocatePorts bool, githubToken string, proxyPortOverride *int, proxyHostOverride *string) error {
	if !force {
		confirmed := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Recreate bench %q?", name)).
					Description("This removes all containers, volumes, and the bench directory, then provisions again from saved settings. This cannot be undone.").
					Affirmative("Yes, recreate").
					Negative("Cancel").
					Value(&confirmed),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	return manager.New(verbose).Recreate(manager.RecreateInput{
		Name:              name,
		Force:             force,
		ReallocatePorts:   reallocatePorts,
		GithubToken:       githubToken,
		ProxyPortOverride: proxyPortOverride,
		ProxyHostOverride: proxyHostOverride,
	}, manager.CLIProgress{})
}
