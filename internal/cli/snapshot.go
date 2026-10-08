package cli

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type jsonSnapshot struct {
	Name      string            `json:"name"`
	CreatedAt string            `json:"created_at"`
	Files     bool              `json:"files"`
	Size      int64             `json:"size"`
	Commits   map[string]string `json:"commits,omitempty"`
}

type jsonSnapshots struct {
	Schema    string         `json:"schema"`
	Bench     string         `json:"bench"`
	Snapshots []jsonSnapshot `json:"snapshots"`
}

func newSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Quick local rollback points for a bench's database (create / list / restore / delete)",
		Long: `Snapshots are rollback points for one bench: the site's database, and with
--files its attachments, as Frappe's own backup writes them. Take one before a
migrate, a git switch or an agent run, and roll back in seconds to a minute.

They live in the bench's workspace/.ffm-snapshots/, stay on this host and go
away with the bench (delete, recreate). They are not backups: use 'ffm backup'
for a copy that survives the bench.

'ffm snapshot restore' replaces the running site's database in place, unlike
'ffm restore', which only ever creates a new bench.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}

	var create manager.SnapshotInput
	createCmd := &cobra.Command{
		Use:   "create [bench]",
		Short: "Take a snapshot",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to snapshot")
			if err != nil {
				return err
			}
			create.Bench = name
			_, err = manager.New(verbose).CreateSnapshot(create, manager.CLIProgress{})
			return err
		},
	}
	createCmd.Flags().StringVar(&create.Name, "name", "", "Snapshot name (default: the UTC time, e.g. 20261008-104500)")
	createCmd.Flags().BoolVar(&create.Files, "files", false, "Also capture the site's public and private files")

	var asJSON bool
	listCmd := &cobra.Command{
		Use:     "list [bench]",
		Aliases: []string{"ls"},
		Short:   "List a bench's snapshots, newest first",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			snaps, err := manager.New(verbose).ListSnapshots(name)
			if err != nil {
				return err
			}
			if asJSON {
				out := jsonSnapshots{Schema: "ffm.snapshots/v1", Bench: name, Snapshots: []jsonSnapshot{}}
				for _, s := range snaps {
					out.Snapshots = append(out.Snapshots, jsonSnapshot{Name: s.Name, CreatedAt: jsonTime(s.CreatedAt),
						Files: s.Files, Size: s.Size, Commits: s.Commits})
				}
				return writeJSON(out)
			}
			if len(snaps) == 0 {
				fmt.Printf("Bench %q has no snapshots.\n", name)
				return nil
			}
			for _, s := range snaps {
				files := ""
				if s.Files {
					files = "  +files"
				}
				fmt.Printf("  %-24s %s  %8s%s\n", s.Name, s.CreatedAt.Local().Format("2006-01-02 15:04:05"), humanSize(s.Size), files)
			}
			return nil
		},
	}
	listCmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.snapshots/v1)")

	var restore manager.RestoreSnapshotInput
	var yes bool
	restoreCmd := &cobra.Command{
		Use:   "restore [bench]",
		Short: "Roll the bench's site back to a snapshot (the newest unless --name)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if yes && len(args) == 0 {
				return usageError{errors.New("--yes requires the bench name: ffm snapshot restore <bench> --yes")}
			}
			name, err := resolveBenchName(args, "Select a bench to roll back")
			if err != nil {
				return err
			}
			if !yes {
				if !isInteractive() {
					return mustNotPrompt("rollback confirmation", "pass --yes")
				}
				target := restore.Name
				if target == "" {
					target = "the newest snapshot"
				}
				ok := false
				if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
					Title(fmt.Sprintf("Roll %q back to %s?", name, target)).
					Description("The site's current database is replaced. Take a snapshot first if you may want it back.").
					Affirmative("Yes, roll back").Negative("Cancel").Value(&ok))).
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
			restore.Bench = name
			_, err = manager.New(verbose).RestoreSnapshot(restore, manager.CLIProgress{})
			return err
		},
	}
	restoreCmd.Flags().StringVar(&restore.Name, "name", "", "Snapshot to restore (default: the newest)")
	restoreCmd.Flags().BoolVar(&restore.Migrate, "migrate", false, "Run bench migrate afterwards")
	restoreCmd.Flags().BoolVar(&yes, "yes", false, "Do not ask first (the bench name is then required)")

	var delName string
	deleteCmd := &cobra.Command{
		Use:     "delete [bench]",
		Aliases: []string{"rm"},
		Short:   "Delete a snapshot",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if delName == "" {
				return usageError{errors.New("--name is required")}
			}
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			if err := manager.New(verbose).DeleteSnapshot(name, delName); err != nil {
				return err
			}
			fmt.Printf("Deleted snapshot %q of %q.\n", delName, name)
			return nil
		},
	}
	deleteCmd.Flags().StringVar(&delName, "name", "", "Snapshot to delete")

	cmd.AddCommand(createCmd, listCmd, restoreCmd, deleteCmd)
	return cmd
}
