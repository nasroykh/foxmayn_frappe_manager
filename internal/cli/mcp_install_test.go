package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/mcpinstall"
)

// installEnv points the install commands at a temporary home and a fixed
// binary path.
func installEnv(t *testing.T) (home, exe string) {
	t.Helper()
	home = t.TempDir()
	exe = filepath.Join(home, "bin", "ffm")
	origEnv, origExe := mcpInstallEnv, mcpInstallExecutable
	mcpInstallEnv = func() (mcpinstall.Env, error) {
		return mcpinstall.Env{Home: home, ConfigDir: filepath.Join(home, ".config"), GOOS: "linux",
			Getenv:   func(string) string { return "" },
			LookPath: func(string) (string, error) { return "", errors.New("no claude") },
		}, nil
	}
	mcpInstallExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { mcpInstallEnv, mcpInstallExecutable = origEnv, origExe })
	t.Setenv("FFM_NON_INTERACTIVE", "1")
	return home, exe
}

// runCaptured runs cmd with args and returns what it wrote to stdout and
// stderr.
func runCaptured(t *testing.T, cmd *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	outF, _ := os.Create(filepath.Join(dir, "out"))
	errF, _ := os.Create(filepath.Join(dir, "err"))
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	os.Stdout, os.Stderr = origOut, origErr
	outF.Close()
	errF.Close()
	out, _ := os.ReadFile(outF.Name())
	errOut, _ := os.ReadFile(errF.Name())
	return string(out), string(errOut), err
}

func TestMCPInstallPrintWritesNothing(t *testing.T) {
	home, exe := installEnv(t)
	out, errOut, err := runCaptured(t, newMCPInstallCmd(), "--client", "cursor", "--allow-write", "--print")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	exeJSON, _ := json.Marshal(exe)
	for _, want := range []string{`"ffm": {`, `"command": ` + string(exeJSON), `"--allow-write"`, `"type": "stdio"`} {
		if !strings.Contains(out, want) {
			t.Errorf("diff lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); !errors.Is(err, os.ErrNotExist) {
		t.Error("--print created files")
	}
}

func TestMCPInstallAndUninstallKeepOtherServers(t *testing.T) {
	home, exe := installEnv(t)
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "{\n  // mine\n  \"mcpServers\": {\n    \"frappe\": {\"command\": \"ffc\", \"args\": [\"mcp\"]}\n  }\n}\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := runCaptured(t, newMCPInstallCmd(), "--client", "cursor", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, errOut)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "// mine") || !strings.Contains(string(got), `"frappe"`) || !strings.Contains(string(got), exe) {
		t.Errorf("install lost the user's content or did not add ffm:\n%s", got)
	}
	if strings.Contains(string(got), "--allow-write") {
		t.Errorf("read-only by default, got:\n%s", got)
	}
	if backups, _ := filepath.Glob(path + ".ffm-*.bak"); len(backups) != 1 {
		t.Errorf("backups = %v", backups)
	}

	if _, errOut, err := runCaptured(t, newMCPUninstallCmd(), "--client", "cursor", "--yes"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, errOut)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), exe) || !strings.Contains(string(got), `"frappe"`) || !strings.Contains(string(got), "// mine") {
		t.Errorf("uninstall removed the wrong thing:\n%s", got)
	}
}

func TestMCPInstallUsageErrors(t *testing.T) {
	installEnv(t)
	for _, args := range [][]string{
		{},                                      // no client
		{"--client", "chatgpt"},                 // unsupported
		{"--client", "cursor", "--name", "a b"}, // bad name
		{"--client", "cursor", "--allow-prod"},  // prod without write
	} {
		_, _, err := runCaptured(t, newMCPInstallCmd(), args...)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
	// Without a terminal and without --yes, nothing is written.
	_, _, err := runCaptured(t, newMCPInstallCmd(), "--client", "cursor")
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("non-interactive install without --yes = %v", err)
	}
}

func TestIsGoRunBuild(t *testing.T) {
	tmp := t.TempDir()
	if !isGoRunBuild(filepath.Join(tmp, "go-build123", "b001", "exe", "ffm"), tmp) {
		t.Error("a go run build was not recognised")
	}
	if isGoRunBuild("/usr/local/bin/ffm", tmp) {
		t.Error("an installed binary was taken for a go run build")
	}
}
