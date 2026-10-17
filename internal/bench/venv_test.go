package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// venvRig is an env directory linked to versions/<made>/bin/<exe>, with the
// given patch releases installed.
func venvRig(t *testing.T, made, exe string, installed ...string) (env, versions string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the script runs in the Debian container: GNU sed -i")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	root := t.TempDir()
	versions = filepath.Join(root, "versions") + "/"
	for _, v := range installed {
		bin := filepath.Join(versions, v, "bin")
		os.MkdirAll(bin, 0o755)
		os.WriteFile(filepath.Join(bin, exe), nil, 0o755)
	}
	env = filepath.Join(root, "env")
	os.MkdirAll(filepath.Join(env, "bin"), 0o755)
	if err := os.Symlink(versions+made+"/bin/"+exe, filepath.Join(env, "bin", "python")); err != nil {
		t.Fatal(err)
	}
	// uv writes version_info = X.Y.Z, virtualenv version_info = X.Y.Z.final.0
	// and the stdlib version = X.Y.Z.
	cfg := "home = " + versions + made + "/bin\nversion_info = " + made + "\nversion = " + made + ".final.0\n"
	os.WriteFile(filepath.Join(env, "pyvenv.cfg"), []byte(cfg), 0o644)
	return env, versions
}

func runRepair(t *testing.T, env, versions string) (string, error) {
	t.Helper()
	out, err := exec.Command("bash", "-c", repairVenvScript(env, versions)).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestRepairVenvRelinksToTheNewestPatchOfTheSameMinor(t *testing.T) {
	env, versions := venvRig(t, "3.14.7", "python3", "3.12.15", "3.14.9", "3.14.10", "3.14.11t", "3.14.12rc1")
	out, err := runRepair(t, env, versions)
	if err != nil || out != "FFM_VENV=relinked 3.14.7 3.14.10" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if got, _ := os.Readlink(filepath.Join(env, "bin", "python")); got != versions+"3.14.10/bin/python3" {
		t.Errorf("link = %q", got)
	}
	cfg, _ := os.ReadFile(filepath.Join(env, "pyvenv.cfg"))
	if want := "home = " + versions + "3.14.10/bin\nversion_info = 3.14.10\nversion = 3.14.10.final.0\n"; string(cfg) != want {
		t.Errorf("pyvenv.cfg =\n%s\nwant\n%s", cfg, want)
	}
	// Repaired: a second run changes nothing.
	if out, err := runRepair(t, env, versions); err != nil || out != "" {
		t.Errorf("second run: out = %q, err = %v", out, err)
	}
}

func TestRepairVenvLeavesAWorkingVenvAlone(t *testing.T) {
	env, versions := venvRig(t, "3.14.7", "python3.14", "3.14.7", "3.14.8")
	if out, err := runRepair(t, env, versions); err != nil || out != "" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if got, _ := os.Readlink(filepath.Join(env, "bin", "python")); got != versions+"3.14.7/bin/python3.14" {
		t.Errorf("link = %q", got)
	}
}

// A venv directory it cannot write is a failure, not a "relinked".
func TestRepairVenvReportsAFailedRelink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through 0o555")
	}
	env, versions := venvRig(t, "3.14.7", "python3", "3.14.8")
	bin := filepath.Join(env, "bin")
	os.Chmod(bin, 0o555)
	t.Cleanup(func() { os.Chmod(bin, 0o755) })
	if out, err := runRepair(t, env, versions); err == nil || !strings.HasSuffix(out, "FFM_VENV=failed 3.14.7 3.14.8") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

// Another minor would leave every installed package unimportable: refuse.
func TestRepairVenvRefusesAnotherMinor(t *testing.T) {
	env, versions := venvRig(t, "3.12.4", "python3", "3.14.8", "3.1.5")
	out, err := runRepair(t, env, versions)
	if err == nil || out != "FFM_VENV=gone 3.12.4" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if got, _ := os.Readlink(filepath.Join(env, "bin", "python")); got != versions+"3.12.4/bin/python3" {
		t.Errorf("link changed to %q", got)
	}
}
