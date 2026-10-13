package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// jsonScale is the ffm.scale/v1 document.
type jsonScale struct {
	Schema string `json:"schema"`
	Bench  string `json:"bench"`
	manager.ScaleSettings
	// Running counts the running worker containers; absent when the bench is
	// not running.
	Running map[string]int `json:"running,omitempty"`
}

func newScaleCmd() *cobra.Command {
	var (
		gunicorn, workerLong, workerShort int
		dryRun, asJSON                    bool
	)
	cmd := &cobra.Command{
		Use:   "scale [bench]",
		Short: "Change a production bench's gunicorn workers and background-worker replicas",
		Long: `Change how many processes a production bench runs, without losing data.

  ffm scale mybench --worker-long-replicas 3
  ffm scale mybench --gunicorn-workers 5 --worker-short-replicas 2

The counts are saved on the bench record, docker-compose.yml is rendered again
and 'docker compose up -d' rolls it out; the database, the workspace and the
other containers are untouched. Changing replicas adds or removes worker
containers only. Changing --gunicorn-workers replaces the frappe container, so
the site is unavailable for a few seconds and ffm waits until it answers.

Each count is between 1 and 32. Every gunicorn and RQ worker is a Python
process of a few hundred MiB: gunicorn's rule of thumb is 2 x CPUs + 1.

Without a count flag it prints the current values. Dev benches run one container
under honcho and are refused. --dry-run shows the change and does nothing.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to scale")
			if err != nil {
				return err
			}
			svc := manager.New(verbose)
			f := cmd.Flags()
			in := manager.ScaleInput{Name: name, DryRun: dryRun}
			if f.Changed("gunicorn-workers") {
				in.Gunicorn = &gunicorn
			}
			if f.Changed("worker-long-replicas") {
				in.WorkerLong = &workerLong
			}
			if f.Changed("worker-short-replicas") {
				in.WorkerShort = &workerShort
			}
			if in.Gunicorn == nil && in.WorkerLong == nil && in.WorkerShort == nil {
				if dryRun {
					return usageError{errors.New("--dry-run needs a count to change")}
				}
				v, err := svc.ScaleStatus(name)
				if err != nil {
					return scaleErr(err)
				}
				return printScale(v, asJSON)
			}
			// --json keeps stdout a single document: progress goes to stderr.
			var pw manager.ProgressWriter = manager.CLIProgress{}
			if asJSON {
				pw = writerProgress{os.Stderr}
			}
			if _, err := svc.Scale(in, pw); err != nil {
				return scaleErr(err)
			}
			if asJSON && !dryRun {
				v, err := svc.ScaleStatus(name)
				if err != nil {
					return err
				}
				return printScale(v, true)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&gunicorn, "gunicorn-workers", 0, "Gunicorn worker processes in the frappe container (1-32)")
	f.IntVar(&workerLong, "worker-long-replicas", 0, "Replicas of the long-queue worker (1-32)")
	f.IntVar(&workerShort, "worker-short-replicas", 0, "Replicas of the short-queue worker (1-32)")
	f.BoolVar(&dryRun, "dry-run", false, "Show the change without applying it")
	f.BoolVar(&asJSON, "json", false, "Print the counts as JSON (schema ffm.scale/v1)")
	f.SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		switch name {
		case "gunicorn":
			name = "gunicorn-workers"
		case "workers-long", "worker-long":
			name = "worker-long-replicas"
		case "workers-short", "worker-short":
			name = "worker-short-replicas"
		}
		return pflag.NormalizedName(name)
	})
	return cmd
}

// scaleErr turns the dev-bench refusal into a usage error (exit 2).
func scaleErr(err error) error {
	if errors.Is(err, manager.ErrProdOnly) {
		return usageError{err}
	}
	return err
}

func printScale(v manager.ScaleView, asJSON bool) error {
	if asJSON {
		return writeJSON(jsonScale{Schema: "ffm.scale/v1", Bench: v.Bench, ScaleSettings: v.Settings, Running: v.Running})
	}
	running := func(svc string) string {
		if v.Running == nil {
			return ""
		}
		return fmt.Sprintf("  (%d running)", v.Running[svc])
	}
	fmt.Printf("Bench %s\n  gunicorn workers:      %d\n  worker-long replicas:  %d%s\n  worker-short replicas: %d%s\n",
		v.Bench, v.Settings.Gunicorn,
		v.Settings.WorkerLong, running("worker-long"),
		v.Settings.WorkerShort, running("worker-short"))
	return nil
}
