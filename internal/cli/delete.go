package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
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
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

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

	fmt.Printf("Deleting bench %q...\n", name)

	teardownBenchFiles(b)

	if err := store.Remove(name); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	fmt.Printf("Bench %q deleted.\n", name)
	return nil
}

// teardownBenchFiles runs docker compose down with volumes and removes the
// bench directory. Warnings are printed for non-fatal errors (same behavior
// as delete).
func teardownBenchFiles(b state.Bench) {
	runner := bench.NewRunner(b.Name, b.Dir, verbose)
	if err := runner.Down(true); err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker compose down: %v\n", err)
	}
	if err := os.RemoveAll(b.Dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: remove bench dir: %v\n", err)
	}
}
