package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func waitFinished(t *testing.T, js *JobStore, id string) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := js.GetJob(id); ok && j.Status.Finished() {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := js.GetJob(id)
	t.Fatalf("job %s never finished (status %v)", id, j.Status)
	return nil
}

// Listing jobs while one runs used to re-read jobs.json and replace the live
// *Job, so the finished job stayed "running" and blocked its bench forever.
func TestJobFinishesWhileBeingListed(t *testing.T) {
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	release := make(chan struct{})
	s := &Service{}
	id, err := s.startJob(nil, js, JobCreate, "b1", func(pw ProgressWriter) error {
		pw.Step("working")
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		js.ListJobs()
		js.GetJob(id)
		js.FailedCount()
	}
	close(release)
	if j := waitFinished(t, js, id); j.Status != JobSucceeded {
		t.Fatalf("status = %v, want succeeded", j.Status)
	}
	if _, err := s.startJob(nil, js, JobCreate, "b1", func(ProgressWriter) error { return nil }); err != nil {
		t.Fatalf("bench still blocked after its job finished: %v", err)
	}
}

// Run with -race: readers and the job goroutine touch the same records.
func TestJobStoreConcurrentReaders(t *testing.T) {
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	s := &Service{}
	id, err := s.startJob(nil, js, JobCreate, "b2", func(pw ProgressWriter) error {
		for i := 0; i < 50; i++ {
			pw.Step("step")
			pw.Println("line")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if j, ok := js.GetJob(id); ok {
					_ = len(j.Lines) + len(j.Steps)
				}
				js.ListJobs()
			}
		}()
	}
	wg.Wait()
	if j := waitFinished(t, js, id); len(j.Lines) == 0 {
		t.Fatal("job output not recorded")
	}
}

// A job that was running when its process died must not block the bench.
func TestLoadMarksDeadJobsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	stale := []*Job{{ID: "1", Type: JobCreate, BenchName: "b3", Status: JobRunning, CreatedAt: time.Now()}}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	js := newJobStoreAt(path)
	if j, _ := js.GetJob("1"); j.Status != JobInterrupted {
		t.Fatalf("status = %v, want interrupted", j.Status)
	}
	if _, err := (&Service{}).startJob(nil, js, JobCreate, "b3", func(ProgressWriter) error { return nil }); err != nil {
		t.Fatalf("bench blocked by a dead job: %v", err)
	}
	var onDisk []*Job
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &onDisk)
	for _, j := range onDisk {
		if j.ID == "1" && j.Status != JobInterrupted {
			t.Fatalf("interrupted status not saved: %v", j.Status)
		}
	}
}

func TestPersistCapsFinishedJobs(t *testing.T) {
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	js.mu.Lock()
	base := time.Now()
	for i := 0; i < maxFinishedJobs+20; i++ {
		id := string(rune('a'+i%26)) + time.Duration(i).String()
		js.jobs[id] = &Job{ID: id, Status: JobSucceeded, CreatedAt: base.Add(time.Duration(i) * time.Second)}
	}
	js.persistLocked()
	n := len(js.jobs)
	js.mu.Unlock()
	if n != maxFinishedJobs {
		t.Fatalf("kept %d finished jobs, want %d", n, maxFinishedJobs)
	}
}
