package scheduler

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// launchdBackend runs the job as a per-user launch agent that fires at one
// minute past every hour.
type launchdBackend struct{}

func (launchdBackend) Name() string { return BackendLaunchd }

func (launchdBackend) Available() error {
	if _, err := lookPath("launchctl"); err != nil {
		return fmt.Errorf("launchctl is not installed (launchd exists on macOS only): %w", err)
	}
	return nil
}

func plistPath() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel()+".plist"), nil
}

func (launchdBackend) Where() string {
	if p, err := plistPath(); err == nil {
		return p + " (launchd agent)"
	}
	return "a launchd agent"
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (launchdBackend) Render(j Job) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlText(launchdLabel()) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlText(j.FFM) + `</string>
`)
	for _, a := range j.runArgs() {
		b.WriteString("\t\t<string>" + xmlText(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, kv := range j.envPairs() {
		b.WriteString("\t\t<key>" + xmlText(kv[0]) + "</key>\n\t\t<string>" + xmlText(kv[1]) + "</string>\n")
	}
	fmt.Fprintf(&b, "\t</dict>\n\t<key>StartCalendarInterval</key>\n\t<dict>\n\t\t<key>Minute</key>\n\t\t<integer>%d</integer>\n\t</dict>\n", j.Minute)
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlText(j.LogFile) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlText(j.LogFile) + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func (b launchdBackend) Print(j Job) string {
	p, err := plistPath()
	if err != nil {
		p = launchdLabel() + ".plist"
	}
	return fmt.Sprintf("%s<!-- Save the plist above as %s, then run:\n     launchctl bootstrap gui/%d %s -->\n",
		b.Render(j), p, uid(), p)
}

func (launchdBackend) Installed() (string, bool, error) {
	p, err := plistPath()
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

func (b launchdBackend) Install(j Job, force bool) (bool, error) {
	p, err := plistPath()
	if err != nil {
		return false, err
	}
	changed, err := writeIfChanged(p, []byte(b.Render(j)), 0o644, 0o755)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	domain := "gui/" + strconv.Itoa(uid())
	// bootout fails when the agent is not loaded yet; that is the normal case.
	run("launchctl", "bootout", domain+"/"+launchdLabel())
	if _, stderr, err := run("launchctl", "bootstrap", domain, p); err != nil {
		return true, fmt.Errorf("launchctl bootstrap %s %s: %w: %s", domain, p, err, stderr)
	}
	return true, nil
}

// Uninstall needs no ownership check: the label carries a hash of the
// configuration directory.
func (launchdBackend) Uninstall(logFile string, force bool) (bool, error) {
	p, err := plistPath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		return false, nil
	}
	run("launchctl", "bootout", "gui/"+strconv.Itoa(uid())+"/"+launchdLabel())
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	return true, nil
}
