package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

// projectDir writes an ffm.yaml with hooks and tooling into a fresh
// repository and makes it the working directory.
func projectDir(t *testing.T) string {
	t.Helper()
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	t.Setenv("FFM_BENCHES_DIR", t.TempDir())
	t.Setenv("FFM_NON_INTERACTIVE", "1")
	dir := filepath.Join(t.TempDir(), "shop")
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	doc := "version: 1\nhooks:\n  post_create: [\"echo hi\"]\ntooling:\n  lint: { cmd: ruff check ., description: Lint the app }\n"
	if err := os.WriteFile(filepath.Join(dir, "ffm.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func TestUpRefusesUntrustedCommands(t *testing.T) {
	projectDir(t)
	fake := fakeexec.Install(t)
	_, _, err := runCaptured(t, newUpCmd())
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "ffm trust") {
		t.Fatalf("up with an untrusted file = %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("work started: %v", fake.Calls())
	}
}

func TestTrustThenRun(t *testing.T) {
	dir := projectDir(t)
	fakeexec.Install(t)
	if _, _, err := runCaptured(t, newRunCmd(), "lint"); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("run before trust = %v", err)
	}
	out, errOut, err := runCaptured(t, newTrustCmd(), "--yes")
	if err != nil || !strings.Contains(errOut, "ffm run lint: ruff check .") || !strings.Contains(out, "Trusted") {
		t.Fatalf("trust: %v\nout %s\nerr %s", err, out, errOut)
	}
	out, _, err = runCaptured(t, newRunCmd())
	if err != nil || !strings.Contains(out, "lint") || !strings.Contains(out, "Lint the app") {
		t.Errorf("tool list = %q, %v", out, err)
	}
	if _, _, err := runCaptured(t, newRunCmd(), "nope"); err == nil {
		t.Error("an unknown tool ran")
	}
	// Trusted, but no bench yet.
	if _, _, err := runCaptured(t, newRunCmd(), "lint"); err == nil || !strings.Contains(err.Error(), "ffm up") {
		t.Errorf("run without a bench = %v", err)
	}
	// Editing the file withdraws the trust.
	f := filepath.Join(dir, "ffm.yaml")
	raw, _ := os.ReadFile(f)
	os.WriteFile(f, append(raw, []byte("# edited\n")...), 0o644)
	if _, _, err := runCaptured(t, newRunCmd(), "lint"); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("run after an edit = %v", err)
	}
}
