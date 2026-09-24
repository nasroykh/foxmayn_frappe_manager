// Package scheduler installs the single hourly system job that runs
// `ffm backup run-due`.
//
// There is one job for the whole host, not one per bench: run-due reads every
// bench's policy and decides what is due. On Linux and macOS the job is a
// line in the user's crontab, tagged so ffm can find, update and remove it
// without touching anything else in the file. Windows is not installed
// automatically; WindowsCommand prints the Task Scheduler equivalent.
package scheduler

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// Tag marks ffm's crontab line.
const Tag = "# ffm-backup-tick"

// passthroughEnv are carried into the job when set at install time: cron
// starts with an almost empty environment, so a custom state directory or a
// rootless Docker socket would otherwise be invisible to the hourly run.
var passthroughEnv = []string{
	"FFM_CONFIG_DIR", "FFM_BENCHES_DIR", "FFM_BACKUPS_DIR",
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
}

// Job describes the hourly job.
type Job struct {
	Minute  int
	FFM     string // absolute path to the ffm binary
	PathEnv string // PATH for the job; must reach docker
	Env     map[string]string
	LogFile string
}

// CurrentJob builds the job for this host from the running binary and
// environment.
func CurrentJob() (Job, error) {
	exe, err := os.Executable()
	if err != nil {
		return Job{}, fmt.Errorf("locate the ffm binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.HasPrefix(exe, os.TempDir()) {
		return Job{}, fmt.Errorf("ffm is running from %s, a temporary build (go run?) — "+
			"install ffm first so the hourly job has a binary that stays put", exe)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return Job{}, fmt.Errorf("docker is not on PATH, so the hourly job could not find it either: %w", err)
	}
	env := map[string]string{}
	for _, k := range passthroughEnv {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	host, _ := os.Hostname()
	return Job{
		Minute:  minuteFor(host),
		FFM:     exe,
		PathEnv: jobPath(filepath.Dir(docker)),
		Env:     env,
		LogFile: config.BackupSchedulerLogFile(),
	}, nil
}

// minuteFor spreads hosts across the hour instead of all firing at :00.
func minuteFor(host string) int {
	h := fnv.New32a()
	h.Write([]byte(host))
	return int(h.Sum32() % 60)
}

// jobPath puts docker's directory first, then the usual system directories.
func jobPath(dockerDir string) string {
	parts := []string{dockerDir}
	for _, p := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		if p != dockerDir {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ":")
}

// Line renders the crontab line.
func (j Job) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d * * * * PATH=%s", j.Minute, shellQuote(j.PathEnv))
	for _, k := range passthroughEnv {
		if v, ok := j.Env[k]; ok {
			fmt.Fprintf(&b, " %s=%s", k, shellQuote(v))
		}
	}
	fmt.Fprintf(&b, " %s backup run-due --non-interactive --log %s >> %s 2>&1 %s",
		shellQuote(j.FFM), shellQuote(j.LogFile), shellQuote(j.LogFile), Tag)
	// A bare % in a crontab command is turned into a newline by cron.
	return strings.ReplaceAll(b.String(), "%", `\%`)
}

// shellQuote single-quotes s for /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Merge returns crontab with ffm's line replaced by line (or added).
func Merge(crontab, line string) string {
	kept := Remove(crontab)
	if kept != "" && !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	return kept + line + "\n"
}

// Remove returns crontab without ffm's line. Every other line is kept
// byte for byte.
func Remove(crontab string) string {
	if crontab == "" {
		return ""
	}
	lines := strings.SplitAfter(crontab, "\n")
	var out strings.Builder
	for _, l := range lines {
		if strings.Contains(l, Tag) {
			continue
		}
		out.WriteString(l)
	}
	return out.String()
}

// Find returns ffm's line in crontab, or "".
func Find(crontab string) string {
	for _, l := range strings.Split(crontab, "\n") {
		if strings.Contains(l, Tag) {
			return l
		}
	}
	return ""
}

// ErrUnsupported is returned where ffm does not install the job itself.
var ErrUnsupported = errors.New("automatic installation of the hourly job is not supported on this platform")

func supported() error {
	if runtime.GOOS == "windows" {
		return ErrUnsupported
	}
	if _, err := exec.LookPath("crontab"); err != nil {
		return fmt.Errorf("crontab is not installed: %w — add the line from "+
			"'ffm backup scheduler print' to your system's scheduler instead", err)
	}
	return nil
}

// ReadCrontab returns the user's crontab; no crontab reads as empty.
func ReadCrontab() (string, error) {
	if err := supported(); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("crontab", "-l")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// "no crontab for <user>" exits 1 on every cron implementation.
		if strings.Contains(strings.ToLower(stderr.String()), "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func writeCrontab(content string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("crontab -: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Install adds or updates ffm's line. It reports whether the crontab changed.
func Install(j Job) (bool, error) {
	current, err := ReadCrontab()
	if err != nil {
		return false, err
	}
	line := j.Line()
	if Find(current) == line {
		return false, nil
	}
	return true, writeCrontab(Merge(current, line))
}

// Uninstall removes ffm's line. It reports whether the crontab changed.
func Uninstall() (bool, error) {
	current, err := ReadCrontab()
	if err != nil {
		return false, err
	}
	if Find(current) == "" {
		return false, nil
	}
	return true, writeCrontab(Remove(current))
}

// WindowsCommand is the Task Scheduler equivalent of the crontab line.
func WindowsCommand(j Job) string {
	return fmt.Sprintf(`schtasks /Create /TN "ffm backup run-due" /SC HOURLY /ST 00:%02d /F /TR "\"%s\" backup run-due --non-interactive --log \"%s\""`,
		j.Minute, j.FFM, j.LogFile)
}
