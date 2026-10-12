package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	secAdmin = "Adm1n-Sentinel-77"
	secDB    = "DbRoot-Sentinel-88"
	secToken = "ghp_TokenSentinel99"
	secKey   = "BackupKeySentinel66="
)

// noSecretOnArgv fails for every recorded command line that contains a secret.
func noSecretOnArgv(t *testing.T, fake *fakeexec.Fake, secrets ...string) {
	t.Helper()
	for _, c := range fake.Calls() {
		for _, s := range secrets {
			if s != "" && strings.Contains(c, s) {
				t.Errorf("secret %q reached a command line: %s", s, c)
			}
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateKeepsSecretsOffArgv(t *testing.T) {
	dir := t.TempDir()
	siteIn := filepath.Join(dir, "newsite.stdin")
	r := newMountRig(t, "sec", fakeexec.Rule{Match: "new-site", StdinTo: siteIn})
	in := r.createInput("sec")
	in.AdminPassword, in.DBPassword, in.GithubToken = secAdmin, secDB, secToken
	in.ProjectApp, in.ProjectAppPath = "", ""
	if err := r.s.Create(in, DiscardProgress{}); err != nil {
		t.Fatalf("Create: %v\ncalls:\n%s", err, strings.Join(r.fake.Calls(), "\n"))
	}
	noSecretOnArgv(t, r.fake, secAdmin, secDB, secToken)
	// Root password first (the database connection), Administrator after.
	if got := readFile(t, siteIn); got != secDB+"\n"+secAdmin+"\n" {
		t.Errorf("new-site stdin = %q", got)
	}
	if !r.fake.Called("new-site 'sec.localhost' --no-mariadb-socket") || !r.fake.Called(" run --rm -T frappe") {
		t.Errorf("unexpected commands:\n%s", strings.Join(r.fake.Calls(), "\n"))
	}
	// The password lives in the secret file, not in the compose file.
	if got := readFile(t, bench.DBRootSecretPath(r.benchDir("sec"))); got != secDB {
		t.Errorf("secret file = %q", got)
	}
	if up := r.snapshot(t, "up.yml"); strings.Contains(up, secDB) {
		t.Error("the compose file carries the database password")
	}
}

func TestCreatePostgresKeepsSecretsOffArgv(t *testing.T) {
	siteIn := filepath.Join(t.TempDir(), "newsite.stdin")
	r := newMountRig(t, "pg", fakeexec.Rule{Match: "new-site", StdinTo: siteIn},
		fakeexec.Rule{Match: "pg_isready", Stdout: "/var/run/postgresql:5432 - accepting connections\n"})
	in := r.createInput("pg")
	in.DBType, in.AdminPassword, in.DBPassword = "postgres", secAdmin, secDB
	in.ProjectApp, in.ProjectAppPath = "", ""
	if err := r.s.Create(in, DiscardProgress{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	noSecretOnArgv(t, r.fake, secAdmin, secDB)
	if !r.fake.Called("--db-type postgres --db-root-username postgres") || readFile(t, siteIn) != secDB+"\n"+secAdmin+"\n" {
		t.Errorf("postgres new-site: stdin %q\n%s", readFile(t, siteIn), strings.Join(r.fake.Calls(), "\n"))
	}
}

func TestRestoreKeepsSecretsOffArgv(t *testing.T) {
	rootIn := filepath.Join(t.TempDir(), "restore.stdin")
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "bench --site", StdinTo: rootIn})
	s, b := newTestBench(t, state.Bench{Name: "rr", Mode: "dev", SiteName: "rr.localhost", DBPassword: secDB, AdminPassword: secAdmin})
	staging := t.TempDir()
	for _, f := range []string{"db/database.sql.gz", "files/public-files.tgz", "files/private-files.tgz"} {
		p := filepath.Join(staging, filepath.FromSlash(archive.Prefix+"/"+f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o600)
	}
	m := Manifest{Members: []archive.Member{{Path: archive.Prefix + "/db/database.sql.gz", Encoding: archive.EncodingGPG}}}
	m.Secrets.BackupEncryptionKey = secKey
	in := RestoreInput{EncryptionKey: secKey}
	if err := s.runBenchRestore(s.runnerFor(b), b, m, in, staging, true, DiscardProgress{}); err != nil {
		t.Fatalf("runBenchRestore: %v", err)
	}
	noSecretOnArgv(t, fake, secAdmin, secDB, secKey)
	if got := readFile(t, rootIn); got != secDB+"\n" {
		t.Errorf("restore stdin = %q", got)
	}
	if fake.Called("--encryption-key") || fake.Called("--db-root-password") {
		t.Error("a secret flag is back")
	}

	// The key reaches Frappe through site_config.json, host-side.
	site := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", b.SiteName)
	os.MkdirAll(site, 0o755)
	os.WriteFile(filepath.Join(site, "site_config.json"), []byte(`{"db_name":"x"}`), 0o600)
	if err := s.applySiteConfig(b, m, in, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if cfg := readFile(t, filepath.Join(site, "site_config.json")); !strings.Contains(cfg, `"backup_encryption_key": "`+secKey+`"`) {
		t.Errorf("site_config.json = %s", cfg)
	}
	// A plaintext dump gets no key written.
	os.WriteFile(filepath.Join(site, "site_config.json"), []byte(`{"db_name":"x"}`), 0o600)
	plain := Manifest{Members: []archive.Member{{Path: archive.Prefix + "/db/database.sql.gz", Encoding: archive.EncodingGzip}}}
	if err := s.applySiteConfig(b, plain, in, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if cfg := readFile(t, filepath.Join(site, "site_config.json")); strings.Contains(cfg, "backup_encryption_key") {
		t.Errorf("a key was written for a plaintext dump: %s", cfg)
	}
}

func TestDBImportKeepsSecretsOffArgv(t *testing.T) {
	rootIn := filepath.Join(t.TempDir(), "import.stdin")
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " restore ", StdinTo: rootIn})
	s, b := newTestBench(t, state.Bench{Name: "im", Mode: "dev", SiteName: "im.localhost", DBPassword: secDB})
	dump := filepath.Join(t.TempDir(), "d.sql")
	os.WriteFile(dump, []byte("select 1;"), 0o600)
	if err := s.DBImport(DBImportInput{Name: b.Name, Path: dump}, DiscardProgress{}); err != nil {
		t.Fatalf("DBImport: %v", err)
	}
	noSecretOnArgv(t, fake, secDB)
	if got := readFile(t, rootIn); got != secDB+"\n" {
		t.Errorf("import stdin = %q", got)
	}
}

func TestConfigureGitHubTokenKeepsTokenOffArgv(t *testing.T) {
	tokIn := filepath.Join(t.TempDir(), "token.stdin")
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "bash -c", StdinTo: tokIn})
	r := bench.NewRunner("tk", t.TempDir(), false)
	if err := r.ConfigureGitHubToken(secToken); err != nil {
		t.Fatal(err)
	}
	noSecretOnArgv(t, fake, secToken)
	if got := readFile(t, tokIn); got != secToken+"\n" {
		t.Errorf("token stdin = %q", got)
	}
	if err := r.ConfigureGitHubToken("a\nb"); err == nil {
		t.Error("a multi-line token was accepted")
	}
}
