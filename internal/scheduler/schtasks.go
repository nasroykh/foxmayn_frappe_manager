package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// schtasksBackend runs the job from Windows Task Scheduler. The task starts a
// small .cmd file beside the state file, which sets the environment and runs
// ffm: a task's /TR is limited to 261 characters and has no environment.
type schtasksBackend struct{}

func (schtasksBackend) Name() string { return BackendSchtasks }

func (schtasksBackend) Available() error {
	if _, err := lookPath("schtasks"); err != nil {
		return fmt.Errorf("schtasks is not installed (Task Scheduler exists on Windows only): %w", err)
	}
	return nil
}

func (schtasksBackend) Where() string {
	return "Task Scheduler task " + taskName()
}

func cmdPath() string {
	return filepath.Join(config.ConfigDir(), withSuffix("backup-run-due", "-")+".cmd")
}

// cmdValue escapes s for a cmd.exe batch file: % would expand.
func cmdValue(s string) string { return strings.ReplaceAll(s, "%", "%%") }

func (schtasksBackend) Render(j Job) string {
	lines := []string{
		"@echo off",
		fmt.Sprintf("rem ffm backup job %s, hourly at minute %02d", cmdValue(taskName()), j.Minute),
	}
	for _, kv := range j.envPairs() {
		if kv[0] == "PATH" {
			lines = append(lines, fmt.Sprintf(`set "PATH=%s;%%PATH%%"`, cmdValue(kv[1])))
			continue
		}
		lines = append(lines, fmt.Sprintf(`set "%s=%s"`, kv[0], cmdValue(kv[1])))
	}
	args := j.runArgs()
	args[len(args)-1] = `"` + cmdValue(j.LogFile) + `"`
	lines = append(lines, fmt.Sprintf(`"%s" %s >> "%s" 2>&1`, cmdValue(j.FFM), strings.Join(args, " "), cmdValue(j.LogFile)))
	return strings.Join(lines, "\r\n") + "\r\n"
}

func createArgs(j Job) []string {
	return []string{"/Create", "/TN", taskName(), "/SC", "HOURLY", "/ST", fmt.Sprintf("00:%02d", j.Minute), "/F",
		"/TR", `"` + cmdPath() + `"`}
}

func (b schtasksBackend) Print(j Job) string {
	return fmt.Sprintf("rem Save as %s:\r\n%s\r\nrem Then run:\r\nschtasks /Create /TN \"%s\" /SC HOURLY /ST 00:%02d /F /TR \"\\\"%s\\\"\"\r\n",
		cmdPath(), b.Render(j), taskName(), j.Minute, cmdPath())
}

func (schtasksBackend) taskExists() bool {
	_, _, err := run("schtasks", "/Query", "/TN", taskName())
	return err == nil
}

func (schtasksBackend) Installed() (string, bool, error) {
	exists := schtasksBackend{}.taskExists()
	data, err := os.ReadFile(cmdPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	return string(data), exists, nil
}

func (b schtasksBackend) Install(j Job, force bool) (bool, error) {
	changed, err := writeIfChanged(cmdPath(), []byte(b.Render(j)), 0o600, 0o700)
	if err != nil {
		return false, err
	}
	if !changed && b.taskExists() {
		return false, nil
	}
	if _, stderr, err := run("schtasks", createArgs(j)...); err != nil {
		return true, fmt.Errorf("schtasks /Create: %w: %s", err, stderr)
	}
	return true, nil
}

// Uninstall needs no ownership check: the task name carries a hash of the
// configuration directory.
func (b schtasksBackend) Uninstall(logFile string, force bool) (bool, error) {
	exists := b.taskExists()
	_, statErr := os.Stat(cmdPath())
	if !exists && statErr != nil {
		return false, nil
	}
	if exists {
		if _, stderr, err := run("schtasks", "/Delete", "/TN", taskName(), "/F"); err != nil {
			return true, fmt.Errorf("schtasks /Delete: %w: %s", err, stderr)
		}
	}
	if err := os.Remove(cmdPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	return true, nil
}
