package bench

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The stop half of DevServerRestartCmd must kill a running "honcho start" and
// let the script that runs it carry on. With the pattern "honcho start" pkill
// matched the script's own `bash -c` command line and killed it, so the dev
// server was never started again.
//
// Only Linux shows the bug (and the containers are Linux): procps pkill
// excludes only itself, while BSD/macOS pkill also excludes its ancestors.
// CI runs this on Linux.
func TestDevServerStopSurvivesItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs bash, pkill and pgrep")
	}
	for _, tool := range []string{"bash", "pkill", "pgrep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	// A stand-in honcho whose command line contains "honcho start".
	honcho := exec.Command("bash", "-c", "exec -a 'honcho start' sleep 60")
	if err := honcho.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- honcho.Wait() }()
	defer honcho.Process.Kill()
	time.Sleep(200 * time.Millisecond)

	out, err := exec.Command("bash", "-c", devServerStopCmd+"; echo reached-start").CombinedOutput()
	if err != nil {
		t.Fatalf("stop script failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reached-start") {
		t.Fatalf("the stop command killed its own shell; output: %q", out)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("honcho stand-in was not stopped")
	}
}
