package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/scheduler"
)

func init() {
	afterScheduleChange = syncSchedulerJob
}

func printWarnings(ws []string) {
	for _, w := range ws {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}

// syncSchedulerJob installs the hourly job when any bench has a schedule and
// removes it when none has. A failure here does not undo the saved policy: it
// is reported with the manual alternative.
func syncSchedulerJob(svc *manager.Service, noInstall bool) error {
	if noInstall {
		fmt.Println("The hourly job was not touched (--no-install). See 'ffm backup scheduler status'.")
		return nil
	}
	any, err := svc.AnyScheduleEnabled()
	if err != nil {
		return err
	}
	if !any {
		res, err := scheduler.Uninstall(config.BackupSchedulerLogFile(), false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove the hourly job: %v\n", err)
			return nil
		}
		if res.Changed {
			fmt.Printf("No bench has scheduled backups any more; removed the hourly job (%s).\n", res.Where)
		}
		printWarnings(res.Warnings)
		return nil
	}
	job, err := scheduler.CurrentJob()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the schedule is saved, but the hourly job was not installed: %v\n", err)
		return nil
	}
	res, err := scheduler.Install(job, "", false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the schedule is saved, but the hourly job was not installed: %v\n", err)
		fmt.Fprintln(os.Stderr, "Install it by hand: see 'ffm backup scheduler print' (--backend cron|systemd|launchd|schtasks).")
		return nil
	}
	if res.Changed {
		fmt.Printf("Installed the hourly job with %s (%s; runs at minute %d; log: %s).\n", res.Backend, res.Where, job.Minute, job.LogFile)
	}
	printWarnings(res.Warnings)
	return nil
}

func checkBackendFlag(name string) error {
	if name == "" {
		return nil
	}
	if _, err := scheduler.ByName(name); err != nil {
		return usageError{err}
	}
	return nil
}

func newBackupSchedulerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scheduler",
		Short: "Manage the hourly system job that runs scheduled backups",
		Long: `Scheduled backups are run by one hourly system job, 'ffm backup run-due', for
all benches. 'ffm backup schedule' installs it with the first schedule and
removes it with the last; these commands manage it directly.

The job lives in one of four schedulers (--backend):
  cron      a line in your crontab tagged ` + scheduler.Tag + `
  systemd   a user timer and service in ~/.config/systemd/user
  launchd   a launch agent in ~/Library/LaunchAgents
  schtasks  a Windows Task Scheduler task running a .cmd file in the config dir

Without --backend, ffm uses the one it recorded in backup-scheduler.json, else
cron when your crontab already holds an ffm line, else the platform's default:
Task Scheduler on Windows, launchd on macOS; on Linux cron when crontab exists
and a cron daemon runs, otherwise a systemd user timer. Installing with another
backend moves the job: the old one is removed afterwards. With FFM_CONFIG_DIR
set, the job gets a name carrying a hash of that directory, so configurations
do not replace each other (cron has a single line per user and keeps refusing
to replace another configuration's).

The job carries the absolute path of this ffm binary, a PATH that reaches
docker, and any FFM_* or DOCKER_* settings present at install time: schedulers
run with an almost empty environment. Re-run 'install' after moving ffm or
docker, or after changing those settings. A systemd user timer stops when you
log out unless linger is on; ffm warns and prints the command, but never
enables it.`,
	}
	var backend string
	var force bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install or update the hourly job",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkBackendFlag(backend); err != nil {
				return err
			}
			job, err := scheduler.CurrentJob()
			if err != nil {
				return err
			}
			res, err := scheduler.Install(job, backend, force)
			if err != nil {
				return err
			}
			if res.Changed {
				fmt.Printf("Installed the hourly job with %s (%s; runs at minute %d; log: %s).\n", res.Backend, res.Where, job.Minute, job.LogFile)
			} else {
				fmt.Printf("The hourly job is already installed and up to date (%s).\n", res.Backend)
			}
			for _, old := range res.Removed {
				fmt.Printf("Removed the old %s job.\n", old)
			}
			printWarnings(res.Warnings)
			return nil
		},
	}
	install.Flags().StringVar(&backend, "backend", "", "Scheduler to use: "+strings.Join(scheduler.Backends, ", ")+" (default: the recorded one, else the platform's)")
	install.Flags().BoolVar(&force, "force", false, "Take over a job installed for another ffm configuration")
	var forceUninstall bool
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the hourly job (schedules are kept but stop running)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := scheduler.Uninstall(config.BackupSchedulerLogFile(), forceUninstall)
			if err != nil {
				return err
			}
			if res.Changed {
				fmt.Printf("Removed the hourly job (%s).\n", res.Where)
			} else {
				fmt.Println("The hourly job was not installed.")
			}
			printWarnings(res.Warnings)
			return nil
		},
	}
	uninstall.Flags().BoolVar(&forceUninstall, "force", false, "Remove the job even when another ffm configuration installed it")
	var printBackend string
	printCmd := &cobra.Command{
		Use:   "print",
		Short: "Print the job for installing it by hand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkBackendFlag(printBackend); err != nil {
				return err
			}
			b, _, err := scheduler.Resolve(printBackend)
			if err != nil {
				return err
			}
			job, err := scheduler.CurrentJob()
			if err != nil {
				return err
			}
			out := b.Print(job)
			fmt.Print(out)
			if !strings.HasSuffix(out, "\n") {
				fmt.Println()
			}
			return nil
		},
	}
	printCmd.Flags().StringVar(&printBackend, "backend", "", "Scheduler to print for: "+strings.Join(scheduler.Backends, ", ")+" (default: the recorded one, else the platform's)")
	cmd.AddCommand(
		install,
		uninstall,
		printCmd,
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the hourly job is installed and still valid",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return schedulerStatus()
			},
		},
	)
	return cmd
}

func schedulerStatus() error {
	svc := manager.New(verbose)
	any, err := svc.AnyScheduleEnabled()
	if err != nil {
		return err
	}
	var want *scheduler.Job
	if job, err := scheduler.CurrentJob(); err == nil {
		want = &job
	}
	st, err := scheduler.CheckStatus(want)
	if err != nil {
		if errors.Is(err, scheduler.ErrNoBackend) {
			fmt.Printf("Hourly job: not installed — no scheduler is usable here.\n  %v\n", err)
			return nil
		}
		fmt.Printf("Hourly job: unknown (%v)\n", err)
		return nil
	}
	switch {
	case !st.Installed && any:
		fmt.Printf("Hourly job: NOT installed (%s), but benches have schedules — nothing is backing them up.\n", st.Backend)
		fmt.Println("Fix: ffm backup scheduler install")
	case !st.Installed:
		fmt.Printf("Hourly job: not installed (%s; no bench has a schedule).\n", st.Backend)
	default:
		fmt.Printf("Hourly job: installed with %s (%s)\n  %s\n", st.Backend, st.Source, st.Where)
		fmt.Printf("  %s\n", strings.ReplaceAll(strings.TrimRight(st.Content, "\r\n"), "\n", "\n  "))
		if st.Drift {
			fmt.Println("It differs from what this ffm would install now (binary, PATH or FFM_* settings changed).")
			fmt.Println("Update it with: ffm backup scheduler install")
		}
		if !any {
			fmt.Println("No bench has a schedule; the job runs but does nothing. Remove it with 'ffm backup scheduler uninstall'.")
		}
		if st.BinaryMissing {
			fmt.Printf("WARNING: the job's ffm binary %s no longer exists — every run fails.\n", st.Binary)
		}
	}
	for _, w := range st.Warnings {
		fmt.Printf("WARNING: %s\n", w)
	}
	return nil
}
