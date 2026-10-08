package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

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
)

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
	_ = os.MkdirAll(config.ConfigDir(), 0o755)
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
	return s.startJob(ctx, js, JobCreate, in.Name, func(pw ProgressWriter) error {
		return s.Create(in, pw)
	})
}

// StartRecreate enqueues a recreate job.
func (s *Service) StartRecreate(ctx context.Context, js *JobStore, in RecreateInput) (string, error) {
	return s.startJob(ctx, js, JobRecreate, in.Name, func(pw ProgressWriter) error {
		return s.Recreate(in, pw)
	})
}

func (s *Service) startJob(_ context.Context, js *JobStore, typ JobType, benchName string, fn func(ProgressWriter) error) (string, error) {
	js.mu.Lock()
	if _, ok := js.byBench[benchName]; ok {
		js.mu.Unlock()
		return "", fmt.Errorf("a job is already running for bench %q", benchName)
	}
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixNano())
	j := &Job{
		ID:        id,
		Type:      typ,
		BenchName: benchName,
		Status:    JobPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	js.jobs[id] = j
	js.byBench[benchName] = id
	js.persistLocked()
	js.mu.Unlock()

	go func() {
		pw := &jobProgress{job: j, store: js}
		js.mu.Lock()
		j.Status = JobRunning
		j.UpdatedAt = time.Now()
		js.persistLocked()
		js.mu.Unlock()

		err := fn(pw)

		js.mu.Lock()
		defer js.mu.Unlock()
		j.UpdatedAt = time.Now()
		if err != nil {
			j.Status = JobFailed
			j.Error = err.Error()
		} else {
			j.Status = JobSucceeded
		}
		delete(js.byBench, benchName)
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
// happen, so SSE consumers see them live.
type jobProgress struct {
	job   *Job
	store *JobStore
	BufferProgress
}

// sync copies the buffered output into the job. Steps are persisted, so a
// crash leaves a record of how far the job got; plain lines are not, to keep
// chatty output from rewriting jobs.json on every line.
func (p *jobProgress) sync(persist bool) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	p.job.Steps = append([]string(nil), p.BufferProgress.Steps...)
	p.job.Lines = append([]string(nil), p.BufferProgress.Lines...)
	p.job.UpdatedAt = time.Now()
	if persist {
		p.store.persistLocked()
	}
}

func (p *jobProgress) Step(msg string) {
	p.BufferProgress.Step(msg)
	p.sync(true)
}

func (p *jobProgress) Printf(format string, args ...any) {
	p.BufferProgress.Printf(format, args...)
	p.sync(false)
}

func (p *jobProgress) Println(args ...any) {
	p.BufferProgress.Println(args...)
	p.sync(false)
}
