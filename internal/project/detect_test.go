package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appRepo(t *testing.T, pkg, hooks string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ffm.yaml"), "version: 1\n")
	writeFile(t, filepath.Join(dir, pkg, "hooks.py"), hooks)
	return dir
}

func TestDetectAppFindsThePackage(t *testing.T) {
	for _, hooks := range []string{
		"app_name = \"my_app\"\napp_title = \"My App\"\n",
		"app_name = 'my_app'\n",
		"# header\napp_name   =   \"my_app\"  # trailing\n",
	} {
		dir := appRepo(t, "my_app", hooks)
		got, err := DetectApp(dir, nil)
		if err != nil || got != "my_app" {
			t.Errorf("hooks %q: got %q, %v", hooks, got, err)
		}
	}
}

func TestDetectAppConfigOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ffm.yaml"), "version: 1\n")
	writeFile(t, filepath.Join(dir, "docs", "index.md"), "x")
	writeFile(t, filepath.Join(dir, "node_modules", "hooks.py"), "app_name = \"other\"\n") // name differs from dir
	writeFile(t, filepath.Join(dir, ".hidden", "hooks.py"), "app_name = \".hidden\"\n")
	got, err := DetectApp(dir, nil)
	if err != nil || got != "" {
		t.Errorf("got %q, %v; want no app", got, err)
	}
}

func TestDetectAppIgnoresAMismatchedDeclaration(t *testing.T) {
	// hooks.py in dir "pkg" that declares another app is not this package.
	dir := appRepo(t, "pkg", "app_name = \"other\"\n")
	if got, err := DetectApp(dir, nil); err != nil || got != "" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestDetectAppSeveralCandidates(t *testing.T) {
	dir := appRepo(t, "app_one", "app_name = \"app_one\"\n")
	writeFile(t, filepath.Join(dir, "app_two", "hooks.py"), "app_name = \"app_two\"\n")
	_, err := DetectApp(dir, nil)
	if err == nil || !strings.Contains(err.Error(), "app_one, app_two") {
		t.Errorf("err = %v", err)
	}
}

func TestDetectAppPyproject(t *testing.T) {
	dir := appRepo(t, "my_app", "app_name = \"my_app\"\n")

	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[build-system]\nname = \"zzz\"\n\n[project]\nname = \"my-app\"\nversion = \"1\"\n")
	if got, err := DetectApp(dir, nil); err != nil || got != "my_app" {
		t.Errorf("matching name: %q, %v", got, err)
	}

	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname = \"other-app\"\n")
	if _, err := DetectApp(dir, nil); err == nil || !strings.Contains(err.Error(), "other-app") {
		t.Errorf("mismatch accepted: %v", err)
	}

	// No [project] name (Poetry, or none): nothing to cross-check.
	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[tool.poetry]\nname = \"whatever\"\n")
	if got, err := DetectApp(dir, nil); err != nil || got != "my_app" {
		t.Errorf("no [project] name: %q, %v", got, err)
	}
}

func TestDetectAppNameRules(t *testing.T) {
	for _, pkg := range []string{"MyApp", "9app", "my-app"} {
		dir := appRepo(t, pkg, "app_name = \""+pkg+"\"\n")
		if _, err := DetectApp(dir, nil); err == nil {
			t.Errorf("%q accepted", pkg)
		}
	}
	dir := appRepo(t, "frappe", "app_name = \"frappe\"\n")
	if _, err := DetectApp(dir, nil); err == nil || !strings.Contains(err.Error(), "frappe") {
		t.Errorf("frappe accepted: %v", err)
	}
}

func TestDetectAppCollidesWithListedApps(t *testing.T) {
	dir := appRepo(t, "my_app", "app_name = \"my_app\"\n")
	for _, spec := range []string{"my_app", "my-app@develop", "https://github.com/acme/my_app.git@main", "git@github.com:acme/my-app.git"} {
		if _, err := DetectApp(dir, []string{"erpnext", spec}); err == nil {
			t.Errorf("collision with %q not detected", spec)
		}
	}
	if got, err := DetectApp(dir, []string{"erpnext", "hrms@version-16"}); err != nil || got != "my_app" {
		t.Errorf("unrelated apps: %q, %v", got, err)
	}
}
