package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newRestoreCmd() *cobra.Command {
	var (
		noFiles         bool
		identity        string
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
		lan             bool
		sshAgent        bool
		skipSpaceCheck  bool
		appOverrides    []string
		showSecrets     bool
		keepDownload    bool
		headerURL       string
		frappeDB        string
		frappeFiles     string
		frappePrivate   string
		frappeConfig    string
		frappeApps      []string
		frappeMode      string
		frappeBranch    string
		frappeDBType    string
	)

	cmd := &cobra.Command{
		Use:   "restore <archive|url|target://NAME/BENCH[/FILE]> [name]",
		Short: "Rebuild a bench from a backup archive, a URL, a backup target or Frappe's own backup files",
		Long: `Rebuild a bench from an archive written by 'ffm backup'.

Restore always creates a NEW bench and never writes into an existing one, so a
failed restore leaves the machine exactly as it found it. Give a name to restore
under a different one; without it the archive's own bench name is used and must
be free.

The bench is provisioned by the same pipeline as 'ffm create' — same image, apps,
mode, ports and proxy wiring — and the archived database and attachments are
restored into it. Apps are cloned from the remote and branch the backup
recorded, at the branch head; pass --pin-apps to check out the exact commits
instead. Apps whose source is stored in the archive are unpacked from it.

--app <app>=<git-url>[@branch] replaces where an app is cloned from, and
--app frappe=<git-url>[@branch] does the same for the framework. Use it for an
app the archive has no source for — the restore refuses to start without one,
rather than failing several minutes in — or for a repository that has moved.

Use --dry-run to validate an archive and print what a restore would do without
touching Docker.

SOURCES

The archive can be:
  a local path                      ~/frappe/_backups/mybench/mybench_….ffm.tar
  an https URL                      https://files.example.com/mybench_….ffm.tar
  a file on a backup target         target://NAME/BENCH/mybench_….ffm.tar.age
  the newest archive on a target    target://NAME/BENCH

NAME is a target from 'ffm backup target list'; targets hold age-encrypted
archives, so --identity is needed. A URL must be https (plain http is refused),
carry no user name or password (use a presigned URL), and an encrypted one
(.age) needs its header next to it as <url>.header.json, or at --header-url.
The download goes into a private temporary directory under the backups
directory, is size-capped, never printed with its query string, and is deleted
when the restore ends (--keep-download keeps it). Everything after the download
is the ordinary restore, preflight checks included. --dry-run still downloads
the archive to check it, then deletes it.

FRAPPE'S OWN BACKUP FILES

--frappe-backup <db.sql.gz> builds a new bench from what 'bench backup' or
Frappe Cloud produce, for moving a site to ffm:

  ffm restore --frappe-backup site-database.sql.gz mysite \
      --files site-files.tar --private-files site-private-files.tar \
      --config site_config.json --apps erpnext,hrms

The name is required. Frappe's backup does not list the apps, so ffm reads the
dump to learn them and refuses, before building anything, if it lists one you
did not give with --apps (or --app <name>=<git-url>). From --config only the
encryption_key is used, so Password fields stay readable; the old database
credentials are never copied. The dump decides MariaDB or Postgres (--db-type
must agree) and the Frappe branch (--frappe-branch must be the same major
version). --mode prod needs --domain. The Administrator password becomes
--admin-password, or a generated one. Encrypted Frappe backups are refused:
decrypt them with gpg first.`,
		Example: `  ffm restore ~/frappe/_backups/mybench/mybench_20260826T090000Z.ffm.tar
  ffm restore mybench_20260826T090000Z.ffm.tar staging
  ffm restore mybench_20260826T090000Z.ffm.tar --dry-run
  ffm restore prod_20260826T090000Z.ffm.tar --domain erp.example.com --pin-apps
  ffm restore mybench_20260826T090000Z.ffm.tar --app my_app=git@github.com:acme/my_app.git@main
  ffm restore https://files.example.com/mybench_20260826T090000Z.ffm.tar staging
  ffm restore target://nas/mybench staging --identity ~/age-identity.txt
  ffm restore --frappe-backup site-database.sql.gz mysite --files files.tar --apps erpnext`,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("frappe-backup") {
				if len(args) != 1 {
					return usageError{errors.New("with --frappe-backup give exactly the new bench's name: " +
						"ffm restore --frappe-backup <db.sql.gz> <name>")}
				}
				return nil
			}
			return cobra.RangeArgs(1, 2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			envFallback(cmd, "admin-password", &adminPassword, envAdminPassword)
			envFallback(cmd, "github-token", &githubToken, envGitHubTokens...)
			envFallback(cmd, "encryption-key", &encryptionKey, envEncryptionKey)
			target := ""
			archiveArg := args[0]
			var frappe *manager.FrappeBackupInput
			if cmd.Flags().Changed("frappe-backup") {
				if err := checkFrappeFlags(cmd, noFiles); err != nil {
					return err
				}
				target, archiveArg = args[0], ""
				frappe = &manager.FrappeBackupInput{
					DB: frappeDB, Files: frappeFiles, PrivateFiles: frappePrivate, Config: frappeConfig,
					Apps: frappeApps, Mode: frappeMode, FrappeBranch: frappeBranch, DBType: frappeDBType,
				}
			} else {
				for _, f := range []string{"files", "private-files", "config", "apps", "mode", "frappe-branch", "db-type"} {
					if cmd.Flags().Changed(f) {
						return usageError{fmt.Errorf("--%s only applies to --frappe-backup", f)}
					}
				}
				if len(args) > 1 {
					target = args[1]
				}
			}
			// Ctrl-C stops a download and lets the temporary directory be removed.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return manager.New(verbose).Restore(manager.RestoreInput{
				Archive:                   archiveArg,
				AllowRemote:               true,
				Context:                   ctx,
				KeepDownload:              keepDownload,
				HeaderURL:                 headerURL,
				Frappe:                    frappe,
				Identity:                  identity,
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
				LAN:                       lan,
				SSHAgent:                  sshAgent,
				SkipSpaceCheck:            skipSpaceCheck,
				AppOverrides:              appOverrides,
			}, manager.CLIProgress{Reveal: showSecrets})
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Validate the archive and print the plan without creating anything")
	cmd.Flags().BoolVar(&noFiles, "no-files", false, "Restore the database only, without attachments")
	cmd.Flags().StringVar(&identity, "identity", "",
		"age identity file that decrypts an encrypted (.age) archive. Env: FFM_AGE_IDENTITY_FILE")
	cmd.Flags().BoolVar(&allowNoKey, "allow-missing-encryption-key", false,
		"Restore even though the archive has no site encryption key, accepting that Password "+
			"fields (email passwords, integration secrets) will not decrypt")
	cmd.Flags().StringVar(&encryptionKey, "encryption-key", "",
		"Key for a GPG-encrypted database dump, when the archive does not carry it."+preferEnv(envEncryptionKey))
	cmd.Flags().StringVar(&domain, "domain", "", "Override the production domain recorded in the archive")
	cmd.Flags().BoolVar(&noSSL, "no-ssl", false, "Serve the restored production bench over plain HTTP")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "Let's Encrypt account address for a production restore")
	cmd.Flags().BoolVar(&reallocatePorts, "reallocate-ports", false,
		"Always assign a fresh port pair instead of reusing the archive's")
	cmd.Flags().IntVar(&webPort, "web-port", 0, "Explicit host web port")
	cmd.Flags().IntVar(&socketIOPort, "socketio-port", 0, "Explicit host Socket.IO port")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "",
		"Set a new Administrator password instead of keeping the archived one."+preferEnv(envAdminPassword))
	cmd.Flags().StringVar(&githubToken, "github-token", "",
		"GitHub token for cloning private apps; github.com SSH sources are cloned over HTTPS with it."+preferEnv(envGitHubTokens...))
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false,
		"Print the Administrator password at the end even when stdout is not a terminal (it is masked then)")
	cmd.Flags().BoolVar(&skipMigrate, "skip-migrate", false,
		"Skip 'bench migrate' after restoring — only safe when the apps are at the backed-up versions")
	cmd.Flags().BoolVar(&pinApps, "pin-apps", false,
		"Check each app out at the commit recorded in the archive instead of its branch head")
	cmd.Flags().BoolVar(&lan, "lan", false,
		"Publish the bench's ports on all interfaces instead of 127.0.0.1 (needed to reach it from another machine). Refused with the default admin password")
	cmd.Flags().BoolVar(&sshAgent, "ssh-agent", false,
		"Forward the host SSH agent into the dev frappe container, for SSH-URL private repos. Requires SSH_AUTH_SOCK")
	cmd.Flags().BoolVar(&keepOnFailure, "keep-on-failure", false,
		"Leave a failed restore in place for diagnosis instead of removing it")
	cmd.Flags().BoolVar(&skipSpaceCheck, "skip-space-check", false,
		"Restore without checking free disk space first")
	cmd.Flags().BoolVar(&keepDownload, "keep-download", false,
		"Keep the archive downloaded from a URL or backup target instead of deleting it afterwards")
	cmd.Flags().StringVar(&headerURL, "header-url", "",
		"https URL of the header sidecar of an encrypted archive, when it is not <archive URL>.header.json")
	cmd.Flags().StringVar(&frappeDB, "frappe-backup", "",
		"Build the bench from Frappe's own backup files instead of an ffm archive: the database dump "+
			"(…-database.sql.gz). The one argument is then the new bench's name")
	cmd.Flags().StringVar(&frappeFiles, "files", "", "With --frappe-backup: the public files tarball (.tar, .tgz)")
	cmd.Flags().StringVar(&frappePrivate, "private-files", "", "With --frappe-backup: the private files tarball (.tar, .tgz)")
	cmd.Flags().StringVar(&frappeConfig, "config", "",
		"With --frappe-backup: the backup's site_config.json. Only its encryption_key is used")
	cmd.Flags().StringSliceVar(&frappeApps, "apps", nil,
		"With --frappe-backup: apps besides frappe, as for 'ffm create --apps' (erpnext,hrms or URL@branch). "+
			"Every app the dump lists must be given here or with --app")
	cmd.Flags().StringVar(&frappeMode, "mode", "dev", "With --frappe-backup: bench mode, dev or prod (prod needs --domain)")
	cmd.Flags().StringVar(&frappeBranch, "frappe-branch", "",
		"With --frappe-backup: Frappe branch (default: the dump's major version, version-N)")
	cmd.Flags().StringVar(&frappeDBType, "db-type", "",
		"With --frappe-backup: mariadb or postgres (default: what the dump is; a mismatch is refused)")
	cmd.Flags().StringArrayVar(&appOverrides, "app", nil,
		"Clone an app from this source instead: <app>=<git-url>[@branch] (repeatable; "+
			"frappe=... sets the framework's repo)")
	return cmd
}

// checkFrappeFlags refuses the combinations that would otherwise be ignored
// without a word.
func checkFrappeFlags(cmd *cobra.Command, noFiles bool) error {
	for _, f := range []string{"identity", "pin-apps", "encryption-key", "keep-download", "header-url"} {
		if cmd.Flags().Changed(f) {
			return usageError{fmt.Errorf("--%s does not apply to --frappe-backup", f)}
		}
	}
	if noFiles && (cmd.Flags().Changed("files") || cmd.Flags().Changed("private-files")) {
		return usageError{errors.New("--no-files contradicts --files / --private-files")}
	}
	return nil
}
