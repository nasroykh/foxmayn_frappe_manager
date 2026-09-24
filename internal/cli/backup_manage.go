package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newBackupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [bench]",
		Short: "List backup archives, newest first",
		Long: `List the archives in ffm's backup directory with when they were taken, whether
a person or the schedule made them, what they contain and their full path —
the path is what 'ffm restore' takes.

Only archives in the default backup directory are listed; archives written
elsewhere with --out are not tracked.`,
		Example: `  ffm backup list
  ffm backup list mybench`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var benches []string
			if len(args) == 1 {
				benches = args
			} else {
				entries, err := os.ReadDir(config.BackupsDir())
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				for _, e := range entries {
					if e.IsDir() {
						benches = append(benches, e.Name())
					}
				}
				sort.Strings(benches)
			}
			return printArchives(benches)
		},
	}
}

func printArchives(benches []string) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BENCH\tTAKEN\tTRIGGER\tCONTENT\tSIZE\tLABEL\tPATH")
	total := 0
	for _, name := range benches {
		archives, err := manager.ScanArchives(name)
		if err != nil {
			return err
		}
		for _, a := range archives {
			total++
			if a.Err != nil {
				fmt.Fprintf(tw, "%s\t?\tunreadable\t-\t%s\t%s\t%s\n",
					name, humanSize(a.Size), a.Err, a.Path)
				continue
			}
			trigger := a.Header.Trigger
			if trigger == "" {
				trigger = manager.TriggerManual
			}
			content := "db"
			if a.Header.HasTier(manager.TierFiles) {
				content = "db+files"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name,
				a.CreatedAt().Local().Format("2006-01-02 15:04"), trigger, content,
				humanSize(a.Size), a.Header.Label, a.Path)
		}
	}
	if total == 0 {
		fmt.Printf("No backup archives in %s.\n", config.BackupsDir())
		return nil
	}
	return tw.Flush()
}

func newBackupPruneCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "prune <bench>",
		Short: "Apply a bench's retention policy to its scheduled archives now",
		Long: `Delete the scheduled archives a bench's retention policy no longer keeps.
Scheduled runs already prune after every successful backup; this is for
applying a changed policy immediately, or previewing it with --dry-run.

Only archives written by scheduled runs are ever deleted. Manual 'ffm backup'
archives, archives from an ffm older than scheduling, and files ffm cannot
read are never touched. The newest ` + fmt.Sprint(manager.RetentionFloor) + ` scheduled archives are always kept.`,
		Example: `  ffm backup prune mybench --dry-run
  ffm backup prune mybench`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := manager.New(verbose).PruneBackups(args[0], dryRun)
			if err != nil {
				return err
			}
			verb := "Removed"
			if dryRun {
				verb = "Would remove"
			}
			for _, a := range res.Removed {
				fmt.Printf("  %s %s\n", strings.ToLower(verb), filepath.Base(a.Path))
			}
			for _, p := range res.Partials {
				fmt.Printf("  %s stale partial file %s\n", strings.ToLower(verb), filepath.Base(p))
			}
			fmt.Printf("%s %d scheduled archive(s); kept %d; %d other archive(s) not subject to pruning.\n",
				verb, len(res.Removed), len(res.Kept), res.Ignored)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without removing it")
	return cmd
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// relativeAge renders a duration since t like "3h ago".
func relativeAge(t, now time.Time) string {
	d := now.Sub(t).Round(time.Minute)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
