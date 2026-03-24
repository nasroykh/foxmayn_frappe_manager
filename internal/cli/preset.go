package cli

import (
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

var starshipPresets = []huh.Option[string]{
	huh.NewOption("Default", ""),
	huh.NewOption("Pure", "pure-preset"),
	huh.NewOption("Tokyo Night", "tokyo-night"),
	huh.NewOption("Pastel Powerline", "pastel-powerline"),
	huh.NewOption("Gruvbox Rainbow", "gruvbox-rainbow"),
	huh.NewOption("Nerd Font Symbols", "nerd-font-symbols"),
	huh.NewOption("Bracketed Segments", "bracketed-segments"),
	huh.NewOption("Jetpack", "jetpack"),
}

func newPresetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preset [name]",
		Short: "Change the starship prompt preset for a bench",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			return runPreset(name)
		},
	}
}

func runPreset(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	var chosen string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Starship preset").
				Options(starshipPresets...).
				Value(&chosen),
		),
	).WithKeyMap(benchPickKeyMap()).Run()
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, false)

	if chosen == "" {
		// Revert to starship default by removing any existing config.
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"rm -f /home/frappe/.config/starship.toml"); err != nil {
			return fmt.Errorf("remove starship config: %w", err)
		}
	} else {
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			fmt.Sprintf("mkdir -p /home/frappe/.config && starship preset %s -o /home/frappe/.config/starship.toml", chosen)); err != nil {
			return fmt.Errorf("apply preset: %w", err)
		}
	}

	// Persist the choice in state.
	b.StarshipPreset = chosen
	benches, err := store.Load()
	if err != nil {
		return err
	}
	for i, rec := range benches {
		if rec.Name == name {
			benches[i].StarshipPreset = chosen
			break
		}
	}
	if err := store.Save(benches); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	if chosen == "" {
		fmt.Printf("Preset reset to starship default for bench %q.\n", name)
	} else {
		fmt.Printf("Preset %q applied to bench %q.\n", chosen, name)
	}
	return nil
}
