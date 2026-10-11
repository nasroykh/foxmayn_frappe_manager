package project

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// hooksAppNameRe finds app_name = "x" (or 'x') in an app's hooks.py.
var hooksAppNameRe = regexp.MustCompile(`(?m)^app_name\s*=\s*(?:"([^"\n]*)"|'([^'\n]*)')`)

// maxHooksSize caps how much of hooks.py is read; the declaration is at the top.
const maxHooksSize = 1 << 20

// DetectApp finds the Frappe app a checkout holds: the one top-level directory
// <pkg>/ of dir (the directory holding ffm.yaml) whose hooks.py declares
// app_name = "<pkg>". It returns "" and no error when there is none, which
// makes the file configuration-only. Two or more candidates are an error.
//
// When dir has a pyproject.toml with a [project] name, that name (hyphens as
// underscores) must equal the package. The name must be a valid app name, not
// frappe, and not one of the file's own apps (listed, as ffm up takes them).
func DetectApp(dir string, listed []string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name(), "hooks.py"))
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxHooksSize))
		f.Close()
		if err != nil {
			continue
		}
		if m := hooksAppNameRe.FindSubmatch(raw); m != nil && (string(m[1]) == e.Name() || string(m[2]) == e.Name()) {
			found = append(found, e.Name())
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 0:
		return "", nil
	case 1:
	default:
		return "", fmt.Errorf("%s holds several Frappe apps (%s); ffm mounts one app per checkout", dir, strings.Join(found, ", "))
	}
	app := found[0]
	if !bench.ValidProjectAppName(app) {
		return "", fmt.Errorf("the app directory %q is not a valid app name (lowercase letters, digits and underscores, starting with a letter)", app)
	}
	if app == "frappe" {
		return "", fmt.Errorf("the app in %s is named frappe, which the bench already has", dir)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil {
		if name := pyprojectName(string(raw)); name != "" {
			if got := strings.ReplaceAll(name, "-", "_"); got != app {
				return "", fmt.Errorf("pyproject.toml names the project %q but hooks.py declares the app %q: make them agree", name, app)
			}
		}
	}
	for _, spec := range listed {
		if bench.AppSpecName(spec) == app {
			return "", fmt.Errorf("apps: lists %q, which is the app in this checkout (ffm mounts it): remove it from the list", spec)
		}
	}
	return app, nil
}

var (
	tomlSectionRe = regexp.MustCompile(`^\s*\[([^\]]*)\]\s*(?:#.*)?$`)
	tomlNameRe    = regexp.MustCompile(`^\s*name\s*=\s*(?:"([^"]*)"|'([^']*)')\s*(?:#.*)?$`)
)

// pyprojectName returns the name under [project], or "". A minimal scan, not
// a TOML parser: the [project] table's name is a plain string on one line.
func pyprojectName(toml string) string {
	inProject := false
	for _, line := range strings.Split(toml, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := tomlSectionRe.FindStringSubmatch(line); m != nil {
			inProject = strings.TrimSpace(m[1]) == "project"
			continue
		}
		if !inProject {
			continue
		}
		if m := tomlNameRe.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1] + m[2])
		}
	}
	return ""
}
