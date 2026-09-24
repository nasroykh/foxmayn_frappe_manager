package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTryAcquireIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.lock")
	first, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire = %v, want ErrHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	again.Release()
}

func TestReleaseNilIsSafe(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}

// TestHeldAcrossProcesses re-runs this test binary as a child that tries the
// lock while the parent holds it — the case the lock exists for.
func TestHeldAcrossProcesses(t *testing.T) {
	if path := os.Getenv("FFM_LOCK_TEST_CHILD"); path != "" {
		_, err := TryAcquire(path)
		if errors.Is(err, ErrHeld) {
			os.Exit(3)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "x.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHeldAcrossProcesses$")
	cmd.Env = append(os.Environ(), "FFM_LOCK_TEST_CHILD="+path)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("child acquire while parent holds the lock: %v, want exit 3 (ErrHeld)", err)
	}
}

// TestReadOnlyLockFileStillLocks is the `sudo ffm` leftover: a lock file the
// current user cannot write must still be lockable.
func TestReadOnlyLockFileStillLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.lock")
	if err := os.WriteFile(path, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0444 file; the fallback is not reachable")
	}
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("acquire a read-only lock file: %v", err)
	}
	defer l.Release()
	if _, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire = %v, want ErrHeld", err)
	}
}
