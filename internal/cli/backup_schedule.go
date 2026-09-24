package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newBackupScheduleCmd() *cobra.Command {
	var (
		every      string
		keep       int
		keepHourly int
		keepDaily  int
		keepWeekly int
		files      string
		off        bool
		noInstall  bool
	)
	cmd := &cobra.Command{
		Use:   "schedule [bench]",
		Short: "Back a bench up automatically, keeping a bounded set of archives",
		Long: `Set, change or remove a bench's scheduled backups. Without a bench, show every
bench's schedule and when it last succeeded.

Retention is tiered: of the scheduled archives, ffm keeps the newest one in each
of the last N hours, N days and N weeks, plus the newest ` + fmt.Sprint(manager.RetentionFloor) + ` whatever their
age. Manual 'ffm backup' archives are never deleted.

With only --every, a preset gives three to four weeks of history:

  --every 1h     24 hourly, 7 daily, 5 weekly  (32-34 archives)  files daily
  --every 6h      4 hourly, 7 daily, 5 weekly  (12-14 archives)  files daily
  --every 24h               7 daily, 5 weekly  (10-11 archives)  files every run
  --every 168h                       4 weekly  (4 archives)      files every run

--keep N keeps N archives of the tier matching the interval instead
(e.g. --every weekly --keep 3 keeps three weeks).

A stopped bench is skipped, not started. Backups run from a single hourly
system job ('ffm backup run-due'); 'ffm backup scheduler' manages it.`,
		Example: `  ffm backup schedule mybench --every 24h
  ffm backup schedule mybench --every 1h --files weekly
  ffm backup schedule mybench --every weekly --keep 3
  ffm backup schedule mybench --off
  ffm backup schedule`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := manager.New(verbose)
			if len(args) == 0 {
				return printSchedules(svc, os.Stdout)
			}
			name := args[0]
			if off {
				if err := svc.SetBackupSchedule(name, nil); err != nil {
					return err
				}
				fmt.Printf("Scheduled backups for %q are off. Its existing archives are kept.\n", name)
				return afterScheduleChange(svc, noInstall)
			}

			b, err := svc.GetBench(name)
			if err != nil {
				return err
			}
			var p state.BackupPolicy
			switch {
			case cmd.Flags().Changed("every"):
				hours, err := manager.ParseEvery(every)
				if err != nil {
					return err
				}
				p = manager.PresetPolicy(hours)
			case b.BackupSchedule != nil:
				p = *b.BackupSchedule // adjust the existing policy
			default:
				p = manager.PresetPolicy(24)
			}
			p.Enabled = true
			if cmd.Flags().Changed("keep") {
				manager.ApplyKeepShorthand(&p, keep)
			}
			if cmd.Flags().Changed("keep-hourly") {
				p.KeepHourly = keepHourly
			}
			if cmd.Flags().Changed("keep-daily") {
				p.KeepDaily = keepDaily
			}
			if cmd.Flags().Changed("keep-weekly") {
				p.KeepWeekly = keepWeekly
			}
			if cmd.Flags().Changed("files") {
				p.Files = files
			}
			if err := svc.SetBackupSchedule(name, &p); err != nil {
				return err
			}
			fmt.Printf("Scheduled backups for %q: %s.\n", name, describePolicy(p))
			return afterScheduleChange(svc, noInstall)
		},
	}
	cmd.Flags().StringVar(&every, "every", "24h", "Interval: 1h, 6h, 24h, 168h (whole hours), or hourly/daily/weekly")
	cmd.Flags().IntVar(&keep, "keep", 0, "Keep N archives of the tier matching --every")
	cmd.Flags().IntVar(&keepHourly, "keep-hourly", 0, "Keep the newest archive of each of the last N hours")
	cmd.Flags().IntVar(&keepDaily, "keep-daily", 0, "Keep the newest archive of each of the last N days")
	cmd.Flags().IntVar(&keepWeekly, "keep-weekly", 0, "Keep the newest archive of each of the last N weeks")
	cmd.Flags().StringVar(&files, "files", "", "Include attachments: every-run, daily, weekly or never")
	cmd.Flags().BoolVar(&off, "off", false, "Stop scheduled backups for the bench (archives are kept)")
	cmd.Flags().BoolVar(&noInstall, "no-install", false, "Save the schedule without installing or removing the hourly system job")
	return cmd
}

func describePolicy(p state.BackupPolicy) string {
	var keep []string
	if p.KeepHourly > 0 {
		keep = append(keep, fmt.Sprintf("%d hourly", p.KeepHourly))
	}
	if p.KeepDaily > 0 {
		keep = append(keep, fmt.Sprintf("%d daily", p.KeepDaily))
	}
	if p.KeepWeekly > 0 {
		keep = append(keep, fmt.Sprintf("%d weekly", p.KeepWeekly))
	}
	return fmt.Sprintf("every %s, keeping %s (never fewer than %d), attachments %s",
		everyLabel(p.EveryHours), strings.Join(keep, " + "), manager.RetentionFloor, p.Files)
}

func everyLabel(h int) string {
	switch h {
	case 1:
		return "hour"
	case 24:
		return "day"
	case 168:
		return "week"
	}
	return fmt.Sprintf("%dh", h)
}

func printSchedules(svc *manager.Service, w io.Writer) error {
	statuses, err := svc.ScheduleStatuses()
	if err != nil {
		return err
	}
	if len(statuses) == 0 {
		fmt.Fprintln(w, "No bench has scheduled backups. Set one with 'ffm backup schedule <bench> --every 24h'.")
		return nil
	}
	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BENCH\tEVERY\tKEEP (H/D/W)\tFILES\tARCHIVES\tLAST SUCCESS\tLAST ATTEMPT\tNEXT")
	for _, st := range statuses {
		p := st.Policy
		last := "never"
		if !st.LastSuccess.IsZero() {
			last = relativeAge(st.LastSuccess, now)
		}
		attempt := "-"
		if !st.Run.LastAttempt.IsZero() {
			attempt = st.Run.Result + ", " + relativeAge(st.Run.LastAttempt, now)
		}
		next := "next tick"
		if !p.Enabled {
			next = "disabled"
		} else if !st.NextDue.IsZero() && st.NextDue.After(now) {
			next = st.NextDue.Local().Format("Jan 2 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%d/%d/%d\t%s\t%d\t%s\t%s\t%s\n", st.Bench, everyLabel(p.EveryHours),
			p.KeepHourly, p.KeepDaily, p.KeepWeekly, p.Files, st.Scheduled, last, attempt, next)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, st := range statuses {
		if st.Run.Result == manager.RunFailed && st.Run.Error != "" {
			fmt.Fprintf(w, "\n%s last failed: %s\n", st.Bench, firstLine(st.Run.Error))
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// afterScheduleChange keeps the hourly system job in step with the policies.
// Installing it is added with the scheduler package.
var afterScheduleChange = func(svc *manager.Service, noInstall bool) error {
	return nil
}

func newBackupRunDueCmd() *cobra.Command {
	var (
		dryRun  bool
		logPath string
	)
	cmd := &cobra.Command{
		Use:   "run-due",
		Short: "Back up every bench whose schedule is due (run by the hourly system job)",
		Long: `Back up each bench whose scheduled backup is due, one at a time, then prune its
old scheduled archives. This is what the hourly system job installed by
'ffm backup scheduler install' runs; running it by hand is safe.

It never prompts, never starts a stopped bench, and never prunes after a failed
backup. A second run-due started while one is running exits at once. The exit
status is non-zero when any bench failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := io.Writer(os.Stdout)
			if logPath != "" {
				f, err := openRotatingLog(logPath, 1<<20)
				if err != nil {
					return err
				}
				defer f.Close()
				out = f
			}
			results, err := manager.New(verbose).RunDue(dryRun, out)
			stamp := time.Now().UTC().Format(time.RFC3339)
			if errors.Is(err, manager.ErrRunInProgress) {
				fmt.Fprintf(out, "%s run-due: %v — exiting\n", stamp, err)
				return nil
			}
			if err != nil {
				fmt.Fprintf(out, "%s run-due: %v\n", stamp, err)
				return err
			}
			failed := 0
			for _, r := range results {
				line := fmt.Sprintf("%s %s %s", stamp, r.Bench, r.Result)
				switch r.Result {
				case manager.RunOK:
					content := "db"
					if r.WithFiles {
						content = "db+files"
					}
					line += fmt.Sprintf(" %s %s, pruned %d", content, r.Archive, r.Pruned)
				case "would-run":
					line += fmt.Sprintf(" (files: %v)", r.WithFiles)
				case manager.RunFailed, manager.RunSkippedBusy:
					line += ": " + firstLine(r.Err.Error())
				}
				if r.Result == manager.RunFailed {
					failed++
				}
				if r.Result != "not-due" || verbose || dryRun {
					fmt.Fprintln(out, line)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d scheduled backup(s) failed — see %s", failed, logOrStdout(logPath))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report which benches are due without backing anything up")
	cmd.Flags().StringVar(&logPath, "log", "", "Append results to this file (rotated at 1 MiB) instead of stdout")
	return cmd
}

func logOrStdout(p string) string {
	if p == "" {
		return "the output above"
	}
	return p
}

// openRotatingLog opens path for appending, first moving it to path.1 when it
// has grown past max bytes. One old generation is kept.
func openRotatingLog(path string, max int64) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		_ = os.Rename(path, path+".1")
	}
	if err := config.EnsureDataDir(); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
