package cli

import (
	"encoding/base64"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newFfcCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ffc [name]",
		Short: "Generate Frappe API keys and configure ffc inside a bench",
		Long: `Generates an API key/secret for the Administrator user and writes
~/.config/ffc/config.yaml inside the bench container.

Run this if ffc setup was skipped or failed during 'ffm create', or to
regenerate keys on an existing bench.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			return runFfcSetup(name)
		},
	}
}

func runFfcSetup(name string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	runner := bench.NewRunner(b.Name, b.Dir, verbose)

	fmt.Printf("Setting up ffc for bench %q...\n", name)

	// 1. Generate API keys via Python inside the container
	fmt.Println("  [1] Generating Frappe API keys")
	keys, err := runner.GenerateAdminAPIKeys(b.SiteName)
	if err != nil {
		return fmt.Errorf("generate API keys: %w", err)
	}

	// 2. Write ffc config
	fmt.Println("  [2] Writing ~/.config/ffc/config.yaml inside the container")
	cfg := fmt.Sprintf(
		"default_site: %s\nnumber_format: french\ndate_format: yyyy-mm-dd\nsites:\n  %s:\n    url: \"http://localhost:8000\"\n    api_key: \"%s\"\n    api_secret: \"%s\"\n",
		name, name, keys.Key, keys.Secret,
	)
	encoded := base64.StdEncoding.EncodeToString([]byte(cfg))
	writeCmd := fmt.Sprintf(
		"mkdir -p /home/frappe/.config/ffc && echo '%s' | base64 -d > /home/frappe/.config/ffc/config.yaml",
		encoded,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", writeCmd); err != nil {
		return fmt.Errorf("write ffc config: %w", err)
	}

	// 3. Verify ffc can reach the site (non-fatal — dev server may not be running)
	fmt.Println("  [3] Verifying ffc connectivity (ffc ping)")
	out, pingErr := runner.ExecSilent("frappe", "bash", "-c",
		"export PATH=\"$HOME/go/bin:/usr/local/go/bin:$PATH\"; ffc ping")

	fmt.Printf("\nffc configured on bench %q.\n", name)
	fmt.Printf("  api_key:    %s\n", keys.Key)
	fmt.Printf("  api_secret: %s\n", keys.Secret)
	fmt.Printf("  site:       %s\n", b.SiteName)
	if pingErr != nil {
		fmt.Printf("  ping:       FAILED — start the bench and run 'ffc ping' to verify\n")
		fmt.Printf("              (%s)\n", out)
	} else {
		fmt.Printf("  ping:       OK — %s\n", out)
	}
	return nil
}
