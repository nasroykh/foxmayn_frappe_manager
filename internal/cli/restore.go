package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newRestoreCmd() *cobra.Command {
	var (
		noFiles         bool
		dryRun          bool
		allowNoKey      bool
		encryptionKey   string
		domain          string
		noSSL           bool
		acmeEmail       string
		reallocatePorts bool
		webPort         int
		socketIOPort    int
		adminPassword   string
		githubToken     string
		skipMigrate     bool
		pinApps         bool
		keepOnFailure   bool
		skipSpaceCheck  bool
	)

	cmd := &cobra.Command{
		Use:   "restore <archive> [name]",
		Short: "Rebuild a bench from a backup archive",
		Long: `Rebuild a bench from an archive written by 'ffm backup'.

Restore always creates a NEW bench and never writes into an existing one, so a
failed restore leaves the machine exactly as it found it. Give a name to restore
under a different one; without it the archive's own bench name is used and must
be free.

The bench is provisioned by the same pipeline as 'ffm create' — same image, apps,
mode, ports and proxy wiring — and the archived database and attachments are
restored into it. Apps are rebuilt from git at their branch head; pass
--pin-apps to check out the exact commits the backup recorded instead.

Use --dry-run to validate an archive and print what a restore would do without
touching Docker.`,
		Example: `  ffm restore ~/frappe/_backups/mybench/mybench_20260826T090000Z.ffm.tar
  ffm restore mybench_20260826T090000Z.ffm.tar staging
  ffm restore mybench_20260826T090000Z.ffm.tar --dry-run
  ffm restore prod_20260826T090000Z.ffm.tar --domain erp.example.com --pin-apps`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) > 1 {
				target = args[1]
			}
			return manager.New(verbose).Restore(manager.RestoreInput{
				Archive:                   args[0],
				TargetName:                target,
				WithFiles:                 !noFiles,
				DryRun:                    dryRun,
				AllowMissingEncryptionKey: allowNoKey,
				EncryptionKey:             encryptionKey,
				Domain:                    domain,
				NoSSL:                     noSSL,
				AcmeEmail:                 acmeEmail,
				ReallocatePorts:           reallocatePorts,
				WebPort:                   webPort,
				SocketIOPort:              socketIOPort,
				AdminPassword:             adminPassword,
				GithubToken:               githubToken,
				SkipMigrate:               skipMigrate,
				PinApps:                   pinApps,
				KeepOnFailure:             keepOnFailure,
				SkipSpaceCheck:            skipSpaceCheck,
			}, manager.CLIProgress{})
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Validate the archive and print the plan without creating anything")
	cmd.Flags().BoolVar(&noFiles, "no-files", false, "Restore the database only, without attachments")
	cmd.Flags().BoolVar(&allowNoKey, "allow-missing-encryption-key", false,
		"Restore even though the archive has no site encryption key, accepting that Password "+
			"fields (email passwords, integration secrets) will not decrypt")
	cmd.Flags().StringVar(&encryptionKey, "encryption-key", "",
		"Key for a GPG-encrypted database dump, when the archive does not carry it")
	cmd.Flags().StringVar(&domain, "domain", "", "Override the production domain recorded in the archive")
	cmd.Flags().BoolVar(&noSSL, "no-ssl", false, "Serve the restored production bench over plain HTTP")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "Let's Encrypt account address for a production restore")
	cmd.Flags().BoolVar(&reallocatePorts, "reallocate-ports", false,
		"Always assign a fresh port pair instead of reusing the archive's")
	cmd.Flags().IntVar(&webPort, "web-port", 0, "Explicit host web port")
	cmd.Flags().IntVar(&socketIOPort, "socketio-port", 0, "Explicit host Socket.IO port")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "",
		"Set a new Administrator password instead of keeping the archived one")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token for cloning private apps")
	cmd.Flags().BoolVar(&skipMigrate, "skip-migrate", false,
		"Skip 'bench migrate' after restoring — only safe when the apps are at the backed-up versions")
	cmd.Flags().BoolVar(&pinApps, "pin-apps", false,
		"Check each app out at the commit recorded in the archive instead of its branch head")
	cmd.Flags().BoolVar(&keepOnFailure, "keep-on-failure", false,
		"Leave a failed restore in place for diagnosis instead of removing it")
	cmd.Flags().BoolVar(&skipSpaceCheck, "skip-space-check", false,
		"Restore without checking free disk space first")
	return cmd
}
