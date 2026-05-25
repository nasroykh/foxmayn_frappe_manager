package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
)

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

// JobStore manages background jobs.
type JobStore struct {
	mu       sync.RWMutex
	jobs     map[string]*Job
	byBench  map[string]string // bench name -> active job id
	path     string
}

// NewJobStore creates an in-memory job store with optional persistence path.
func NewJobStore() *JobStore {
	return &JobStore{
		jobs:    make(map[string]*Job),
		byBench: make(map[string]string),
		path:    config.JobsFile(),
	}
}

func (js *JobStore) loadLocked() {
	if js.path == "" {
		return
	}
	data, err := os.ReadFile(js.path)
	if err != nil {
		return
	}
	var list []*Job
	if json.Unmarshal(data, &list) == nil {
		for _, j := range list {
			js.jobs[j.ID] = j
			if j.Status == JobRunning || j.Status == JobPending {
				js.byBench[j.BenchName] = j.ID
			}
		}
	}
}

func (js *JobStore) persistLocked() {
	if js.path == "" {
		return
	}
	list := make([]*Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		list = append(list, j)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(config.ConfigDir(), 0o755)
	_ = os.WriteFile(js.path, data, 0o600)
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

// StartRestartRebuild enqueues restart with --rebuild.
func (s *Service) StartRestartRebuild(ctx context.Context, js *JobStore, name string) (string, error) {
	return s.startJob(ctx, js, JobRestart, name, func(pw ProgressWriter) error {
		return s.Restart(RestartInput{Name: name, Rebuild: true}, pw)
	})
}

func (s *Service) startJob(_ context.Context, js *JobStore, typ JobType, benchName string, fn func(ProgressWriter) error) (string, error) {
	js.mu.Lock()
	js.loadLocked()
	if _, ok := js.byBench[benchName]; ok {
		js.mu.Unlock()
		return "", fmt.Errorf("a job is already running for bench %q", benchName)
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	j := &Job{
		ID:        id,
		Type:      typ,
		BenchName: benchName,
		Status:    JobPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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
		js.mu.Unlock()

		err := fn(pw)

		js.mu.Lock()
		defer js.mu.Unlock()
		j.Steps = pw.Steps
		j.Lines = append([]string(nil), pw.Lines...)
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

// GetJob returns a job by ID.
func (js *JobStore) GetJob(id string) (*Job, bool) {
	js.mu.RLock()
	defer js.mu.RUnlock()
	j, ok := js.jobs[id]
	if !ok {
		js.loadLocked()
		j, ok = js.jobs[id]
	}
	return j, ok
}

// ListJobs returns all jobs newest first.
func (js *JobStore) ListJobs() []*Job {
	js.mu.RLock()
	defer js.mu.RUnlock()
	js.loadLocked()
	out := make([]*Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		out = append(out, j)
	}
	return out
}

// FailedCount returns the number of failed jobs.
func (js *JobStore) FailedCount() int {
	n := 0
	for _, j := range js.ListJobs() {
		if j.Status == JobFailed {
			n++
		}
	}
	return n
}

// jobProgress writes steps into the job record for SSE consumers.
type jobProgress struct {
	job   *Job
	store *JobStore
	BufferProgress
}

func (p *jobProgress) Step(msg string) {
	p.BufferProgress.Step(msg)
	p.store.mu.Lock()
	p.job.Steps = append([]string(nil), p.BufferProgress.Steps...)
	p.job.Lines = append([]string(nil), p.BufferProgress.Lines...)
	p.job.UpdatedAt = time.Now()
	p.store.mu.Unlock()
	p.store.persistLocked()
}

func (p *jobProgress) Printf(format string, args ...any) {
	p.BufferProgress.Printf(format, args...)
	p.store.mu.Lock()
	p.job.Lines = append([]string(nil), p.BufferProgress.Lines...)
	p.job.UpdatedAt = time.Now()
	p.store.mu.Unlock()
}
