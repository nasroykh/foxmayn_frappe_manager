package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/backuptarget"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type jsonTarget struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type jsonTargets struct {
	Schema  string       `json:"schema"`
	Targets []jsonTarget `json:"targets"`
}

func newBackupTargetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Manage off-host backup targets: S3, SFTP, a mounted directory or rclone",
		Long: `Targets are where 'ffm backup --to' and 'ffm backup schedule --to' upload
archives. Uploads are always encrypted with age ('ffm backup key init' first):
a target only ever holds <archive>.ffm.tar.age and its cleartext header.

Targets and their credentials are kept in ~/.config/ffm/backup-targets.json
(0600). Secrets are never taken as flag values: an S3 secret key comes from
stdin (--secret-access-key-stdin) or a file (--secret-access-key-file). SFTP
uses a key file and a pinned host key.

Bucket Object Lock (S3) or an append-only account on the SFTP server keeps a
compromised host from deleting the off-host copies.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return printTargets(false) },
	}

	var t backuptarget.Target
	var secretStdin, acceptHostKey, replace, noTest bool
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a target; it is tested (write, list, read, delete) before it is saved",
		Example: `  echo "$SECRET" | ffm backup target add r2 --type s3 --endpoint <account>.r2.cloudflarestorage.com \
      --bucket backups --access-key-id <id> --secret-access-key-stdin
  ffm backup target add box --type sftp --host backup.example.com --user ffm \
      --key-file ~/.ssh/ffm_backup --path /backups --accept-host-key
  ffm backup target add nas --type local --path /mnt/nas/ffm
  ffm backup target add drive --type rclone --remote gdrive:ffm-backups`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t.Name = args[0]
			if secretStdin {
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("read the secret access key from stdin: %w", err)
				}
				t.SecretAccessKey = strings.TrimSpace(line)
			}
			if t.Type == backuptarget.TypeSFTP && t.HostKey == "" {
				if !acceptHostKey {
					return usageError{errors.New("an SFTP target needs a pinned host key: --host-key '<type> <base64>' " +
						"(from ssh-keyscan), or --accept-host-key to fetch and pin it now")}
				}
				port := t.Port
				if port == 0 {
					port = 22
				}
				line, fp, err := backuptarget.FetchHostKey(t.Host, port)
				if err != nil {
					return err
				}
				fmt.Printf("Pinning %s's host key %s\n", t.Host, fp)
				t.HostKey = line
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := manager.AddTarget(ctx, t, replace, noTest); err != nil {
				return err
			}
			fmt.Printf("Saved backup target %q: %s.\n", t.Name, t.Describe())
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&t.Type, "type", "", "s3, sftp, local or rclone")
	f.StringVar(&t.Prefix, "prefix", "", "Folder inside the target for ffm's archives")
	f.StringVar(&t.Endpoint, "endpoint", "", "s3: endpoint host, e.g. s3.eu-west-1.amazonaws.com")
	f.StringVar(&t.Bucket, "bucket", "", "s3: bucket (must exist)")
	f.StringVar(&t.Region, "region", "", "s3: region, when the provider needs one")
	f.StringVar(&t.AccessKeyID, "access-key-id", "", "s3: access key id")
	f.BoolVar(&secretStdin, "secret-access-key-stdin", false, "s3: read the secret access key from stdin")
	f.StringVar(&t.SecretAccessKeyFile, "secret-access-key-file", "", "s3: read the secret access key from this file at each use")
	f.BoolVar(&t.Insecure, "insecure", false, "s3: plain HTTP (a MinIO on a private network)")
	f.StringVar(&t.Host, "host", "", "sftp: server")
	f.IntVar(&t.Port, "port", 0, "sftp: port (default 22)")
	f.StringVar(&t.User, "user", "", "sftp: user")
	f.StringVar(&t.KeyFile, "key-file", "", "sftp: private key file (no passphrase; use a dedicated key)")
	f.StringVar(&t.HostKey, "host-key", "", "sftp: the server's host key, as one line of ssh-keyscan output without the host")
	f.BoolVar(&acceptHostKey, "accept-host-key", false, "sftp: fetch the server's host key now and pin it")
	f.StringVar(&t.Path, "path", "", "sftp: directory on the server; local: absolute directory")
	f.StringVar(&t.Remote, "remote", "", "rclone: remote and path, e.g. gdrive:ffm (configured with rclone config)")
	f.BoolVar(&replace, "replace", false, "Replace a target with the same name")
	f.BoolVar(&noTest, "no-test", false, "Save without testing the target")

	var asJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List backup targets (no secrets)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return printTargets(asJSON) },
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.targets/v1)")

	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Forget a target (archives stored on it are left alone)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := manager.RemoveTarget(args[0], manager.New(verbose)); err != nil {
				return err
			}
			fmt.Printf("Removed backup target %q; archives stored on it are untouched.\n", args[0])
			return nil
		},
	}
	test := &cobra.Command{
		Use:   "test <name>",
		Short: "Write, list, read back and delete a small object on a target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := manager.TestTarget(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("Target %q works.\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(add, list, remove, test)
	return cmd
}

func printTargets(asJSON bool) error {
	ts, err := manager.LoadTargets()
	if err != nil {
		return err
	}
	if asJSON {
		out := jsonTargets{Schema: "ffm.targets/v1", Targets: []jsonTarget{}}
		for _, t := range ts {
			out.Targets = append(out.Targets, jsonTarget{Name: t.Name, Type: t.Type, Description: t.Describe()})
		}
		return writeJSON(out)
	}
	if len(ts) == 0 {
		fmt.Println("No backup targets. Add one with: ffm backup target add <name> --type …")
		return nil
	}
	for _, t := range ts {
		fmt.Printf("  %-16s %s\n", t.Name, t.Describe())
	}
	return nil
}

func newBackupPullCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "pull <target> <bench>",
		Short: "Download a bench's archive from a target (the newest unless --archive)",
		Long: `Download an encrypted archive and its header from a target into the bench's
local backups directory, for 'ffm restore <archive> <name> --identity <file>'.
The bench does not need to exist on this host.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
			defer cancel()
			local, err := manager.PullArchive(ctx, args[0], args[1], file, manager.CLIProgress{})
			if err != nil {
				return err
			}
			fmt.Printf("Downloaded %s\nRestore it with:\n  ffm restore %s <newname> --identity <file>\n", local, local)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "archive", "", "File name of the archive (default: the newest)")
	return cmd
}
