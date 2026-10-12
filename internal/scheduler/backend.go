package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// Backend names, as given to --backend and recorded in the state file.
const (
	BackendCron     = "cron"
	BackendSystemd  = "systemd"
	BackendLaunchd  = "launchd"
	BackendSchtasks = "schtasks"
)

// Backends lists every backend name in the order help text shows them.
var Backends = []string{BackendCron, BackendSystemd, BackendLaunchd, BackendSchtasks}

// Seams the tests replace. Nothing in this package reads the platform, the
// PATH, the home directory or /proc except through these.
var (
	goos     = runtime.GOOS
	lookPath = func(file string) (string, error) { return execx.LookPath(file) }
	homeDir  = os.UserHomeDir
	getenv   = os.Getenv
	procDir  = "/proc"
	uid      = os.Getuid
)

// Backend is one way to run the hourly job on a host.
type Backend interface {
	Name() string
	// Available reports why the backend cannot be used here, nil if it can.
	Available() error
	// Where names the place the job lives, for messages.
	Where() string
	// Install creates or updates the job. It reports whether anything
	// changed. A job that belongs to another ffm configuration is left alone
	// unless force.
	Install(j Job, force bool) (bool, error)
	// Uninstall removes the job if it is this configuration's (logFile) or
	// force is set. It reports whether anything changed.
	Uninstall(logFile string, force bool) (bool, error)
	// Installed returns what is installed now, in the form Render uses, so
	// the two compare equal when the job is current.
	Installed() (string, bool, error)
	// Render is the job as this backend stores it.
	Render(j Job) string
	// Print is Render plus the steps to install it by hand.
	Print(j Job) string
}

// ErrNoBackend means no backend can run the job on this host.
var ErrNoBackend = errors.New("no scheduler is available on this host")

// ByName returns the backend called name.
func ByName(name string) (Backend, error) {
	switch name {
	case BackendCron:
		return cronBackend{}, nil
	case BackendSystemd:
		return systemdBackend{}, nil
	case BackendLaunchd:
		return launchdBackend{}, nil
	case BackendSchtasks:
		return schtasksBackend{}, nil
	}
	return nil, fmt.Errorf("unknown scheduler backend %q (use %s)", name, strings.Join(Backends, ", "))
}

// jobHash is empty for the default configuration directory and a short hash
// of the absolute directory when FFM_CONFIG_DIR is set, so a second
// configuration gets a job of its own instead of replacing the first.
func jobHash() string {
	if os.Getenv("FFM_CONFIG_DIR") == "" {
		return ""
	}
	dir := config.ConfigDir()
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	h := fnv.New32a()
	h.Write([]byte(dir))
	return fmt.Sprintf("%08x", h.Sum32())
}

func withSuffix(base, sep string) string {
	if h := jobHash(); h != "" {
		return base + sep + h
	}
	return base
}

// JobName is the unit name for systemd and the stem of the schtasks file.
func JobName() string { return withSuffix("ffm-backup", "-") }

func launchdLabel() string { return withSuffix("dev.foxmayn.ffm.backup", ".") }

func taskName() string { return withSuffix(`\ffm\backup`, "-") }

// State records where this configuration's job was installed, so later
// commands act on the same scheduler without guessing.
type State struct {
	Backend string `json:"backend"`
	Name    string `json:"name"`
	FFM     string `json:"ffm,omitempty"`
}

// LoadState reads the state file; a missing file is not an error.
func LoadState() (State, bool, error) {
	data, err := os.ReadFile(config.BackupSchedulerFile())
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil || s.Backend == "" {
		return State{}, false, fmt.Errorf("%s is not valid (delete it and run 'ffm backup scheduler install')",
			config.BackupSchedulerFile())
	}
	return s, true, nil
}

// SaveState writes the state file atomically.
func SaveState(s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeIfChanged(config.BackupSchedulerFile(), append(data, '\n'), 0o600, 0o700)
	return err
}

// writeIfChanged writes content atomically unless the file already holds it.
func writeIfChanged(path string, content []byte, mode, dirMode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ffm-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

// Why a backend was chosen.
const (
	SourceFlag    = "--backend"
	SourceState   = "recorded for this configuration"
	SourceLegacy  = "an ffm line already in your crontab"
	SourceDefault = "default for this platform"
)

// Resolve picks the backend: the flag, else the recorded one, else cron when
// an ffm line already sits in the crontab, else the platform default.
func Resolve(flag string) (Backend, string, error) {
	if flag != "" {
		b, err := ByName(flag)
		return b, SourceFlag, err
	}
	st, ok, err := LoadState()
	if err != nil {
		return nil, "", err
	}
	if ok {
		b, err := ByName(st.Backend)
		return b, SourceState, err
	}
	if legacyCronJob() {
		return cronBackend{}, SourceLegacy, nil
	}
	b, err := Default()
	return b, SourceDefault, err
}

// legacyCronJob reports an ffm line in the crontab, from before backends.
func legacyCronJob() bool {
	if _, err := lookPath("crontab"); err != nil {
		return false
	}
	current, err := ReadCrontab()
	return err == nil && ownLine(current) != ""
}

// Default is the platform's scheduler: Task Scheduler on Windows, launchd on
// macOS; on Linux cron when crontab exists and a cron daemon runs, else the
// systemd user manager.
func Default() (Backend, error) {
	switch goos {
	case "windows":
		return schtasksBackend{}, nil
	case "darwin":
		return launchdBackend{}, nil
	}
	_, cronErr := lookPath("crontab")
	if cronErr == nil && cronDaemonRunning() {
		return cronBackend{}, nil
	}
	sd := systemdBackend{}
	sdErr := sd.Available()
	if sdErr == nil {
		return sd, nil
	}
	why := "crontab is not installed"
	if cronErr == nil {
		why = "crontab is installed but no cron daemon is running (WSL2 without systemd starts none)"
	}
	return nil, fmt.Errorf("%w: %s, and the systemd user manager is not usable (%v); "+
		"start cron, enable systemd, or install the job by hand with 'ffm backup scheduler print --backend cron'",
		ErrNoBackend, why, sdErr)
}

// cronDaemonRunning looks for a cron or crond process under procDir.
func cronDaemonRunning() bool {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "" || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(procDir, e.Name(), "comm"))
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(comm)) {
		case "cron", "crond":
			return true
		}
	}
	return false
}

// Result describes what Install or Uninstall did.
type Result struct {
	Backend  string
	Where    string
	Changed  bool
	Removed  []string // backends whose job was removed after a switch
	Warnings []string
}

// Install installs the job with the resolved backend and records it. When the
// job moves to another backend the old one is removed afterwards: the worst
// case is two jobs for a moment, and the run-due lock makes the second a no-op.
func Install(j Job, flag string, force bool) (Result, error) {
	b, _, err := Resolve(flag)
	if err != nil {
		return Result{}, err
	}
	if err := b.Available(); err != nil {
		return Result{}, fmt.Errorf("%s: %w", b.Name(), err)
	}
	prev := previousBackend()
	changed, err := b.Install(j, force)
	if err != nil {
		return Result{}, err
	}
	res := Result{Backend: b.Name(), Where: b.Where(), Changed: changed}
	if err := SaveState(State{Backend: b.Name(), Name: JobName(), FFM: j.FFM}); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not record the backend in %s: %v", config.BackupSchedulerFile(), err))
	}
	if prev != "" && prev != b.Name() {
		if old, err := ByName(prev); err == nil {
			removed, err := old.Uninstall(j.LogFile, force)
			switch {
			case err != nil:
				res.Warnings = append(res.Warnings, fmt.Sprintf("the job is installed with %s, but the old %s job was not removed: %v", b.Name(), prev, err))
			case removed:
				res.Removed = append(res.Removed, prev)
			}
		}
	}
	if b.Name() == BackendSystemd {
		if w := lingerWarning(); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
	}
	return res, nil
}

// previousBackend is where the job sits now, per the state file or a crontab
// line from before backends existed.
func previousBackend() string {
	if st, ok, err := LoadState(); err == nil && ok {
		return st.Backend
	}
	if legacyCronJob() {
		return BackendCron
	}
	return ""
}

// Uninstall removes the job from the backend that holds it. Nothing to remove
// is not an error.
func Uninstall(logFile string, force bool) (Result, error) {
	b, _, err := Resolve("")
	if err != nil {
		if errors.Is(err, ErrNoBackend) {
			return Result{}, nil
		}
		return Result{}, err
	}
	if b.Available() != nil {
		return Result{Backend: b.Name()}, nil
	}
	changed, err := b.Uninstall(logFile, force)
	if err != nil {
		return Result{}, err
	}
	// The state file is kept: a backend the user chose survives the last
	// schedule being turned off and the next one installing the job again.
	return Result{Backend: b.Name(), Where: b.Where(), Changed: changed}, nil
}

// Status is the state of the hourly job.
type Status struct {
	Backend       string
	Source        string
	Where         string
	Installed     bool
	Content       string
	Drift         bool // installed differs from what want would install
	Binary        string
	BinaryMissing bool
	Warnings      []string
}

// CheckStatus inspects the job. want, when not nil, is what this ffm would
// install now, for drift detection.
func CheckStatus(want *Job) (Status, error) {
	b, src, err := Resolve("")
	if err != nil {
		return Status{}, err
	}
	st := Status{Backend: b.Name(), Source: src, Where: b.Where()}
	if err := b.Available(); err != nil {
		return st, fmt.Errorf("%s: %w", b.Name(), err)
	}
	content, ok, err := b.Installed()
	if err != nil {
		return st, err
	}
	st.Installed, st.Content = ok, content
	if !ok {
		if b.Name() == BackendCron {
			if current, err := ReadCrontab(); err == nil {
				if owner := LogOf(Find(current)); owner != "" {
					st.Warnings = append(st.Warnings, fmt.Sprintf(
						"your crontab holds the ffm line of another configuration (it logs to %s); it is not this configuration's job", owner))
				}
			}
		}
		return st, nil
	}
	if want != nil && b.Render(*want) != content {
		st.Drift = true
	}
	if rec, ok, _ := LoadState(); ok && rec.Backend == b.Name() && rec.FFM != "" {
		st.Binary = rec.FFM
	} else if b.Name() == BackendCron {
		st.Binary = BinaryOf(content)
	}
	if st.Binary != "" {
		if _, err := os.Stat(st.Binary); err != nil {
			st.BinaryMissing = true
		}
	}
	if b.Name() == BackendSystemd {
		if w := lingerWarning(); w != "" {
			st.Warnings = append(st.Warnings, w)
		}
	}
	return st, nil
}

// BinaryOf extracts the quoted ffm path from an installed crontab line.
func BinaryOf(line string) string {
	i := strings.Index(line, "' backup run-due")
	if i < 0 {
		return ""
	}
	j := strings.LastIndex(line[:i], " '")
	if j < 0 {
		return ""
	}
	return line[j+2 : i]
}

// envPairs are the variables the job runs with: PATH, then the passthrough
// ones set at install time.
func (j Job) envPairs() [][2]string {
	pairs := [][2]string{{"PATH", j.PathEnv}}
	for _, k := range passthroughEnv {
		if v, ok := j.Env[k]; ok {
			pairs = append(pairs, [2]string{k, v})
		}
	}
	return pairs
}

// runArgs are the arguments after the ffm binary.
func (j Job) runArgs() []string {
	return []string{"backup", "run-due", "--non-interactive", "--log", j.LogFile}
}

// run executes a command and returns its trimmed stdout and stderr.
func run(name string, args ...string) (string, string, error) {
	var stdout, stderr strings.Builder
	cmd := execx.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}
