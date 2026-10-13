package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestRestoreFrappeBackupRunsTheWholePipelineWithoutSecretsOnArgv(t *testing.T) {
	stubRegistry(t, true)
	oldWait, oldCreate := restoreWebWait, createWebWait
	restoreWebWait, createWebWait = 0, 0
	t.Cleanup(func() { restoreWebWait, createWebWait = oldWait, oldCreate })

	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	t.Setenv("FFM_SEEDS_DIR", filepath.Join(root, "seeds"))
	t.Setenv("FFM_NO_SEED", "1")

	f := newFrappeFixture(t, "frappe@15.38.0", "erpnext@15.40.1")
	stdinFile := filepath.Join(t.TempDir(), "restore.stdin")
	const name, site = "imported", "imported.localhost"
	fb := filepath.Join(root, "benches", name, "workspace", "frappe-bench")
	siteCfg := filepath.Join(fb, "sites", site, "site_config.json")
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "' restore '", StdinTo: stdinFile},
		fakeexec.Rule{Match: "list-apps", Stdout: `{"` + site + `": ["frappe", "erpnext"]}`},
		fakeexec.Rule{Match: "new-site", Write: map[string]string{siteCfg: `{"db_name":"_new","db_password":"NEW-SITE-PW-xyz"}`}},
		fakeexec.Rule{Match: "SELECT 1", Stdout: "1\n"},
		fakeexec.Rule{Match: " run --rm ", Mkdir: []string{filepath.Join(fb, "apps")},
			Write: map[string]string{filepath.Join(fb, "sites", "apps.txt"): "frappe",
				filepath.Join(fb, "apps", "frappe", "realtime", "middlewares", "authenticate.js"): "} else if (conf.default_site) {\n",
				filepath.Join(fb, "apps", "frappe", "realtime", "utils.js"):                       "// stub\n"}},
	)
	s := New(false)

	in := f.input()
	in.Apps = []string{"https://github.com/frappe/erpnext"}
	err := s.Restore(RestoreInput{
		TargetName: name, Frappe: in, AdminPassword: secAdmin,
		WebPort: 18810, SocketIOPort: 19810, SkipSpaceCheck: true,
	}, DiscardProgress{})
	if err != nil {
		t.Fatalf("Restore: %v\ncalls:\n%s", err, strings.Join(fake.Calls(), "\n"))
	}

	rec, err := s.GetBench(name)
	if err != nil {
		t.Fatal(err)
	}
	if rec.DBType != "mariadb" || rec.Mode != "dev" || rec.FrappeBranch != "version-15" || rec.SiteName != site {
		t.Errorf("record: %+v", rec)
	}
	if len(rec.Apps) != 1 || !strings.Contains(rec.Apps[0], "erpnext") {
		t.Errorf("record apps = %v", rec.Apps)
	}
	if rec.AdminPassword != secAdmin || rec.DBPassword == "" || rec.DBPassword == "ffm123456" {
		t.Errorf("passwords: admin %q db %q", rec.AdminPassword, rec.DBPassword)
	}

	calls := fake.Calls()
	// bench init was told the dump's Frappe major version.
	if indexOfCall(calls, "--frappe-branch 'version-15'") < 0 {
		t.Errorf("bench init did not use version-15:\n%s", strings.Join(calls, "\n"))
	}
	restoreCall := indexOfCall(calls, "' restore '", "--with-public-files", "--with-private-files", "--db-root-username root --force")
	if restoreCall < 0 {
		t.Fatalf("no bench restore with both file halves:\n%s", strings.Join(calls, "\n"))
	}
	for _, want := range []string{"database.sql.gz", "public-files.tar", "private-files.tar"} {
		if !strings.Contains(calls[restoreCall], want) {
			t.Errorf("restore command lacks %s: %s", want, calls[restoreCall])
		}
	}
	// The order that matters: the data, then the reconcile steps.
	setPW := indexOfCall(calls, "set-admin-password")
	migrate := indexOfCall(calls, "migrate")
	if !(restoreCall < setPW && setPW < migrate) {
		t.Errorf("order: restore %d, set-admin-password %d, migrate %d", restoreCall, setPW, migrate)
	}

	// Nothing secret on any command line: not the admin password, the
	// site's encryption key, the old site's database password, nor ours.
	noSecretOnArgv(t, fake, secAdmin, testFernet, oldDBPass, "NEW-SITE-PW-xyz", rec.DBPassword, "bk-key-1")
	if got := readFile(t, stdinFile); got != rec.DBPassword+"\n" {
		t.Errorf("restore stdin = %q", got)
	}

	cfgRaw := readFile(t, siteCfg)
	var cfg map[string]any
	if err := json.Unmarshal([]byte(cfgRaw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["encryption_key"] != testFernet || cfg["backup_encryption_key"] != "bk-key-1" {
		t.Errorf("keys not applied: %s", cfgRaw)
	}
	// db_name / db_password stay the NEW site's; nothing of the old site came in.
	if cfg["db_password"] != "NEW-SITE-PW-xyz" || cfg["db_name"] != "_new" {
		t.Errorf("database settings were touched: %s", cfgRaw)
	}
	for _, bad := range []string{oldDBPass, "old-db.example", "old.example", "maintenance_mode"} {
		if strings.Contains(cfgRaw, bad) {
			t.Errorf("site_config.json took %q from the backup's config: %s", bad, cfgRaw)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(root, "benches", "_restore-*")); len(left) != 0 {
		t.Errorf("staging left behind: %v", left)
	}
	// The user's own files are untouched: same mode, still there.
	if st, err := os.Stat(f.db); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("source dump: %v %v", st, err)
	}
}

func TestRestoreFrappeBackupRollsBackAFailedImport(t *testing.T) {
	stubRegistry(t, true)
	oldWait, oldCreate := restoreWebWait, createWebWait
	restoreWebWait, createWebWait = 0, 0
	t.Cleanup(func() { restoreWebWait, createWebWait = oldWait, oldCreate })
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	t.Setenv("FFM_SEEDS_DIR", filepath.Join(root, "seeds"))
	t.Setenv("FFM_NO_SEED", "1")
	const name, site = "failing", "failing.localhost"
	fb := filepath.Join(root, "benches", name, "workspace", "frappe-bench")
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "' restore '", Exit: 1, Stdout: "boom: OLD-SITE-DB-PASSWORD-123"},
		fakeexec.Rule{Match: "new-site", Write: map[string]string{
			filepath.Join(fb, "sites", site, "site_config.json"): `{"db_name":"_new"}`}},
		fakeexec.Rule{Match: "SELECT 1", Stdout: "1\n"},
		fakeexec.Rule{Match: " run --rm ", Mkdir: []string{filepath.Join(fb, "apps")},
			Write: map[string]string{filepath.Join(fb, "sites", "apps.txt"): "frappe",
				filepath.Join(fb, "apps", "frappe", "realtime", "middlewares", "authenticate.js"): "} else if (conf.default_site) {\n",
				filepath.Join(fb, "apps", "frappe", "realtime", "utils.js"):                       "// stub\n"}},
	)
	f := newFrappeFixture(t)
	s := New(false)
	err := s.Restore(RestoreInput{TargetName: name, Frappe: f.input(), WebPort: 18820, SocketIOPort: 19820,
		SkipSpaceCheck: true, AdminPassword: secAdmin}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "bench restore") {
		t.Fatalf("err = %v", err)
	}
	if ok, _ := s.BenchExists(name); ok {
		t.Error("the half-imported bench stayed tracked")
	}
	if !fake.Called("down") {
		t.Errorf("containers were not torn down:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}
