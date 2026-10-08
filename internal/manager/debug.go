package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// debugContainerPort is where debugpy listens inside the frappe container. It
// is the last port of the published web range (8000-8005), so it reaches the
// host on the bench's web port + 5 without a compose change.
const debugContainerPort = 8005

// debugMarker starts the Procfile comment that keeps the web line ffm replaced.
const debugMarker = "# ffm debug, original: "

// debugWebLine runs the dev web server under debugpy, the way Frappe's VS Code
// guide runs it: from sites/, without the reloader or threads, which confuse
// the debugger.
var debugWebLine = fmt.Sprintf("web: cd sites && /workspace/frappe-bench/env/bin/python -m debugpy --listen 0.0.0.0:%d"+
	" ../apps/frappe/frappe/utils/bench_helper.py frappe serve --port 8000 --noreload --nothreading", debugContainerPort)

// DebugStatus reports whether a dev bench's web process runs under debugpy.
func (s *Service) DebugStatus(name string) (bool, int, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return false, 0, err
	}
	raw, err := os.ReadFile(procfilePath(b))
	if err != nil {
		return false, 0, err
	}
	return strings.Contains(string(raw), debugMarker), b.WebPort + 5, nil
}

func procfilePath(b state.Bench) string {
	return filepath.Join(b.Dir, "workspace", "frappe-bench", "Procfile")
}

// setDebugWeb rewrites the Procfile's web line: on runs it under debugpy and
// keeps the original in a comment, off puts the original back. It reports
// whether the file changed.
func setDebugWeb(procfile string, on bool) (string, bool, error) {
	lines := strings.Split(procfile, "\n")
	original := -1
	web := -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, debugMarker):
			original = i
		case strings.HasPrefix(strings.TrimSpace(l), "web:"):
			web = i
		}
	}
	if web < 0 {
		return procfile, false, fmt.Errorf("the Procfile has no web: line")
	}
	if on {
		if original >= 0 {
			return procfile, false, nil
		}
		saved := debugMarker + strings.TrimSpace(lines[web])
		lines[web] = debugWebLine
		lines = append(lines[:web], append([]string{saved}, lines[web:]...)...)
		return strings.Join(lines, "\n"), true, nil
	}
	if original < 0 {
		return procfile, false, nil
	}
	lines[web] = strings.TrimPrefix(lines[original], debugMarker)
	lines = append(lines[:original], lines[original+1:]...)
	return strings.Join(lines, "\n"), true, nil
}

// Debug turns debugpy on or off for a dev bench's web process and restarts
// the dev server. On also installs debugpy into the bench's virtualenv and
// writes a VS Code attach configuration when the bench has none.
func (s *Service) Debug(name string, on bool, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	release, err := s.lockBench(name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	if !b.IsDev() {
		return fmt.Errorf("ffm debug works on dev benches only")
	}
	// debugpy runs any code it is sent. On ports bound to every interface,
	// that is anyone on the network.
	if on && b.PublishHost() == "" {
		return fmt.Errorf("bench %q publishes its ports on every interface, and debugpy runs any code it is sent;"+
			" run 'ffm reconcile %s --loopback' first", name, name)
	}
	runner := s.runnerFor(b)
	if on {
		pw.Step("Installing debugpy into the bench's virtualenv")
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && (uv pip install --python env/bin/python -q debugpy || env/bin/python -m pip install -q debugpy)"); err != nil {
			return fmt.Errorf("install debugpy: %w\n%s", err, lastLines(out, 5))
		}
	}
	raw, err := os.ReadFile(procfilePath(b))
	if err != nil {
		return err
	}
	updated, changed, err := setDebugWeb(string(raw), on)
	if err != nil {
		return err
	}
	if changed {
		if err := os.WriteFile(procfilePath(b), []byte(updated), 0o644); err != nil {
			return err
		}
		pw.Step("Restarting the dev server")
		if out, err := runner.ExecSilent("frappe", "bash", "-c", bench.DevServerRestartCmd); err != nil {
			return fmt.Errorf("restart the dev server: %w\n%s", err, lastLines(out, 5))
		}
	}
	if !on {
		pw.Printf("Debugging is off for %q; the web server runs as before.\n", name)
		return nil
	}
	wrote, err := writeLaunchJSON(b)
	if err != nil {
		pw.Printf("  warning: could not write .vscode/launch.json: %v\n", err)
	}
	pw.Printf("debugpy listens on localhost:%d (container port %d) for bench %q.\n", b.WebPort+5, debugContainerPort, name)
	if wrote {
		pw.Println("  Wrote .vscode/launch.json: in VS Code, run \"ffm: attach (host)\", or \"ffm: attach (in container)\" from a devcontainer window.")
	} else {
		pw.Println("  .vscode/launch.json exists and was left alone; attach to the port above with pathMappings")
		pw.Println("  localRoot ${workspaceFolder} → remoteRoot /workspace/frappe-bench.")
	}
	pw.Println("  The server now runs without auto-reload; 'ffm debug off " + name + "' brings it back.")
	return nil
}

// writeLaunchJSON writes .vscode/launch.json into the bench when it has none.
func writeLaunchJSON(b state.Bench) (bool, error) {
	dir := filepath.Join(b.Dir, "workspace", "frappe-bench", ".vscode")
	path := filepath.Join(dir, "launch.json")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	attach := func(name, host string, port int) map[string]any {
		return map[string]any{
			"name":    name,
			"type":    "debugpy",
			"request": "attach",
			"connect": map[string]any{"host": host, "port": port},
			"pathMappings": []map[string]string{
				{"localRoot": "${workspaceFolder}", "remoteRoot": "/workspace/frappe-bench"},
			},
			"justMyCode": false,
		}
	}
	cfg := map[string]any{
		"version": "0.2.0",
		"configurations": []any{
			attach("ffm: attach (host)", "localhost", b.WebPort+5),
			attach("ffm: attach (in container)", "localhost", debugContainerPort),
		},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(raw, '\n'), 0o644)
}
