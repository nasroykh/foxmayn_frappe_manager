package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/notify"
)

type jsonVerify struct {
	Schema      string `json:"schema"`
	Archive     string `json:"archive"`
	Bench       string `json:"bench"`
	TakenAt     string `json:"taken_at"`
	Members     int    `json:"members"`
	Bytes       int64  `json:"bytes"`
	Encrypted   bool   `json:"encrypted"`
	DumpChecked bool   `json:"dump_checked"`
	Restored    string `json:"restored,omitempty"`
	Seconds     int    `json:"seconds"`
}

func newBackupVerifyCmd() *cobra.Command {
	var in manager.VerifyInput
	var asJSON, notifyResult bool
	cmd := &cobra.Command{
		Use:   "verify [archive]",
		Short: "Prove an archive restores: checksums, dump, and with --restore a real restore",
		Long: `Check that an archive can be restored, without touching any bench:
- decrypt it when it is encrypted (age authenticates every byte);
- unpack it with the guards a restore uses and check every member against the
  SHA-256 the manifest records;
- check that the database dump contains Frappe's __Auth table.

--restore also restores it into a throwaway bench (verify-<random>), asks the
site for /api/method/ping, and deletes the bench. It takes minutes, and the
ports and disk of one more bench while it runs.

--target with --bench verifies the newest archive of a bench on a backup
target, downloaded to a temporary directory.`,
		Example: `  ffm backup verify ~/frappe/_backups/mybench/mybench_20261008T120000Z.ffm.tar
  ffm backup verify mybench_….ffm.tar.age --identity ~/ffm-backup.key --restore
  ffm backup verify --target r2 --bench mybench --identity ~/ffm-backup.key`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Archive = args[0]
			}
			if (in.Archive == "") == (in.Target == "") {
				return usageError{errors.New("give an archive, or --target with --bench")}
			}
			res, err := manager.New(verbose).VerifyBackup(in, manager.CLIProgress{})
			if notifyResult {
				e := notify.Event{Kind: "verify", Bench: res.Header.BenchName, OK: err == nil}
				if err != nil {
					e.Message = fmt.Sprintf("Verifying %s failed: %v", res.Archive, err)
				} else {
					e.Message = fmt.Sprintf("Verified %s (%d members%s)", res.Archive, res.Members, map[bool]string{true: ", restored", false: ""}[res.Restored != ""])
				}
				if nerr := manager.Notify(e); nerr != nil {
					fmt.Fprintf(os.Stderr, "warning: %v\n", nerr)
				}
			}
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonVerify{Schema: "ffm.verify/v1", Archive: res.Archive, Bench: res.Header.BenchName,
					TakenAt: jsonTime(res.Header.CreatedAt), Members: res.Members, Bytes: res.Bytes, Encrypted: res.Encrypted,
					DumpChecked: res.DumpChecked, Restored: res.Restored, Seconds: int(res.Took.Seconds())})
			}
			fmt.Printf("\nArchive OK: %s\n", res.Archive)
			fmt.Printf("  Bench:     %s, taken %s\n", res.Header.BenchName, res.Header.CreatedAt.Local().Format("2006-01-02 15:04"))
			fmt.Printf("  Members:   %d, %s, all matching the manifest\n", res.Members, humanSize(res.Bytes))
			if res.DumpChecked {
				fmt.Println("  Database:  dump contains Frappe's __Auth table")
			} else {
				fmt.Println("  Database:  encrypted by Frappe; not read")
			}
			if res.Restored != "" {
				fmt.Printf("  Restore:   restored into %s, site answered /api/method/ping, bench deleted\n", res.Restored)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&in.Identity, "identity", "", "age identity file for an encrypted archive. Env: FFM_AGE_IDENTITY_FILE")
	cmd.Flags().BoolVar(&in.Restore, "restore", false, "Also restore into a throwaway bench, ping its site, and delete it")
	cmd.Flags().StringVar(&in.Target, "target", "", "Verify the newest archive on this backup target (needs --bench)")
	cmd.Flags().StringVar(&in.Bench, "bench", "", "Bench whose archive --target verifies")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON (schema ffm.verify/v1)")
	cmd.Flags().BoolVar(&notifyResult, "notify", false, "Report the result through 'ffm notify' notifiers (for a cron job)")
	return cmd
}
