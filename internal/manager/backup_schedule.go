package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/lock"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// dueSlack lets a run count as due slightly early. The hourly tick fires at
// the same minute each hour, but a backup that took a few minutes pushes its
// archive's timestamp later than the tick, so without slack an hourly
// schedule would skip every second hour.
const dueSlack = 5 * time.Minute

// Run results recorded in RunState.Result.
const (
	RunOK             = "ok"
	RunFailed         = "failed"
	RunSkippedStopped = "skipped-stopped"
	RunSkippedBusy    = "skipped-busy"
)

// RunState is the last scheduled attempt for a bench.
type RunState struct {
	LastAttempt time.Time `json:"last_attempt"`
	Result      string    `json:"result"`
	Error       string    `json:"error,omitempty"`
}

// ReadRunState returns the bench's last recorded attempt, or a zero RunState.
func ReadRunState(benchName string) RunState {
	var rs RunState
	raw, err := os.ReadFile(config.BackupRunStateFile(benchName))
	if err == nil {
		_ = json.Unmarshal(raw, &rs)
	}
	return rs
}

func writeRunState(benchName string, rs RunState) error {
	if _, err := config.EnsureBenchBackupsDir(benchName); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(config.BackupRunStateFile(benchName), raw, 0o600)
}

// ScheduleStatus summarises one bench's schedule for display.
type ScheduleStatus struct {
	Bench       string
	Policy      state.BackupPolicy
	LastSuccess time.Time // newest scheduled archive; zero if none
	LastFiles   time.Time // newest scheduled archive with attachments
	Scheduled   int       // scheduled archives on disk
	Run         RunState
	NextDue     time.Time
}

// scheduleStatus derives a bench's status from the archives on disk.
func scheduleStatus(b state.Bench) (ScheduleStatus, error) {
	st := ScheduleStatus{Bench: b.Name, Policy: *b.BackupSchedule, Run: ReadRunState(b.Name)}
	archives, err := ScanArchives(b.Name)
	if err != nil {
		return st, err
	}
	for _, a := range archives {
		if !prunable(a, b.Name) {
			continue
		}
		st.Scheduled++
		if st.LastSuccess.IsZero() {
			st.LastSuccess = a.CreatedAt()
		}
		if st.LastFiles.IsZero() && a.Header.HasTier(TierFiles) {
			st.LastFiles = a.CreatedAt()
		}
	}
	if !st.LastSuccess.IsZero() {
		st.NextDue = st.LastSuccess.Add(time.Duration(st.Policy.EveryHours) * time.Hour)
	}
	return st, nil
}

// due reports whether a scheduled run should back the bench up now.
func (st ScheduleStatus) due(now time.Time) bool {
	return st.LastSuccess.IsZero() || !now.Before(st.NextDue.Add(-dueSlack))
}

// includeFiles decides whether this run carries attachments.
func (st ScheduleStatus) includeFiles(now time.Time) bool {
	var every time.Duration
	switch st.Policy.Files {
	case FilesNever:
		return false
	case FilesDaily:
		every = 24 * time.Hour
	case FilesWeekly:
		every = 168 * time.Hour
	default:
		return true
	}
	return st.LastFiles.IsZero() || now.Sub(st.LastFiles) >= every-dueSlack
}

// ScheduleStatuses returns the status of every bench that has a schedule.
func (s *Service) ScheduleStatuses() ([]ScheduleStatus, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return nil, err
	}
	var out []ScheduleStatus
	for _, b := range benches {
		if b.BackupSchedule == nil {
			continue
		}
		st, err := scheduleStatus(b)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// SetBackupSchedule saves (or, with nil, removes) a bench's policy.
func (s *Service) SetBackupSchedule(name string, p *state.BackupPolicy) error {
	if p != nil {
		if err := ValidatePolicy(*p); err != nil {
			return err
		}
	}
	if _, err := s.GetBench(name); err != nil {
		return err
	}
	return s.UpdateBench(name, func(b *state.Bench) { b.BackupSchedule = p })
}

// AnyScheduleEnabled reports whether any bench has an enabled schedule.
func (s *Service) AnyScheduleEnabled() (bool, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return false, err
	}
	for _, b := range benches {
		if b.BackupSchedule != nil && b.BackupSchedule.Enabled {
			return true, nil
		}
	}
	return false, nil
}

// RunDueResult is one bench's outcome in a run-due pass.
type RunDueResult struct {
	Bench     string
	Result    string // RunOK, RunFailed, RunSkipped*, or "not-due" / "would-run" (dry run)
	WithFiles bool
	Archive   string
	Pruned    int
	Err       error
}

// ErrRunInProgress is returned by RunDue when another run-due holds the lock.
var ErrRunInProgress = errors.New("another 'ffm backup run-due' is already running")

// RunDue backs up every bench whose schedule is due, one at a time, and
// prunes each after a successful backup. It never prompts, never starts a
// stopped bench and never prunes after a failure.
func (s *Service) RunDue(dryRun bool, log io.Writer) ([]RunDueResult, error) {
	runLock, err := lock.TryAcquire(config.BackupRunLockFile())
	if errors.Is(err, lock.ErrHeld) {
		return nil, ErrRunInProgress
	}
	if err != nil {
		return nil, err
	}
	defer runLock.Release()

	benches, err := s.LoadBenches()
	if err != nil {
		return nil, err
	}
	var results []RunDueResult
	for _, b := range benches {
		if b.BackupSchedule == nil || !b.BackupSchedule.Enabled {
			continue
		}
		results = append(results, s.runOne(b, dryRun, log))
	}
	return results, nil
}

func (s *Service) runOne(b state.Bench, dryRun bool, log io.Writer) RunDueResult {
	res := RunDueResult{Bench: b.Name}
	now := s.clock()
	st, err := scheduleStatus(b)
	if err != nil {
		res.Result, res.Err = RunFailed, err
		return res
	}
	if !st.due(now) {
		res.Result = "not-due"
		return res
	}
	res.WithFiles = st.includeFiles(now)
	if dryRun {
		res.Result = "would-run"
		return res
	}

	// Hold the bench across backup AND prune, so nothing deletes, recreates
	// or restores into it between the two. Backup re-enters this lock.
	release, err := s.lockBench(b.Name)
	if err != nil {
		res.Result, res.Err = RunSkippedBusy, err
		_ = writeRunState(b.Name, RunState{LastAttempt: now, Result: res.Result})
		return res
	}
	defer release()

	err = s.Backup(BackupInput{
		BenchName:     b.Name,
		NoFiles:       !res.WithFiles,
		Trigger:       TriggerScheduled,
		SkipIfStopped: true,
	}, logProgress{w: log})
	switch {
	case errors.Is(err, ErrBenchStopped):
		res.Result = RunSkippedStopped
	case err != nil:
		res.Result, res.Err = RunFailed, err
	default:
		res.Result = RunOK
		if after, serr := scheduleStatus(b); serr == nil && !after.LastSuccess.IsZero() {
			res.Archive = archiveFileName(b.Name, TriggerScheduled, after.LastSuccess)
		}
		pr, perr := pruneWithPolicy(b.Name, *b.BackupSchedule, false, s.clock(), time.Local)
		res.Pruned = len(pr.Removed)
		if perr != nil {
			// The backup itself succeeded; a prune failure is reported but
			// does not turn the run into a failed backup.
			fmt.Fprintf(log, "warning: %s: prune failed: %v\n", b.Name, perr)
		}
	}
	rs := RunState{LastAttempt: now, Result: res.Result}
	if res.Err != nil {
		rs.Error = scrubSecrets(res.Err.Error(), benchSecrets(b)...)
	}
	_ = writeRunState(b.Name, rs)
	return res
}

// logProgress keeps a scheduled backup's warnings and drops its step chatter:
// the log should hold one line per bench per run, plus anything that went
// wrong.
type logProgress struct{ w io.Writer }

func (logProgress) Step(string)           {}
func (logProgress) Printf(string, ...any) {}
func (logProgress) Println(...any)        {}
func (p logProgress) Stderr() io.Writer   { return p.w }
