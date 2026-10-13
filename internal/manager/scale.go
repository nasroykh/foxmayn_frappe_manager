package manager

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Bounds for ffm scale.
//
// The minimum is 1 because the bench record stores 0 as "not recorded, use the
// create-time default", so a zero replica count could not be persisted (and a
// bench without a long-queue worker never runs its long jobs). The maximum is a
// sanity cap against a typo: each gunicorn worker or RQ worker is a full Python
// process of a few hundred MiB, and 32 already exceeds what one host runs.
const (
	ScaleMin = 1
	ScaleMax = 32
)

// ErrProdOnly is returned by operations that need the separate per-process
// containers of a production bench.
var ErrProdOnly = errors.New("this applies to production benches only")

// ErrScaleRange is returned (wrapped) for a count outside ScaleMin..ScaleMax.
var ErrScaleRange = errors.New("count out of range")

// ScaleSettings are the process counts of a production bench.
type ScaleSettings struct {
	// Gunicorn is the number of gunicorn worker processes in the frappe
	// container (gunicorn -w).
	Gunicorn int `json:"gunicorn_workers"`
	// WorkerLong and WorkerShort are the replica counts of the worker-long
	// and worker-short containers.
	WorkerLong  int `json:"worker_long_replicas"`
	WorkerShort int `json:"worker_short_replicas"`
}

// ScaleInput changes some of a production bench's process counts. A nil field
// is left as it is.
type ScaleInput struct {
	Name        string
	Gunicorn    *int
	WorkerLong  *int
	WorkerShort *int
	// DryRun shows the change and touches nothing.
	DryRun bool
}

// ScaleView is what `ffm scale` reports for a bench.
type ScaleView struct {
	Bench    string
	Settings ScaleSettings
	// Running counts the running containers of worker-long and worker-short;
	// nil when the bench is not running.
	Running map[string]int
}

// scaleWebWait and scaleSettleWait bound the waits after a change; tests
// shorten them.
var (
	scaleWebWait    = 90 * time.Second
	scaleSettleWait = 30 * time.Second
)

func requireProd(b state.Bench) error {
	if !b.IsProd() {
		return fmt.Errorf("%w: bench %q is a dev bench, which runs every process in one container under honcho", ErrProdOnly, b.Name)
	}
	return nil
}

// settingsOf is the effective counts of a bench: the record, with the create
// defaults for what was never recorded.
func (s *Service) settingsOf(b state.Bench) (ScaleSettings, error) {
	data, err := s.composeDataFor(b)
	if err != nil {
		return ScaleSettings{}, err
	}
	return ScaleSettings{Gunicorn: data.GunicornWorkers, WorkerLong: data.WorkerLongCount, WorkerShort: data.WorkerShortCount}, nil
}

// ScaleStatus reports a production bench's process counts and, when it runs,
// how many worker containers are actually up.
func (s *Service) ScaleStatus(name string) (ScaleView, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return ScaleView{}, err
	}
	if err := requireProd(b); err != nil {
		return ScaleView{}, err
	}
	set, err := s.settingsOf(b)
	if err != nil {
		return ScaleView{}, err
	}
	v := ScaleView{Bench: b.Name, Settings: set}
	if s.LiveStatus(b) == StatusRunning {
		v.Running = runningReplicas(s.quietRunnerFor(b))
	}
	return v, nil
}

// runningReplicas counts the running containers per service from
// `docker compose ps`.
func runningReplicas(r *bench.Runner) map[string]int {
	out, err := r.PS("{{.Service}} {{.State}}")
	if err != nil {
		return nil
	}
	counts := map[string]int{"worker-long": 0, "worker-short": 0}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[1] == "running" {
			if _, ok := counts[f[0]]; ok {
				counts[f[0]]++
			}
		}
	}
	return counts
}

func validateScale(name string, v *int) error {
	if v != nil && (*v < ScaleMin || *v > ScaleMax) {
		return fmt.Errorf("%w: %s must be between %d and %d (got %d)", ErrScaleRange, name, ScaleMin, ScaleMax, *v)
	}
	return nil
}

// Scale changes the gunicorn worker count and the worker replica counts of a
// production bench: it records them, re-renders docker-compose.yml and rolls
// the change onto the containers with `docker compose up -d`. Databases, the
// workspace and the other containers are untouched.
//
// Replica-only changes add or remove worker containers without restarting
// anything else. A gunicorn change replaces the frappe container, so the site
// is briefly unavailable and the containers depending on it restart too; Scale
// waits for the web server to answer again.
func (s *Service) Scale(in ScaleInput, pw ProgressWriter) (ScaleSettings, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	if in.Gunicorn == nil && in.WorkerLong == nil && in.WorkerShort == nil {
		return ScaleSettings{}, errors.New("nothing to change: give --gunicorn-workers, --worker-long-replicas or --worker-short-replicas")
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return ScaleSettings{}, err
	}
	defer release()

	b, err := s.GetBench(in.Name)
	if err != nil {
		return ScaleSettings{}, err
	}
	if err := requireProd(b); err != nil {
		return ScaleSettings{}, err
	}
	for _, c := range []struct {
		flag string
		v    *int
	}{{"--gunicorn-workers", in.Gunicorn}, {"--worker-long-replicas", in.WorkerLong}, {"--worker-short-replicas", in.WorkerShort}} {
		if err := validateScale(c.flag, c.v); err != nil {
			return ScaleSettings{}, err
		}
	}
	have, err := s.settingsOf(b)
	if err != nil {
		return ScaleSettings{}, err
	}
	want := have
	if in.Gunicorn != nil {
		want.Gunicorn = *in.Gunicorn
	}
	if in.WorkerLong != nil {
		want.WorkerLong = *in.WorkerLong
	}
	if in.WorkerShort != nil {
		want.WorkerShort = *in.WorkerShort
	}
	if want == have {
		pw.Printf("Bench %q already runs %s — nothing to do.\n", b.Name, describeScale(have))
		return have, nil
	}
	pw.Printf("Bench %q: %s\n  -> %s\n", b.Name, describeScale(have), describeScale(want))
	if in.DryRun {
		if want.Gunicorn != have.Gunicorn {
			pw.Println("  (dry run: changing gunicorn replaces the frappe container; the site is briefly unavailable)")
		}
		return want, nil
	}

	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.GunicornWorkers = want.Gunicorn
		rec.WorkerLongCount = want.WorkerLong
		rec.WorkerShortCount = want.WorkerShort
	}); err != nil {
		return have, fmt.Errorf("update state: %w", err)
	}
	b.GunicornWorkers, b.WorkerLongCount, b.WorkerShortCount = want.Gunicorn, want.WorkerLong, want.WorkerShort

	if err := s.applyScaleLocked(b, want, want.Gunicorn != have.Gunicorn, pw); err != nil {
		return want, fmt.Errorf("%w\nThe new counts are recorded; 'ffm reconcile %s' applies them again", err, b.Name)
	}
	pw.Printf("Bench %q now runs %s.\n", b.Name, describeScale(want))
	return want, nil
}

func describeScale(v ScaleSettings) string {
	return fmt.Sprintf("gunicorn workers %d, worker-long x%d, worker-short x%d", v.Gunicorn, v.WorkerLong, v.WorkerShort)
}

// applyScaleLocked writes the compose file and, when the bench runs, rolls it
// out and waits until the web server answers (after a gunicorn change) and the
// worker containers number what was asked for.
func (s *Service) applyScaleLocked(b state.Bench, want ScaleSettings, gunicornChanged bool, pw ProgressWriter) error {
	data, err := s.composeDataFor(b)
	if err != nil {
		return err
	}
	if err := proxy.EnsureNetwork(); err != nil {
		return fmt.Errorf("ensure proxy network: %w", err)
	}
	if err := bench.WriteCompose(b.Dir, data); err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	pw.Println("  ✓ docker-compose.yml updated")
	if st := s.LiveStatus(b); st != StatusRunning && st != StatusPartial {
		pw.Println("  Bench is not running — the new counts apply on the next 'ffm start'.")
		return nil
	}
	runner := s.runnerFor(b)
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	pw.Println("  ✓ containers updated")

	if gunicornChanged {
		pw.Println("  Waiting for the web server...")
		url := fmt.Sprintf("http://localhost:%d", b.WebPort)
		if err := bench.WaitForHTTP(url, scaleWebWait); err != nil {
			return webServerUnreachable(runner, false, err)
		}
	}
	deadline := time.Now().Add(scaleSettleWait)
	for {
		got := runningReplicas(s.quietRunnerFor(b))
		if got != nil && got["worker-long"] == want.WorkerLong && got["worker-short"] == want.WorkerShort {
			return nil
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(pw.Stderr(), "warning: expected %d worker-long and %d worker-short containers running, found %v (check 'docker compose ps')\n",
				want.WorkerLong, want.WorkerShort, got)
			return nil
		}
		time.Sleep(time.Second)
	}
}
