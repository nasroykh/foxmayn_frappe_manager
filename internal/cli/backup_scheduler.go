package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/scheduler"
)

func init() {
	afterScheduleChange = syncSchedulerJob
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
		changed, err := scheduler.Uninstall()
		if err != nil {
			if !errors.Is(err, scheduler.ErrUnsupported) {
				fmt.Fprintf(os.Stderr, "warning: could not remove the hourly job: %v\n", err)
			}
			return nil
		}
		if changed {
			fmt.Println("No bench has scheduled backups any more; removed the hourly job from your crontab.")
		}
		return nil
	}
	job, err := scheduler.CurrentJob()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the schedule is saved, but the hourly job was not installed: %v\n", err)
		return nil
	}
	changed, err := scheduler.Install(job)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the schedule is saved, but the hourly job was not installed: %v\n", err)
		if errors.Is(err, scheduler.ErrUnsupported) && runtime.GOOS == "windows" {
			fmt.Fprintf(os.Stderr, "Create it with:\n  %s\n", scheduler.WindowsCommand(job))
		}
		return nil
	}
	if changed {
		fmt.Printf("Installed the hourly job in your crontab (runs at minute %d; log: %s).\n", job.Minute, job.LogFile)
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

On Linux and macOS the job is a line in your crontab tagged ` + scheduler.Tag + `.
It carries the absolute path of this ffm binary, a PATH that reaches docker, and
any FFM_* or DOCKER_* settings present at install time — cron runs with an
almost empty environment. Re-run 'install' after moving ffm or docker, or after
changing those settings. On Windows, 'print' gives the Task Scheduler command.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install or update the hourly job",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				job, err := scheduler.CurrentJob()
				if err != nil {
					return err
				}
				if runtime.GOOS == "windows" {
					return fmt.Errorf("%w; create it with:\n  %s", scheduler.ErrUnsupported, scheduler.WindowsCommand(job))
				}
				changed, err := scheduler.Install(job)
				if err != nil {
					return err
				}
				if changed {
					fmt.Printf("Installed: %s\n", job.Line())
				} else {
					fmt.Println("The hourly job is already installed and up to date.")
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "uninstall",
			Short: "Remove the hourly job (schedules are kept but stop running)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				changed, err := scheduler.Uninstall()
				if err != nil {
					return err
				}
				if changed {
					fmt.Println("Removed the hourly job from your crontab.")
				} else {
					fmt.Println("The hourly job was not installed.")
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "print",
			Short: "Print the job for installing it by hand",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				job, err := scheduler.CurrentJob()
				if err != nil {
					return err
				}
				if runtime.GOOS == "windows" {
					fmt.Println(scheduler.WindowsCommand(job))
					return nil
				}
				fmt.Println(job.Line())
				return nil
			},
		},
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
	crontab, err := scheduler.ReadCrontab()
	if err != nil {
		fmt.Printf("Hourly job: unknown (%v)\n", err)
		return nil
	}
	line := scheduler.Find(crontab)
	switch {
	case line == "" && any:
		fmt.Println("Hourly job: NOT installed, but benches have schedules — nothing is backing them up.")
		fmt.Println("Fix: ffm backup scheduler install")
	case line == "":
		fmt.Println("Hourly job: not installed (no bench has a schedule).")
	default:
		fmt.Printf("Hourly job: installed\n  %s\n", line)
		if job, err := scheduler.CurrentJob(); err == nil && job.Line() != line {
			fmt.Println("It differs from what this ffm would install now (binary, PATH or FFM_* settings changed).")
			fmt.Println("Update it with: ffm backup scheduler install")
		}
		if !any {
			fmt.Println("No bench has a schedule; the job runs but does nothing. Remove it with 'ffm backup scheduler uninstall'.")
		}
		if exe := installedBinary(line); exe != "" {
			if _, err := os.Stat(exe); err != nil {
				fmt.Printf("WARNING: the job's ffm binary %s no longer exists — every run fails.\n", exe)
			}
		}
	}
	return nil
}

// installedBinary extracts the quoted ffm path from an installed line.
func installedBinary(line string) string {
	i := strings.Index(line, "' backup run-due")
	if i < 0 {
		return ""
	}
	j := strings.LastIndex(line[:i], " '")
	if j < 0 {
		return ""
	}
	return line[j+2 : i]
}
