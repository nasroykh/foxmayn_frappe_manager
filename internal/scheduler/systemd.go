package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// systemdBackend runs the job from a user timer: <name>.timer starts the
// oneshot <name>.service every hour, under `systemctl --user`.
type systemdBackend struct{}

func (systemdBackend) Name() string { return BackendSystemd }

func (systemdBackend) Where() string {
	dir, err := systemdDir()
	if err != nil {
		return "the systemd user manager"
	}
	return filepath.Join(dir, JobName()+".timer") + " (systemd user timer)"
}

func (systemdBackend) Available() error {
	if _, err := lookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl is not installed: %w", err)
	}
	if _, stderr, err := run("systemctl", "--user", "show-environment"); err != nil {
		msg := stderr
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("the systemd user manager is not running (%s)", msg)
	}
	return nil
}

// systemdDir is where user units live: $XDG_CONFIG_HOME/systemd/user.
func systemdDir() (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func systemdPaths() (service, timer string, err error) {
	dir, err := systemdDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, JobName()+".service"), filepath.Join(dir, JobName()+".timer"), nil
}

// unitValue escapes s for a double-quoted unit-file word. systemd expands
// %-specifiers nearly everywhere; $ is expanded in Exec lines only, so execLine
// also doubles it.
func unitValue(s string, exec bool) string {
	r := []string{`\`, `\\`, `"`, `\"`, "%", "%%", "\n", `\n`}
	if exec {
		r = append(r, "$", "$$")
	}
	return strings.NewReplacer(r...).Replace(s)
}

func (systemdBackend) units(j Job) (service, timer string) {
	var s strings.Builder
	s.WriteString("[Unit]\nDescription=ffm scheduled backups (backup run-due)\n\n[Service]\nType=oneshot\n")
	for _, kv := range j.envPairs() {
		fmt.Fprintf(&s, "Environment=\"%s=%s\"\n", kv[0], unitValue(kv[1], false))
	}
	words := append([]string{j.FFM}, j.runArgs()...)
	for i, w := range words {
		words[i] = `"` + unitValue(w, true) + `"`
	}
	fmt.Fprintf(&s, "ExecStart=%s\n", strings.Join(words, " "))
	logFile := strings.ReplaceAll(strings.ReplaceAll(j.LogFile, "%", "%%"), "\n", " ")
	fmt.Fprintf(&s, "StandardOutput=append:%s\nStandardError=append:%s\n", logFile, logFile)

	timer = fmt.Sprintf("[Unit]\nDescription=Hourly ffm scheduled backups\n\n[Timer]\nOnCalendar=*-*-* *:%02d:00\nPersistent=true\nUnit=%s.service\n\n[Install]\nWantedBy=timers.target\n",
		j.Minute, JobName())
	return s.String(), timer
}

func systemdText(servicePath, service, timerPath, timer string) string {
	return "# " + servicePath + "\n" + service + "\n# " + timerPath + "\n" + timer
}

func (b systemdBackend) Render(j Job) string {
	sp, tp, err := systemdPaths()
	if err != nil {
		sp, tp = JobName()+".service", JobName()+".timer"
	}
	service, timer := b.units(j)
	return systemdText(sp, service, tp, timer)
}

func (b systemdBackend) Print(j Job) string {
	return b.Render(j) + fmt.Sprintf("\n# Then:\n#   systemctl --user daemon-reload\n#   systemctl --user enable --now %s.timer\n", JobName())
}

func (systemdBackend) Installed() (string, bool, error) {
	sp, tp, err := systemdPaths()
	if err != nil {
		return "", false, err
	}
	service, serr := os.ReadFile(sp)
	timer, terr := os.ReadFile(tp)
	if errors.Is(serr, os.ErrNotExist) && errors.Is(terr, os.ErrNotExist) {
		return "", false, nil
	}
	if serr != nil && !errors.Is(serr, os.ErrNotExist) {
		return "", false, serr
	}
	if terr != nil && !errors.Is(terr, os.ErrNotExist) {
		return "", false, terr
	}
	return systemdText(sp, string(service), tp, string(timer)), true, nil
}

func (b systemdBackend) Install(j Job, force bool) (bool, error) {
	sp, tp, err := systemdPaths()
	if err != nil {
		return false, err
	}
	service, timer := b.units(j)
	c1, err := writeIfChanged(sp, []byte(service), 0o644, 0o755)
	if err != nil {
		return false, err
	}
	c2, err := writeIfChanged(tp, []byte(timer), 0o644, 0o755)
	if err != nil {
		return false, err
	}
	if !c1 && !c2 {
		return false, nil
	}
	if _, stderr, err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return true, fmt.Errorf("systemctl --user daemon-reload: %w: %s", err, stderr)
	}
	if _, stderr, err := run("systemctl", "--user", "enable", "--now", JobName()+".timer"); err != nil {
		return true, fmt.Errorf("systemctl --user enable --now %s.timer: %w: %s", JobName(), err, stderr)
	}
	return true, nil
}

// Uninstall needs no ownership check: the unit name carries a hash of the
// configuration directory, so another configuration's units are never these.
func (systemdBackend) Uninstall(logFile string, force bool) (bool, error) {
	sp, tp, err := systemdPaths()
	if err != nil {
		return false, err
	}
	_, serr := os.Stat(sp)
	_, terr := os.Stat(tp)
	if serr != nil && terr != nil {
		return false, nil
	}
	// Best effort: the files are removed even when the manager is down.
	run("systemctl", "--user", "disable", "--now", JobName()+".timer")
	for _, p := range []string{tp, sp} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, err
		}
	}
	run("systemctl", "--user", "daemon-reload")
	return true, nil
}

// lingerWarning explains that user timers stop at logout without linger. It
// never enables it: that is a system-wide setting for the user to choose.
func lingerWarning() string {
	if _, err := lookPath("loginctl"); err != nil {
		return ""
	}
	out, _, err := run("loginctl", "show-user", strconv.Itoa(uid()), "-p", "Linger")
	if err != nil || strings.TrimSpace(out) != "Linger=no" {
		return ""
	}
	return "systemd stops your user timers when you log out (linger is off), so backups only run while you are logged in. " +
		"To keep them running, enable linger yourself: sudo loginctl enable-linger $USER"
}
