package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newBackupCmd() *cobra.Command {
	var (
		out            string
		label          string
		noFiles        bool
		skipSpaceCheck bool
	)

	cmd := &cobra.Command{
		Use:   "backup [name]",
		Short: "Archive a bench's site into a single restorable file",
		Long: `Write a bench's database, file attachments and configuration into one
portable archive that 'ffm restore' can turn back into a working bench.

The archive holds Frappe's own database dump, the site's public and private
files, the site and bench configuration, and each installed app's git commit —
everything needed to rebuild the bench around the data. It does not contain the
app source, the Python virtualenv or the built assets: those are reproduced at
restore time, which is what keeps the archive small and lets it restore onto a
different machine, architecture or host user.

A stopped bench is started for the duration of the backup and stopped again
afterwards.

The archive contains the database root password, the Administrator password and
the site's encryption key in plain text. It is written 0600 inside a 0700
directory; ffm warns when that cannot be enforced by the filesystem.`,
		Example: `  ffm backup
  ffm backup mybench
  ffm backup mybench --out ~/archives
  ffm backup mybench --no-files --label "before the v16 upgrade"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to back up")
			if err != nil {
				return err
			}
			return manager.New(verbose).Backup(manager.BackupInput{
				BenchName:      name,
				Out:            out,
				NoFiles:        noFiles,
				Label:          label,
				SkipSpaceCheck: skipSpaceCheck,
			}, manager.CLIProgress{})
		},
	}

	cmd.AddCommand(newBackupListCmd(), newBackupPruneCmd(), newBackupScheduleCmd(), newBackupRunDueCmd())

	cmd.Flags().StringVar(&out, "out", "",
		"Directory to write the archive into, or an explicit path ending in .tar "+
			"(default: ~/frappe/_backups/<bench>/, overridable with $FFM_BACKUPS_DIR)")
	cmd.Flags().StringVar(&label, "label", "", "Short note recorded in the archive")
	cmd.Flags().BoolVar(&noFiles, "no-files", false,
		"Skip file attachments and archive the database only")
	cmd.Flags().BoolVar(&skipSpaceCheck, "skip-space-check", false,
		"Write the archive without checking free disk space first")
	return cmd
}
