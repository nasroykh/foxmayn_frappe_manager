package cli

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// resolveBenchName returns args[0] if provided, otherwise shows an interactive
// bench picker with the given title.
func resolveBenchName(args []string, title string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return pickBench(state.Default(), title)
}

// pickBench shows an interactive bench selector when the user omits the bench
// name on the command line. It auto-selects when only one bench exists.
func pickBench(store *state.Store, title string) (string, error) {
	benches, err := store.Load()
	if err != nil {
		return "", fmt.Errorf("load state: %w", err)
	}
	if len(benches) == 0 {
		return "", errors.New("no benches found — create one with 'ffm create <name>'")
	}
	if len(benches) == 1 {
		return benches[0].Name, nil
	}

	opts := make([]huh.Option[string], len(benches))
	for i, b := range benches {
		opts[i] = huh.NewOption(b.Name, b.Name)
	}

	var chosen string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(title).
				Options(opts...).
				Value(&chosen),
		),
	).WithKeyMap(benchPickKeyMap()).Run()
	if err != nil {
		return "", err
	}
	return chosen, nil
}

func benchPickKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	return km
}
