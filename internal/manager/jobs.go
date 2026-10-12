package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// JobStatus is the lifecycle state of a background job.
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	// JobInterrupted marks a job that was pending or running when the process
	// that ran it exited. Nothing will ever finish it, so it no longer blocks
	// its bench.
	JobInterrupted JobStatus = "interrupted"
)

// Finished reports whether the job has reached a terminal state.
func (s JobStatus) Finished() bool {
	return s == JobSucceeded || s == JobFailed || s == JobInterrupted
}

// JobType identifies the operation.
type JobType string

const (
	JobCreate   JobType = "create"
	JobRecreate JobType = "recreate"
	JobRestart  JobType = "restart"
	JobStart    JobType = "start"
	JobStop     JobType = "stop"
	JobDelete   JobType = "delete"
	JobFFC      JobType = "ffc"
	JobSetProxy JobType = "set-proxy"
	// JobTunnelOn / JobTunnelOff enable and disable a bench's VPS tunnel.
	JobTunnelOn   JobType = "tunnel-on"
	JobTunnelOff  JobType = "tunnel-off"
	JobCleanLogs  JobType = "clean-logs"
	JobProxyStart JobType = "proxy-start"
	JobProxyStop  JobType = "proxy-stop"
	// The dashboard's parity pages.
	JobBackup          JobType = "backup"
	JobPrune           JobType = "prune"
	JobRestore         JobType = "restore"
	JobClone           JobType = "clone"
	JobUpdate          JobType = "update"
	JobDoctor          JobType = "doctor"
	JobDomainAdd       JobType = "domain-add"
	JobDomainRemove    JobType = "domain-remove"
	JobSnapshotCreate  JobType = "snapshot-create"
	JobSnapshotRestore JobType = "snapshot-restore"
)

// ProxyJobKey is the job key of the shared Traefik proxy, which belongs to no
// bench. It starts with an underscore, which no bench name can.
const ProxyJobKey = "_proxy"

// maxJobLines bounds Job.Lines: a long build is tens of thousands of lines,
// and jobs.json holds up to 100 jobs.
const maxJobLines = 5000

// Job holds async operation state.
type Job struct {
	ID        string    `json:"id"`
	Type      JobType   `json:"type"`
	BenchName string    `json:"bench_name"`
	Status    JobStatus `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Steps     []string  `json:"steps,omitempty"`
	Lines     []string  `json:"lines,omitempty"`
	// LinesDropped counts the oldest lines cut to stay within maxJobLines.
	// The absolute number of a line is LinesDropped plus its index in Lines,
	// which is how the SSE stream tells a client what is new.
	LinesDropped int `json:"lines_dropped,omitempty"`
}

// snapshot returns a copy that is safe to read without the store's lock.
func (j *Job) snapshot() *Job {
	c := *j
	c.Steps = append([]string(nil), j.Steps...)
	c.Lines = append([]string(nil), j.Lines...)
	return &c
}

// maxFinishedJobs bounds jobs.json: older finished jobs are dropped on save.
const maxFinishedJobs = 100

// JobStore manages background jobs for one process (the dashboard).
//
// jobs.json is read once, when the store is created. It used to be re-read on
// every list and lookup, which replaced the *Job a running goroutine was
// updating with a stale copy from disk: the job then stayed "running" forever
// and blocked its bench. Every read and write of a Job happens under mu, and
// readers only ever get snapshots.
type JobStore struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	byBench map[string]string // bench name -> active job id
	path    string
}

// NewJobStore creates the job store, loading jobs.json from earlier runs.
func NewJobStore() *JobStore {
	return newJobStoreAt(config.JobsFile())
}

func newJobStoreAt(path string) *JobStore {
	js := &JobStore{
		jobs:    make(map[string]*Job),
		byBench: make(map[string]string),
		path:    path,
	}
	js.mu.Lock()
	defer js.mu.Unlock()
	if js.load() {
		js.persistLocked()
	}
	return js
}

// load reads jobs.json. Jobs that were still pending or running belonged to a
// process that has exited, so they are marked interrupted; it reports whether
// any were, so the caller can save that.
func (js *JobStore) load() (changed bool) {
	if js.path == "" {
		return false
	}
	data, err := os.ReadFile(js.path)
	if err != nil {
		return false
	}
	var list []*Job
	if json.Unmarshal(data, &list) != nil {
		return false
	}
	for _, j := range list {
		if !j.Status.Finished() {
			j.Status = JobInterrupted
			j.Error = "the process running this job exited before it finished"
			j.UpdatedAt = time.Now()
			changed = true
		}
		js.jobs[j.ID] = j
	}
	return changed
}

// persistLocked writes jobs.json. The caller holds mu.
func (js *JobStore) persistLocked() {
	if js.path == "" {
		return
	}
	list := js.sortedLocked()
	kept := list[:0]
	finished := 0
	for _, j := range list {
		if j.Status.Finished() {
			finished++
			if finished > maxFinishedJobs {
				delete(js.jobs, j.ID)
				continue
			}
		}
		kept = append(kept, j)
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(js.path), 0o755)
	_ = os.WriteFile(js.path, data, 0o600)
}

// sortedLocked returns the jobs newest first. The caller holds mu.
func (js *JobStore) sortedLocked() []*Job {
	list := make([]*Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].CreatedAt.After(list[b].CreatedAt) })
	return list
}

// StartCreate enqueues a create job.
func (s *Service) StartCreate(ctx context.Context, js *JobStore, in CreateInput) (string, error) {
	return s.startJobSecrets(js, JobCreate, in.Name, []string{in.AdminPassword, in.DBPassword, in.GithubToken}, func(pw ProgressWriter) error {
		return s.Create(in, pw)
	})
}

// StartRecreate enqueues a recreate job.
func (s *Service) StartRecreate(ctx context.Context, js *JobStore, in RecreateInput) (string, error) {
	secrets := []string{in.GithubToken}
	if s.Store != nil {
		if b, err := s.GetBench(in.Name); err == nil {
			secrets = benchSecrets(b, in.GithubToken)
		}
	}
	return s.startJobSecrets(js, JobRecreate, in.Name, secrets, func(pw ProgressWriter) error {
		return s.Recreate(in, pw)
	})
}

// StartAction runs fn as a background job keyed by key: a bench name, or
// ProxyJobKey for the shared proxy. It refuses while another job holds the
// key, with ErrBenchBusy like lockBench does for an operation in another
// process. Output of fn reaches the job through the ProgressWriter, with the
// key bench's passwords masked.
func (s *Service) StartAction(js *JobStore, typ JobType, key string, fn func(ProgressWriter) error) (string, error) {
	var secrets []string
	if s.Store != nil && key != "" && key != ProxyJobKey {
		if b, err := s.GetBench(key); err == nil {
			secrets = benchSecrets(b)
		}
	}
	return s.startJobSecrets(js, typ, key, secrets, fn)
}

func (s *Service) startJob(_ context.Context, js *JobStore, typ JobType, benchName string, fn func(ProgressWriter) error) (string, error) {
	return s.startJobSecrets(js, typ, benchName, nil, fn)
}

// startJobSecrets is startJob that also masks secrets in everything the job
// records (steps, lines, the error), because jobs.json is plain text.
func (s *Service) startJobSecrets(js *JobStore, typ JobType, key string, secrets []string, fn func(ProgressWriter) error) (string, error) {
	js.mu.Lock()
	if _, ok := js.byBench[key]; ok {
		js.mu.Unlock()
		return "", fmt.Errorf("%w: a job is already running for %q", ErrBenchBusy, key)
	}
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixNano())
	j := &Job{
		ID:        id,
		Type:      typ,
		BenchName: key,
		Status:    JobPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	js.jobs[id] = j
	js.byBench[key] = id
	js.persistLocked()
	js.mu.Unlock()

	go func() {
		pw := newJobProgress(js, j, secrets)
		js.mu.Lock()
		j.Status = JobRunning
		j.UpdatedAt = time.Now()
		js.persistLocked()
		js.mu.Unlock()

		err := fn(pw)
		pw.errw.Flush()
		s.InvalidateStatus()

		js.mu.Lock()
		defer js.mu.Unlock()
		j.UpdatedAt = time.Now()
		if err != nil {
			j.Status = JobFailed
			j.Error = bench.Scrub(err.Error(), secrets...)
		} else {
			j.Status = JobSucceeded
		}
		delete(js.byBench, key)
		js.persistLocked()
	}()

	return id, nil
}

// GetJob returns a snapshot of a job by ID.
func (js *JobStore) GetJob(id string) (*Job, bool) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.jobs[id]
	if !ok {
		return nil, false
	}
	return j.snapshot(), true
}

// ListJobs returns snapshots of all jobs, newest first.
func (js *JobStore) ListJobs() []*Job {
	js.mu.Lock()
	defer js.mu.Unlock()
	list := js.sortedLocked()
	for i, j := range list {
		list[i] = j.snapshot()
	}
	return list
}

// FailedCount returns the number of failed jobs.
func (js *JobStore) FailedCount() int {
	js.mu.Lock()
	defer js.mu.Unlock()
	n := 0
	for _, j := range js.jobs {
		if j.Status == JobFailed {
			n++
		}
	}
	return n
}

// jobProgress writes steps and output lines into the job record as they
// happen, so SSE consumers see them live. Only the new line is appended each
// time; the record keeps the last maxJobLines.
type jobProgress struct {
	job     *Job
	store   *JobStore
	secrets []string
	errw    *lineWriter
}

func newJobProgress(js *JobStore, j *Job, secrets []string) *jobProgress {
	p := &jobProgress{job: j, store: js, secrets: secrets}
	p.errw = &lineWriter{emit: func(line string) { p.add("", line, false) }}
	return p
}

// add appends one line (and, when step is not empty, a step) to the job.
// Steps are persisted, so a crash leaves a record of how far the job got;
// plain lines are not, to keep chatty output from rewriting jobs.json on
// every line.
func (p *jobProgress) add(step, line string, persist bool) {
	line = bench.Scrub(line, p.secrets...)
	step = bench.Scrub(step, p.secrets...)
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	j := p.job
	if step != "" {
		j.Steps = append(j.Steps, step)
	}
	j.Lines = append(j.Lines, line)
	if over := len(j.Lines) - maxJobLines; over > 0 {
		j.Lines = j.Lines[:copy(j.Lines, j.Lines[over:])]
		j.LinesDropped += over
	}
	j.UpdatedAt = time.Now()
	if persist {
		p.store.persistLocked()
	}
}

func (p *jobProgress) Step(msg string) { p.add(msg, "→ "+msg, true) }

func (p *jobProgress) Printf(format string, args ...any) {
	p.add("", fmt.Sprintf(format, args...), false)
}

func (p *jobProgress) Println(args ...any) { p.add("", fmt.Sprint(args...), false) }

func (p *jobProgress) Stderr() io.Writer { return p.errw }
