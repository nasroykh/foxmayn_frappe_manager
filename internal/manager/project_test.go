package manager

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/project"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func projectFile(t *testing.T, name, path string, apps ...string) project.File {
	t.Helper()
	f, err := project.Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.Name, f.Path, f.Apps = name, path, apps
	return f
}

func TestUpRunningBenchReportsMissingApps(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	s, _ := newTestBench(t, state.Bench{Name: "shop", Mode: "dev", SiteName: "shop.localhost", ProjectFile: "/src/shop/ffm.yaml",
		Apps: []string{"erpnext@version-16"}})
	res, err := s.Up(UpInput{File: projectFile(t, "shop", "/src/shop/ffm.yaml", "erpnext", "hrms@version-16", "https://github.com/acme/shop-theme.git")}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "running" || strings.Join(res.MissingApps, ",") != "hrms@version-16,https://github.com/acme/shop-theme.git" {
		t.Errorf("Up = %+v", res)
	}
}

func TestUpRefusesABenchItDidNotCreate(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	for _, owner := range []string{"", "/elsewhere/ffm.yaml"} {
		s, _ := newTestBench(t, state.Bench{Name: "shop", Mode: "dev", SiteName: "shop.localhost", ProjectFile: owner})
		_, err := s.Up(UpInput{File: projectFile(t, "shop", "/src/shop/ffm.yaml")}, DiscardProgress{})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("owner %q: err = %v", owner, err)
		}
	}
}

func TestRunHooksAndTool(t *testing.T) {
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning}, fakeexec.Rule{Match: "boom", Exit: 3})
	s, b := newTestBench(t, state.Bench{Name: "shop", Mode: "dev", SiteName: "shop.localhost"})
	var out bytes.Buffer
	if err := s.RunHooks(b, "post_create", []string{"bench --site $SITE migrate", "echo two"}, &out, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls(), "\n")
	if !strings.Contains(calls, "export SITE='shop.localhost' BENCH='shop'; bench --site $SITE migrate") || !strings.Contains(calls, "; echo two") {
		t.Errorf("hook calls:\n%s", calls)
	}
	err := s.RunHooks(b, "post_update", []string{"boom", "never"}, &out, DiscardProgress{})
	if err == nil || strings.Contains(strings.Join(fake.Calls(), "\n"), "never") {
		t.Errorf("hooks went on after a failure: %v", err)
	}
	if err := s.RunTool(b, "ruff check apps/shop", []string{"--fix", "a b; rm -rf /"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fake.Calls(), "\n"), "ruff check apps/shop '--fix' 'a b; rm -rf /'") {
		t.Errorf("tool args not quoted:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestAppSpecName(t *testing.T) {
	for spec, want := range map[string]string{
		"erpnext":                         "erpnext",
		"hrms@version-16":                 "hrms",
		"https://github.com/a/b-c":        "b_c",
		"https://github.com/a/x.git@main": "x",
		"git@github.com:a/y.git@dev":      "y",
	} {
		if got := appSpecName(spec); got != want {
			t.Errorf("appSpecName(%q) = %q, want %q", spec, got, want)
		}
	}
}
