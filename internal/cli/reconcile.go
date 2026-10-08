package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newReconcileCmd() *cobra.Command {
	var (
		dryRun     bool
		lan        bool
		loopback   bool
		sshAgent   bool
		noSSHAgent bool
	)

	cmd := &cobra.Command{
		Use:   "reconcile [name]",
		Short: "Apply this ffm version's templates to an existing bench without losing data",
		Long: `Regenerate a bench's docker-compose.yml from its saved settings and the
templates of this ffm version, then run 'docker compose up -d'.

Use it after upgrading ffm so template fixes reach benches created earlier.
Unlike 'ffm recreate' it keeps the databases, the workspace and the bench
record: only containers whose definition changed are replaced. --dry-run shows
the changes first.

--loopback / --lan change which host interfaces the ports are published on.
--ssh-agent / --no-ssh-agent turn host SSH agent forwarding on or off (dev).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lan && loopback {
				return fmt.Errorf("--lan and --loopback are mutually exclusive")
			}
			if sshAgent && noSSHAgent {
				return fmt.Errorf("--ssh-agent and --no-ssh-agent are mutually exclusive")
			}
			name, err := resolveBenchName(args, "Select a bench to reconcile")
			if err != nil {
				return err
			}
			in := manager.ReconcileInput{Name: name, DryRun: dryRun}
			switch {
			case lan:
				in.Bind = state.BindLAN
			case loopback:
				in.Bind = state.BindLoopback
			}
			if sshAgent || noSSHAgent {
				v := sshAgent
				in.SSHAgent = &v
			}
			return manager.New(verbose).Reconcile(in, manager.CLIProgress{})
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without touching the bench")
	cmd.Flags().BoolVar(&lan, "lan", false, "Publish the ports on all interfaces")
	cmd.Flags().BoolVar(&loopback, "loopback", false, "Publish the ports on 127.0.0.1 only")
	cmd.Flags().BoolVar(&sshAgent, "ssh-agent", false, "Forward the host SSH agent into the dev container")
	cmd.Flags().BoolVar(&noSSHAgent, "no-ssh-agent", false, "Stop forwarding the host SSH agent")
	return cmd
}
