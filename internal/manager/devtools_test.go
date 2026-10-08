package manager

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func writeSite(t *testing.T, b state.Bench, cfg map[string]any) string {
	t.Helper()
	dir := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", b.SiteName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "site_config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSite(t *testing.T, path string) map[string]any {
	t.Helper()
	cfg, err := readJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEnsureDevMail(t *testing.T) {
	b := state.Bench{Name: "m", Mode: "dev", SiteName: "m.localhost", Dir: t.TempDir(), TemplateVersion: mailpitTemplateVersion}
	path := writeSite(t, b, map[string]any{"db_name": "_x"})
	if err := ensureDevMail(b); err != nil {
		t.Fatal(err)
	}
	cfg := readSite(t, path)
	if cfg["mail_server"] != "mailpit" || cfg["mail_port"] != float64(1025) || cfg["disable_mail_smtp_authentication"] != float64(1) {
		t.Errorf("mail not pointed at Mailpit: %v", cfg)
	}
	if cfg["db_name"] != "_x" {
		t.Errorf("other keys lost: %v", cfg)
	}

	// A mail server the user configured is never replaced.
	path = writeSite(t, b, map[string]any{"mail_server": "smtp.example.com"})
	if err := ensureDevMail(b); err != nil {
		t.Fatal(err)
	}
	if cfg := readSite(t, path); cfg["mail_server"] != "smtp.example.com" || cfg["mail_port"] != nil {
		t.Errorf("user's mail server changed: %v", cfg)
	}

	// Prod benches and benches whose compose has no mailpit are left alone.
	for _, other := range []state.Bench{
		{Name: "p", Mode: "prod", SiteName: "p.example.com", Dir: b.Dir, TemplateVersion: mailpitTemplateVersion},
		{Name: "o", Mode: "dev", SiteName: "o.localhost", Dir: b.Dir, TemplateVersion: 1},
	} {
		path := writeSite(t, other, map[string]any{})
		if err := ensureDevMail(other); err != nil {
			t.Fatal(err)
		}
		if cfg := readSite(t, path); len(cfg) != 0 {
			t.Errorf("%s: site_config changed: %v", other.Name, cfg)
		}
	}
}

func TestSiteURL(t *testing.T) {
	for _, tc := range []struct {
		b     state.Bench
		proxy bool
		want  string
	}{
		{state.Bench{Mode: "dev", WebPort: 8010, SiteName: "a.localhost"}, false, "http://localhost:8010"},
		{state.Bench{Mode: "dev", WebPort: 8010, SiteName: "a.localhost"}, true, "http://a.localhost"},
		{state.Bench{Mode: "dev", WebPort: 8010, ProxyHost: "https://dev.example.com"}, true, "https://dev.example.com"},
		{state.Bench{Mode: "prod", Domain: "erp.example.com"}, true, "https://erp.example.com"},
		{state.Bench{Mode: "prod", Domain: "erp.example.com", ProxyHost: "http://erp.example.com"}, false, "http://erp.example.com"},
	} {
		if got := siteURL(tc.b, tc.proxy); got != tc.want {
			t.Errorf("siteURL(%+v, %v) = %q, want %q", tc.b, tc.proxy, got, tc.want)
		}
	}
}

func TestMailURL(t *testing.T) {
	fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "dv", Mode: "dev", SiteName: "dv.localhost", WebPort: 8020, SocketIOPort: 9020, TemplateVersion: mailpitTemplateVersion})
	if url, err := s.MailURL("dv"); err != nil || url != "http://localhost:8026" {
		t.Errorf("MailURL = %q, %v; want http://localhost:8026", url, err)
	}
	if err := s.AddBench(state.Bench{Name: "old", Mode: "dev", SiteName: "old.localhost", WebPort: 8030, SocketIOPort: 9030, TemplateVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MailURL("old"); err == nil || !strings.Contains(err.Error(), "ffm reconcile old") {
		t.Errorf("old bench: err = %v, want a pointer to reconcile", err)
	}
	if err := s.AddBench(state.Bench{Name: "pr", Mode: "prod", SiteName: "pr.example.com", Domain: "pr.example.com", WebPort: 8040, SocketIOPort: 9040, TemplateVersion: mailpitTemplateVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MailURL("pr"); err == nil {
		t.Error("prod bench got a Mailpit URL")
	}
}

func TestLoginURL(t *testing.T) {
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "login_as", Stdout: "some warning\nFFM_SID=abc123\n"},
		fakeexec.Rule{Match: "ffm-proxy", Stdout: ""},
	)
	s, _ := newTestBench(t, state.Bench{Name: "lg", Mode: "dev", SiteName: "lg.localhost", WebPort: 8050, SocketIOPort: 9050})
	url, err := s.LoginURL("lg", "")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://localhost:8050/app?sid=abc123" {
		t.Errorf("url = %q", url)
	}
	if !fake.Called("exec -T frappe", "'lg.localhost' 'Administrator'") {
		t.Errorf("user and site not passed as arguments; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if _, err := s.LoginURL("lg", "x'; drop"); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("'x'\\''; drop'") {
		t.Errorf("user not shell-quoted; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}

	if err := s.AddBench(state.Bench{Name: "pd", Mode: "prod", SiteName: "pd.example.com", WebPort: 8060, SocketIOPort: 9060}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginURL("pd", ""); err == nil {
		t.Error("prod bench handed out a session")
	}
}

func gz(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.String()
}

func TestDBExport(t *testing.T) {
	dump := "CREATE TABLE `tabX` (name varchar(140));\n"
	fake := fakeexec.Install(t, fakeexec.Rule{Match: "cat /tmp/ffm-db-export", StdoutBytes: []byte(gz(t, dump))})
	s, _ := newTestBench(t, state.Bench{Name: "ex", Mode: "dev", SiteName: "ex.localhost", WebPort: 8070, SocketIOPort: 9070})
	out := t.TempDir()

	plain := filepath.Join(out, "ex.sql")
	if err := s.DBExport(DBExportInput{Name: "ex", Path: plain}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(plain); string(got) != dump {
		t.Errorf("plain export = %q, want %q", got, dump)
	}
	if !fake.Called("bench --site 'ex.localhost' backup", "--backup-path-db /tmp/ffm-db-export") {
		t.Errorf("no bench backup; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}

	gzPath := filepath.Join(out, "ex.sql.gz")
	if err := s.DBExport(DBExportInput{Name: "ex", Path: gzPath}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(gzPath); string(got) != gz(t, dump) {
		t.Error(".gz export is not the gzip dump as written")
	}
	if st, _ := os.Stat(gzPath); st.Mode().Perm() != 0o600 {
		t.Errorf("export mode = %v, want 0600", st.Mode().Perm())
	}

	if err := s.DBExport(DBExportInput{Name: "ex", Path: plain}, DiscardProgress{}); err == nil {
		t.Error("an existing file was overwritten without --force")
	}
}

func TestDBImportKeepsThePasswordOffCommandLines(t *testing.T) {
	fake := fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "im", Mode: "dev", SiteName: "im.localhost", WebPort: 8080, SocketIOPort: 9080, DBPassword: "s3cret-db1"})
	dump := filepath.Join(t.TempDir(), "d.sql.gz")
	if err := os.WriteFile(dump, []byte(gz(t, "SELECT 1;")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.DBImport(DBImportInput{Name: "im", Path: dump, Migrate: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("cat > /tmp/ffm-db-import-", ".sql.gz") {
		t.Errorf("dump not streamed in; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if !fake.Called("bench --site 'im.localhost' restore '/tmp/ffm-db-import-", "--force") {
		t.Errorf("no bench restore; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if !fake.Called("bench --site 'im.localhost' migrate") {
		t.Errorf("--migrate did not migrate; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "s3cret") {
			t.Errorf("the database password reached a command line: %s", c)
		}
	}
}
