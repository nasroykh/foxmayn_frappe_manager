package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newBackupKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage the age keys backups are encrypted to (init / add / list)",
		Long: `Encrypted backups ('ffm backup --encrypt', 'ffm backup schedule --encrypt',
and every upload to a remote target) are encrypted with age to the public keys
listed in ~/.config/ffm/backup-recipients.txt. ffm never keeps the private key
(the identity): 'ffm backup key init' shows it once. Store it away from this
host — without it, no encrypted backup can be restored.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return printRecipients() },
	}
	var out string
	var add bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a key pair: keep the identity, ffm keeps the public key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			identity, recipient, err := manager.InitBackupKey(out, add)
			if err != nil {
				return err
			}
			fmt.Printf("Backups will be encrypted to %s\n(added to %s).\n\n", recipient, config.BackupRecipientsFile())
			if out != "" {
				fmt.Printf("The identity was written to %s (0600). Move it off this host, to a password\n"+
					"manager or offline storage: whoever has it can read every encrypted backup, and\n"+
					"without it none can be restored.\n", out)
				return nil
			}
			fmt.Println("Identity (shown once — store it in a password manager or offline, not on this host):")
			fmt.Println()
			fmt.Println(identity)
			fmt.Println()
			fmt.Println("To restore, save it to a file and pass: ffm restore <archive>.age <name> --identity <file>")
			return nil
		},
	}
	initCmd.Flags().StringVar(&out, "out", "", "Write the identity to this new file (0600) instead of printing it")
	initCmd.Flags().BoolVar(&add, "add", false, "Add a key even though backups already have one")
	addCmd := &cobra.Command{
		Use:   "add <age-recipient>",
		Short: "Also encrypt to another public key (age1…), e.g. an offline one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := manager.AddBackupRecipient(args[0]); err != nil {
				return err
			}
			fmt.Printf("Added %s.\n", args[0])
			return nil
		},
	}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Show the public keys backups are encrypted to",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return printRecipients() },
	}
	cmd.AddCommand(initCmd, addCmd, listCmd)
	return cmd
}

func printRecipients() error {
	lines, err := manager.RecipientLines()
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		fmt.Println("No backup key yet. Create one with: ffm backup key init")
		return nil
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}
