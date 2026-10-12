package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestStartActionRefusesABusyKey(t *testing.T) {
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	s := &Service{}
	release := make(chan struct{})
	id, err := s.StartAction(js, JobStart, "alpha", func(ProgressWriter) error { <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.StartAction(js, JobStop, "alpha", func(ProgressWriter) error { return nil })
	if !errors.Is(err, ErrBenchBusy) {
		t.Fatalf("second job on the same key = %v, want ErrBenchBusy", err)
	}
	// Another key is independent, and so is the proxy.
	id2, err := s.StartAction(js, JobProxyStart, ProxyJobKey, func(ProgressWriter) error { return nil })
	if err != nil {
		t.Fatalf("proxy job refused by a bench job: %v", err)
	}
	waitFinished(t, js, id2)
	close(release)
	waitFinished(t, js, id)
	id3, err := s.StartAction(js, JobStop, "alpha", func(ProgressWriter) error { return nil })
	if err != nil {
		t.Fatalf("key still busy after its job finished: %v", err)
	}
	waitFinished(t, js, id3)
}

func TestJobLinesKeepTheLast5000(t *testing.T) {
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	id, err := (&Service{}).StartAction(js, JobStart, "alpha", func(pw ProgressWriter) error {
		for i := 0; i < maxJobLines+100; i++ {
			pw.Println(fmt.Sprintf("line %d", i))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	j := waitFinished(t, js, id)
	if len(j.Lines) != maxJobLines {
		t.Fatalf("kept %d lines, want %d", len(j.Lines), maxJobLines)
	}
	if j.LinesDropped != 100 || j.Lines[0] != "line 100" || j.Lines[len(j.Lines)-1] != fmt.Sprintf("line %d", maxJobLines+99) {
		t.Fatalf("dropped=%d first=%q last=%q", j.LinesDropped, j.Lines[0], j.Lines[len(j.Lines)-1])
	}
}

func TestStderrWriterSplitsLinesAndFlushes(t *testing.T) {
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	id, err := (&Service{}).StartAction(js, JobStart, "alpha", func(pw ProgressWriter) error {
		w := pw.Stderr()
		fmt.Fprint(w, "first\nsec")
		fmt.Fprint(w, "ond\r\nno newline at the end")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	j := waitFinished(t, js, id)
	want := []string{"first", "second", "no newline at the end"}
	if strings.Join(j.Lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", j.Lines, want)
	}
}

func TestBufferProgressStderrLandsInLines(t *testing.T) {
	var b BufferProgress
	b.Step("one")
	fmt.Fprintln(b.Stderr(), "boom")
	if got := strings.Join(b.Lines, "|"); got != "→ one|boom" {
		t.Fatalf("lines = %q", got)
	}
}

// newJobBench registers a dev bench with a compose file in isolated dirs.
func newJobBench(t *testing.T, name string) (*Service, state.Bench) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	b := state.Bench{
		Name: name, Mode: "dev", SiteName: name + ".localhost", WebPort: 8000, SocketIOPort: 9000,
		AdminPassword: "adm1n-secret-pw", DBPassword: "db-secret-pw-77", Bind: state.BindLoopback,
		Dir: filepath.Join(root, "benches", name),
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(false)
	if err := s.AddBench(b); err != nil {
		t.Fatal(err)
	}
	return s, b
}

// A failed image build used to dump its output to the dashboard daemon's
// stderr. It must land in the job, with the bench's passwords masked, because
// jobs.json is plain text.
func TestFailedBuildOutputReachesTheJobMasked(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{
		Match: " build", Exit: 1,
		Stdout: "#7 ERROR: process \"pip install\" did not complete\nconnecting with db-secret-pw-77 failed\n",
	})
	s, b := newJobBench(t, "alpha")
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	id, err := s.StartAction(js, JobRestart, b.Name, func(pw ProgressWriter) error {
		return s.Restart(RestartInput{Name: b.Name, Rebuild: true}, pw)
	})
	if err != nil {
		t.Fatal(err)
	}
	j := waitFinished(t, js, id)
	if j.Status != JobFailed {
		t.Fatalf("status = %v, want failed", j.Status)
	}
	all := strings.Join(j.Lines, "\n")
	if !strings.Contains(all, "did not complete") {
		t.Errorf("build output missing from the job:\n%s", all)
	}
	if !strings.Contains(all, "connecting with *** failed") {
		t.Errorf("secret not masked in the job:\n%s", all)
	}
	onDisk, err := os.ReadFile(js.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "db-secret-pw-77") || strings.Contains(string(onDisk), "adm1n-secret-pw") {
		t.Errorf("a password reached jobs.json:\n%s", onDisk)
	}
}

// Anything a job records is scrubbed with the bench's passwords, whichever
// writer it went through, and so is the error.
func TestJobScrubsBenchSecretsEverywhere(t *testing.T) {
	s, b := newJobBench(t, "alpha")
	js := newJobStoreAt(filepath.Join(t.TempDir(), "jobs.json"))
	id, err := s.StartAction(js, JobFFC, b.Name, func(pw ProgressWriter) error {
		pw.Step("using " + b.AdminPassword)
		pw.Printf("db %s\n", b.DBPassword)
		pw.Println("admin", b.AdminPassword)
		fmt.Fprintln(pw.Stderr(), "warn "+b.DBPassword)
		return errors.New("failed with " + b.DBPassword)
	})
	if err != nil {
		t.Fatal(err)
	}
	j := waitFinished(t, js, id)
	blob := strings.Join(j.Lines, "\n") + strings.Join(j.Steps, "\n") + j.Error
	for _, secret := range []string{b.AdminPassword, b.DBPassword} {
		if strings.Contains(blob, secret) {
			t.Errorf("%q leaked into the job:\n%s", secret, blob)
		}
	}
	if !strings.Contains(j.Error, "failed with ***") {
		t.Errorf("error = %q", j.Error)
	}
}
