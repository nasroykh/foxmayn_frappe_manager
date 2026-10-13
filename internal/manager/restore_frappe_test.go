package manager

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	testFernet = "3_hb1VNx0G3_rWI7r_W8rvKDt_Ib1Nb2ULzGYpthyAs="
	oldDBPass  = "OLD-SITE-DB-PASSWORD-123"
)

type tarEntry struct {
	name, link string
	typ        byte
	body       string
}

func buildTar(t *testing.T, entries []tarEntry, gzipped bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w *tar.Writer
	var zw *gzip.Writer
	if gzipped {
		zw = gzip.NewWriter(&buf)
		w = tar.NewWriter(zw)
	} else {
		w = tar.NewWriter(&buf)
	}
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			w.Write([]byte(e.body))
		}
	}
	w.Close()
	if zw != nil {
		zw.Close()
	}
	return buf.Bytes()
}

// frappeFixture writes a Frappe-style backup into a fresh directory.
type frappeFixture struct {
	dir                string
	db, pub, priv, cfg string
	dumpApps           []string
}

func newFrappeFixture(t *testing.T, dumpApps ...string) frappeFixture {
	t.Helper()
	if len(dumpApps) == 0 {
		dumpApps = []string{"frappe@15.38.0"}
	}
	dir := t.TempDir()
	f := frappeFixture{dir: dir, dumpApps: dumpApps,
		db:   filepath.Join(dir, "20260101_000000-site-database.sql.gz"),
		pub:  filepath.Join(dir, "20260101_000000-site-files.tar"),
		priv: filepath.Join(dir, "20260101_000000-site-private-files.tar"),
		cfg:  filepath.Join(dir, "20260101_000000-site-site_config_backup.json"),
	}
	must := func(p string, b []byte) {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(f.db, gzipBytes(mariaDump(dumpApps...)))
	must(f.pub, buildTar(t, entry("./site/public/files/a.txt", "hello"), false))
	must(f.priv, buildTar(t, entry("./site/private/files/b.txt", "secret"), false))
	cfg, _ := json.Marshal(map[string]any{
		"db_name": "_old", "db_password": oldDBPass, "db_host": "old-db.example", "host_name": "old.example",
		"encryption_key": testFernet, "backup_encryption_key": "bk-key-1", "maintenance_mode": 1,
	})
	must(f.cfg, cfg)
	return f
}

func entry(name, body string) []tarEntry { return []tarEntry{{name: name, body: body}} }

func (f frappeFixture) input() *FrappeBackupInput {
	return &FrappeBackupInput{DB: f.db, Files: f.pub, PrivateFiles: f.priv, Config: f.cfg}
}

func TestInspectFrappeFilesReadsEverything(t *testing.T) {
	f := newFrappeFixture(t, "frappe@15.38.0", "erpnext@15.40.1")
	got, err := inspectFrappeFiles(f.input(), DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if got.info.Engine != "mariadb" || len(got.info.Apps) != 2 || !got.dbGzip || got.publicExt != ".tar" || got.privateExt != ".tar" {
		t.Errorf("got %+v", got)
	}
	if got.encryptionKey != testFernet || got.backupEncryptionKey != "bk-key-1" {
		t.Errorf("keys: %q %q", got.encryptionKey, got.backupEncryptionKey)
	}
	if got.ignoredConfigKeys != 5 {
		t.Errorf("ignored keys = %d, want db_name, db_password, db_host, host_name, maintenance_mode", got.ignoredConfigKeys)
	}
	if got.total <= 0 {
		t.Error("no size")
	}
}

func TestInspectFrappeFilesRefusals(t *testing.T) {
	f := newFrappeFixture(t)
	write := func(name string, data []byte) string {
		p := filepath.Join(f.dir, name)
		os.WriteFile(p, data, 0o644)
		return p
	}
	cases := []struct {
		name string
		mut  func(*FrappeBackupInput)
		want string
	}{
		{"no db", func(in *FrappeBackupInput) { in.DB = "" }, "needs the database dump"},
		{"missing db", func(in *FrappeBackupInput) { in.DB = filepath.Join(f.dir, "nope.sql.gz") }, "database dump"},
		{"wrong extension", func(in *FrappeBackupInput) { in.DB = write("dump.txt", gzipBytes(mariaDump("frappe@15.0.0"))) }, "expected a .sql.gz or .sql"},
		{"empty db", func(in *FrappeBackupInput) { in.DB = write("e.sql.gz", nil) }, "is empty"},
		{"encrypted db", func(in *FrappeBackupInput) {
			in.DB = write("enc.sql.gz", append([]byte{0x8c, 0x0d, 0x04}, bytes.Repeat([]byte{0xff, 0x00}, 200)...))
		}, "GPG-encrypted"},
		{"no auth table", func(in *FrappeBackupInput) {
			in.DB = write("na.sql.gz", gzipBytes(mariaHeader+mariaAppsCreate+"INSERT INTO `tabInstalled Application` VALUES "+appRow(1, "frappe", "15.0.0")+";\n"+mariaUser))
		}, "no tabUser / __Auth"},
		{"no user table", func(in *FrappeBackupInput) {
			in.DB = write("nu.sql.gz", gzipBytes(mariaHeader+mariaAuth+mariaAppsCreate+"INSERT INTO `tabInstalled Application` VALUES "+appRow(1, "frappe", "15.0.0")+";\n"))
		}, "no tabUser / __Auth"},
		{"no apps table", func(in *FrappeBackupInput) {
			in.DB = write("nt.sql.gz", gzipBytes(mariaHeader+mariaAuth+mariaUser))
		}, "lists no installed apps"},
		{"truncated gzip", func(in *FrappeBackupInput) {
			full := gzipBytes(mariaDump("frappe@15.0.0") + strings.Repeat("-- filler\n", 50000))
			in.DB = write("tr.sql.gz", full[:len(full)/2])
		}, "truncated"},
		{"files not a tar", func(in *FrappeBackupInput) { in.Files = write("x.tar", []byte("not a tar at all, just text")) }, "not a tar"},
		{"files wrong ext", func(in *FrappeBackupInput) { in.Files = write("x.zip", []byte("zip")) }, "expected a .tar"},
		{"files path traversal", func(in *FrappeBackupInput) {
			in.Files = write("evil.tar", buildTar(t, entry("../../etc/cron.d/x", "boom"), false))
		}, "outside the site"},
		{"files absolute path", func(in *FrappeBackupInput) {
			in.PrivateFiles = write("abs.tar", buildTar(t, entry("/etc/passwd", "x"), false))
		}, "outside the site"},
		{"files symlink out", func(in *FrappeBackupInput) {
			in.Files = write("sl.tar", buildTar(t, []tarEntry{{name: "site/public/l", typ: tar.TypeSymlink, link: "../../../../etc"}}, false))
		}, "points outside"},
		{"files truncated", func(in *FrappeBackupInput) {
			full := buildTar(t, entry("site/public/files/a", strings.Repeat("z", 100000)), false)
			in.Files = write("cut.tar", full[:len(full)/2])
		}, "truncated"},
		{"tgz that is not gzip", func(in *FrappeBackupInput) { in.Files = write("x.tgz", buildTar(t, entry("a", "b"), false)) }, "not a gzip"},
		{"config not json", func(in *FrappeBackupInput) { in.Config = write("c.json", []byte("[1,2")) }, "not a JSON object"},
		{"config bad key", func(in *FrappeBackupInput) { in.Config = write("k.json", []byte(`{"encryption_key":"short"}`)) }, "Fernet"},
		{"config key not string", func(in *FrappeBackupInput) { in.Config = write("k2.json", []byte(`{"encryption_key":12}`)) }, "encryption_key"},
	}
	for _, tt := range cases {
		in := f.input()
		tt.mut(in)
		if _, err := inspectFrappeFiles(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}

	// Accepted variants.
	ok := f.input()
	ok.Files = write("a.tgz", buildTar(t, entry("site/public/files/a", "x"), true))
	ok.PrivateFiles = write("b.tar.gz", buildTar(t, entry("site/private/files/a", "x"), true))
	ok.Config = ""
	got, err := inspectFrappeFiles(ok, DiscardProgress{})
	if err != nil || got.publicExt != ".tgz" || got.privateExt != ".tgz" || got.encryptionKey != "" {
		t.Errorf("tgz variants: %+v %v", got, err)
	}
	only := &FrappeBackupInput{DB: f.db}
	if got, err := inspectFrappeFiles(only, DiscardProgress{}); err != nil || got.publicPath != "" {
		t.Errorf("db only: %+v %v", got, err)
	}
}

func TestConfigNeverContributesDatabaseSettings(t *testing.T) {
	f := newFrappeFixture(t)
	got, err := inspectFrappeFiles(f.input(), DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	// frappeFiles has no field that could carry them; the manifest built from it
	// must hold nothing but the two keys.
	raw, _ := json.Marshal(got)
	for _, bad := range []string{oldDBPass, "old-db.example", "old.example", "_old"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("%q survived into the import state: %s", bad, raw)
		}
	}
}

func TestChooseDBEngine(t *testing.T) {
	tests := []struct{ dump, req, want, err string }{
		{"mariadb", "", "mariadb", ""},
		{"postgres", "", "postgres", ""},
		{"postgres", "postgres", "postgres", ""},
		{"mariadb", "postgres", "", "cannot be loaded"},
		{"postgres", "mariadb", "", "cannot be loaded"},
		{"", "postgres", "postgres", ""},
		{"", "", "", "pass --db-type"},
		{"mariadb", "mysql", "", "invalid --db-type"},
	}
	for _, tt := range tests {
		got, err := chooseDBEngine(tt.dump, tt.req)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%+v: err = %v", tt, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%+v: %q %v", tt, got, err)
		}
	}
}

func TestChooseFrappeBranch(t *testing.T) {
	tests := []struct {
		ver, dumpBranch, req string
		repo                 bool
		want                 string
		problem              bool
	}{
		{"15.38.0", "", "", false, "version-15", false},
		{"16.0.1", "", "", false, "version-16", false},
		{"", "", "", false, "version-16", false},
		{"16.51.1", "version-16", "", false, "version-16", false},
		{"15.38.0", "develop", "", false, "version-15", false},
		{"15.38.0", "", "version-15", false, "version-15", false},
		{"15.38.0", "", "version-16", false, "version-16", true},
		{"15.38.0", "", "version-16", true, "version-16", false},
		{"15.38.0", "", "develop", false, "develop", false},
	}
	for _, tt := range tests {
		got, problems := chooseFrappeBranch(tt.ver, tt.dumpBranch, tt.req, tt.repo)
		if got != tt.want || (len(problems) > 0) != tt.problem {
			t.Errorf("%+v: %q %v", tt, got, problems)
		}
	}
}

func TestPlanDumpApps(t *testing.T) {
	info := dumpInfo{Apps: []dumpApp{{Name: "frappe"}, {Name: "erpnext"}, {Name: "hrms"}, {Name: "my_app"}}}

	_, installed, problems := planDumpApps(info, nil, nil)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "erpnext, hrms, my_app") ||
		!strings.Contains(problems[0].Message, "--apps erpnext,hrms,my_app") {
		t.Fatalf("problems = %+v", problems)
	}
	if strings.Join(installed, ",") != "frappe,erpnext,hrms,my_app" {
		t.Errorf("installed = %v", installed)
	}

	specs, _, problems := planDumpApps(info, []string{"erpnext", " hrms ", "git@github.com:acme/my_app.git@main"},
		nil)
	if len(problems) != 0 || len(specs) != 3 {
		t.Errorf("by --apps: %+v %v", problems, specs)
	}
	_, _, problems = planDumpApps(info, []string{"erpnext", "hrms"}, map[string]string{"my_app": "https://x/y.git"})
	if len(problems) != 0 {
		t.Errorf("by --app: %+v", problems)
	}
	_, _, problems = planDumpApps(info, []string{"erpnext"}, map[string]string{"my_app": "https://x/y.git"})
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "hrms") || strings.Contains(problems[0].Message, "my_app") {
		t.Errorf("partial: %+v", problems)
	}

	// An app name from a hostile dump never reaches a shell.
	hostile := dumpInfo{Apps: []dumpApp{{Name: "frappe"}, {Name: "x'; rm -rf / #"}}}
	_, _, problems = planDumpApps(hostile, nil, nil)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "not a Frappe app name") {
		t.Errorf("hostile: %+v", problems)
	}
}

// ---- the whole entry point, without Docker ----

// frappeRig isolates the directories and fakes docker.
func frappeRig(t *testing.T, rules ...fakeexec.Rule) (*Service, *fakeexec.Fake) {
	t.Helper()
	isolatedDirs(t)
	t.Setenv("FFM_NO_SEED", "1")
	fake := fakeexec.Install(t, rules...)
	return New(false), fake
}

func noBenchBuilt(t *testing.T, fake *fakeexec.Fake, name string) {
	t.Helper()
	if fake.Called("compose -p") || fake.Called(" build") {
		t.Errorf("docker compose ran:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("FFM_BENCHES_DIR"), name)); err == nil {
		t.Errorf("bench directory %s was created", name)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("FFM_BENCHES_DIR"), "_restore-*")); len(left) != 0 {
		t.Errorf("staging left behind: %v", left)
	}
}

func TestRestoreFrappeBackupRefusesBeforeBuildingAnything(t *testing.T) {
	stubRegistry(t, true)
	f := newFrappeFixture(t, "frappe@15.38.0", "erpnext@15.40.1")
	pg := newFrappeFixture(t)
	_ = pg

	tests := []struct {
		name string
		in   func() RestoreInput
		want string
	}{
		{"no name", func() RestoreInput { return RestoreInput{Frappe: f.input()} }, "give the new bench a name"},
		{"reserved name", func() RestoreInput { return RestoreInput{TargetName: "list", Frappe: f.input()} }, "reserved"},
		{"an archive argument too", func() RestoreInput {
			return RestoreInput{TargetName: "x", Archive: "a.ffm.tar", Frappe: f.input()}
		}, "not an archive argument"},
		{"apps the dump lists but nobody sourced", func() RestoreInput {
			return RestoreInput{TargetName: "fb1", Frappe: f.input(), SkipSpaceCheck: true}
		}, "--apps erpnext"},
		{"db type mismatch", func() RestoreInput {
			in := f.input()
			in.DBType, in.Apps = "postgres", []string{"erpnext"}
			return RestoreInput{TargetName: "fb2", Frappe: in, SkipSpaceCheck: true}
		}, "cannot be loaded into postgres"},
		{"branch of another major", func() RestoreInput {
			in := f.input()
			in.FrappeBranch, in.Apps = "version-16", []string{"erpnext"}
			return RestoreInput{TargetName: "fb3", Frappe: in, SkipSpaceCheck: true}
		}, "different major version"},
		{"prod without a domain", func() RestoreInput {
			in := f.input()
			in.Mode, in.Apps = "prod", []string{"erpnext"}
			return RestoreInput{TargetName: "fb4", Frappe: in, SkipSpaceCheck: true, NoSSL: true}
		}, "records no domain"},
		{"bad mode", func() RestoreInput {
			in := f.input()
			in.Mode = "staging"
			return RestoreInput{TargetName: "fb5", Frappe: in}
		}, "invalid --mode"},
		{"no encryption key", func() RestoreInput {
			in := f.input()
			in.Config, in.Apps = "", []string{"erpnext"}
			return RestoreInput{TargetName: "fb6", Frappe: in, SkipSpaceCheck: true}
		}, "encryption_key"},
		{"bad override", func() RestoreInput {
			in := f.input()
			in.Apps = []string{"erpnext"}
			return RestoreInput{TargetName: "fb7", Frappe: in, SkipSpaceCheck: true, AppOverrides: []string{"nonsense"}}
		}, "expected <app>=<git-url>"},
		{"override for an app the dump lacks", func() RestoreInput {
			in := f.input()
			in.Apps = []string{"erpnext"}
			return RestoreInput{TargetName: "fb8", Frappe: in, SkipSpaceCheck: true, AppOverrides: []string{"ghost=https://x/y.git"}}
		}, "neither installs nor records"},
	}
	for _, tt := range tests {
		s, fake := frappeRig(t)
		in := tt.in()
		err := s.Restore(in, DiscardProgress{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
		noBenchBuilt(t, fake, in.TargetName)
	}

	// A taken name, found by the same gate as an archive restore.
	s, _ := frappeRig(t)
	if err := s.AddBench(state.Bench{Name: "taken", Mode: "dev"}); err != nil {
		t.Fatal(err)
	}
	in := f.input()
	in.Apps = []string{"erpnext"}
	if err := s.Restore(RestoreInput{TargetName: "taken", Frappe: in, SkipSpaceCheck: true}, DiscardProgress{}); err == nil ||
		!strings.Contains(err.Error(), "taken") {
		t.Errorf("existing bench: %v", err)
	}
}

func TestRestoreFrappeBackupDryRun(t *testing.T) {
	stubRegistry(t, true)
	f := newFrappeFixture(t, "frappe@15.38.0", "erpnext@15.40.1")
	s, fake := frappeRig(t)
	in := f.input()
	in.Apps = []string{"erpnext"}
	pw := &BufferProgress{}
	if err := s.Restore(RestoreInput{TargetName: "dry", Frappe: in, DryRun: true, SkipSpaceCheck: true}, pw); err != nil {
		t.Fatalf("%v\n%v", err, pw.Lines)
	}
	out := strings.Join(pw.Lines, "\n")
	for _, want := range []string{"Frappe backup files are valid", "dry.localhost", "frappe 15.38.0", "frappe, erpnext", "Public files:  yes", "Encryption key present: yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{testFernet, oldDBPass} {
		if strings.Contains(out, secret) {
			t.Errorf("the plan prints a secret: %s", secret)
		}
	}
	noBenchBuilt(t, fake, "dry")
}

// stubRegistry replaces the GitHub lookup of bare app names.
func stubRegistry(t *testing.T, found bool) {
	t.Helper()
	old := githubRegistryLookup
	githubRegistryLookup = func(context.Context, string, string) (bool, error) { return found, nil }
	t.Cleanup(func() { githubRegistryLookup = old })
}

func TestRestoreFrappeBackupNeedsTheAppsItReads(t *testing.T) {
	// A bare name GitHub does not know is refused before Create, like for an
	// archive.
	stubRegistry(t, false)
	f := newFrappeFixture(t, "frappe@15.38.0", "erpnext@15.40.1")
	s, fake := frappeRig(t)
	in := f.input()
	in.Apps = []string{"erpnext"}
	err := s.Restore(RestoreInput{TargetName: "bare", Frappe: in, DryRun: true, SkipSpaceCheck: true}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "has no source") {
		t.Errorf("err = %v", err)
	}
	noBenchBuilt(t, fake, "bare")
}
