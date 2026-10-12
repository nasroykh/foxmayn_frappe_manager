package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newRotateCmd() *cobra.Command {
	var (
		dbRoot, admin, asJSON, showSecrets bool
	)
	cmd := &cobra.Command{
		Use:   "rotate-credentials [bench]",
		Short: "Replace a bench's database root and Administrator passwords with new random ones",
		Long: `Generates a new database root password and a new Administrator password for a
running bench and applies them: the root password in the database itself (every
root account) and in the bench's secrets/db_root_password file, the
Administrator password in the site, and both in ffm's record. The new root
password is then used to open a connection from the frappe container.

With --db-root or --admin only that one is rotated. The site's own database
user (the one Frappe connects as) is not touched. The bench must be running.

A bench built by an older ffm keeps its database root password in the container
environment: run 'ffm reconcile' on it first (or rotate with --admin only).

The new values are printed only when stdout is a terminal or --show-secrets is
given; otherwise read them with 'ffm status <bench> --show-secrets'. Backups
taken before the rotation still carry the old passwords.`,
		Example: `  ffm rotate-credentials mybench
  ffm rotate-credentials mybench --admin --json --show-secrets`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			// --json keeps stdout a single document: progress goes to stderr.
			var pw manager.ProgressWriter = manager.CLIProgress{Reveal: showSecrets}
			if asJSON {
				pw = writerProgress{os.Stderr}
			}
			res, err := manager.New(verbose).RotateCredentials(manager.RotateInput{Name: name, DBRoot: dbRoot, Admin: admin}, pw)
			if err != nil {
				return err
			}
			if asJSON {
				doc := jsonCredentials{Schema: "ffm.credentials/v1", Bench: res.Name,
					DBRootRotated: res.DBRotated, AdminRotated: res.AdminRotated, Verified: res.Verified}
				if showSecrets {
					doc.DBPassword, doc.AdminPassword = res.DBPassword, res.AdminPassword
				}
				return writeJSON(doc)
			}
			hint := "ffm status " + name + " --show-secrets"
			fmt.Printf("Credentials of %q rotated.\n", name)
			if res.DBRotated {
				fmt.Printf("  DB root:  %s\n", manager.Secret(res.DBPassword, hint, showSecrets))
			}
			if res.AdminRotated {
				fmt.Printf("  Admin:    %s\n", manager.Secret(res.AdminPassword, hint, showSecrets))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dbRoot, "db-root", false, "Rotate the database root password (default: both)")
	cmd.Flags().BoolVar(&admin, "admin", false, "Rotate the Administrator password (default: both)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON (schema ffm.credentials/v1)")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false,
		"Print the new passwords even when stdout is not a terminal; with --json, include them in the document")
	return cmd
}
