package cli

import (
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type jsonCleanItem struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Bench string `json:"bench,omitempty"`
	Size  string `json:"size,omitempty"`
}

type jsonClean struct {
	Schema string          `json:"schema"`
	Items  []jsonCleanItem `json:"items"`
}

func newCleanCmd() *cobra.Command {
	var in manager.CleanInput
	var dryRun, yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove volumes and images left by deleted benches (and, opt-in, Docker caches)",
		Long: `List and remove the Docker volumes and images of ffm benches that no longer
exist: a bench is gone when no record, no bench directory and no running ffm
operation has it. Benches tracked under another FFM_CONFIG_DIR look gone from
here, so check the list.

--build-cache also prunes Docker's build cache and --dangling removes untagged
images. Both are shared with every other project on the host.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := manager.New(verbose)
			items, err := svc.CleanPlan(in)
			if err != nil {
				return err
			}
			if asJSON {
				out := jsonClean{Schema: "ffm.clean/v1", Items: []jsonCleanItem{}}
				for _, it := range items {
					out.Items = append(out.Items, jsonCleanItem(it))
				}
				if err := writeJSON(out); err != nil {
					return err
				}
			} else if len(items) == 0 {
				fmt.Println("Nothing to clean.")
				return nil
			} else {
				for _, it := range items {
					line := fmt.Sprintf("  %-15s %s", it.Kind, it.Name)
					if it.Size != "" {
						line += "  (" + it.Size + ")"
					}
					fmt.Println(line)
				}
			}
			if dryRun || len(items) == 0 {
				return nil
			}
			if !yes {
				if !isInteractive() {
					return mustNotPrompt("clean confirmation", "pass --yes, or --dry-run to only list")
				}
				ok := false
				if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
					Title(fmt.Sprintf("Remove these %d item(s)?", len(items))).
					Affirmative("Yes, remove").Negative("Cancel").Value(&ok))).
					WithKeyMap(benchPickKeyMap()).Run(); err != nil {
					if cancelled(err) {
						return nil
					}
					return err
				}
				if !ok {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			return svc.Clean(items, manager.CLIProgress{})
		},
	}
	cmd.Flags().BoolVar(&in.BuildCache, "build-cache", false, "Also prune Docker's build cache (shared with other projects)")
	cmd.Flags().BoolVar(&in.Dangling, "dangling", false, "Also remove untagged images (from any project)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Only list what would be removed")
	cmd.Flags().BoolVar(&yes, "yes", false, "Do not ask before removing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the list as JSON (schema ffm.clean/v1)")
	return cmd
}
