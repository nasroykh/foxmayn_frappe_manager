package cli

import (
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "delete [name]",
		Aliases: []string{"rm", "remove"},
		Short:   "Delete a bench and all its data",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to delete")
			if err != nil {
				return err
			}
			return runDelete(name, force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

func runDelete(name string, force bool) error {
	svc := manager.New(verbose)
	if !force {
		confirmed := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Delete bench %q?", name)).
					Description("This will remove all containers, volumes, and the bench directory. This cannot be undone.").
					Affirmative("Yes, delete").
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
	return svc.Delete(name, manager.CLIProgress{})
}
