package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// resolveBenchName returns args[0] if provided, auto-detects the bench from
// the current working directory when inside ~/frappe/<name>/*, or otherwise
// shows an interactive bench picker with the given title.
func resolveBenchName(args []string, title string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}

	// Auto-detect bench from CWD when inside the benches directory.
	if name := benchNameFromCWD(); name != "" {
		return name, nil
	}

	return pickBench(state.Default(), title)
}

// benchNameFromCWD returns the bench name if the current working directory is
// under ~/frappe/<name>/ and that name exists in the state store. Returns ""
// if detection fails for any reason so the caller can fall back to the picker.
func benchNameFromCWD() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	benchesDir := config.BenchesDir()
	// Ensure both paths use the same separator and trailing-slash form.
	rel, err := filepath.Rel(benchesDir, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}

	// rel is now "<name>" or "<name>/..." — the first component is the bench name.
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	name := parts[0]
	if name == "" || name == "." {
		return ""
	}

	// Confirm the name is tracked in the state store.
	store := state.Default()
	benches, err := store.Load()
	if err != nil {
		return ""
	}
	for _, b := range benches {
		if b.Name == name {
			return name
		}
	}
	return ""
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
