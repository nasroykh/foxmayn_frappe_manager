package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// runCLI runs the real command tree with args, returning what it wrote to
// stdout. Stdout here is a pipe, so secrets are masked unless asked for.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	markUsageErrors(root)
	root.SetArgs(append([]string{"--non-interactive"}, args...))
	root.SetErr(io.Discard)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	runErr := root.Execute()
	os.Stdout = orig
	w.Close()
	return <-done, runErr
}

const (
	psUp      = "frappe running\nmariadb running\n"
	cfgSecret = "Cfg-Sentinel-61"
	smtpPW    = "Smtp-Sentinel-72"
	userPW    = "User-Sentinel-83"
)

func result(doc string) string { return "FFM_RESULT=" + doc + "\n" }

func TestConfigListMasksSecretsAndAnnotatesJSON(t *testing.T) {
	mcpBenches(t)
	fakeexec.Install(t,
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"config": {"db_password": "` + cfgSecret + `", "developer_mode": 1,` +
			` "mail_server": "mailpit", "encryption_key": "k-` + cfgSecret + `", "allow_cors": ["a"]}}`)},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})

	out, err := runCLI(t, "config", "list", "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, cfgSecret) || !strings.Contains(out, "developer_mode") || !strings.Contains(out, manager.MaskedSecret) {
		t.Errorf("list leaked a secret or lost a key:\n%s", out)
	}
	if !strings.Contains(out, "mail_server") || !strings.Contains(out, `["a"]`) {
		t.Errorf("list output:\n%s", out)
	}

	out, err = runCLI(t, "config", "list", "dev1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, cfgSecret) {
		t.Errorf("json leaked a secret:\n%s", out)
	}
	var doc struct {
		Schema string `json:"schema"`
		Bench  string `json:"bench"`
		Config []struct {
			Key    string `json:"key"`
			Value  any    `json:"value"`
			Secret bool   `json:"secret"`
			Masked bool   `json:"masked"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.Schema != "ffm.config/v1" || doc.Bench != "dev1" || len(doc.Config) != 5 {
		t.Fatalf("doc = %+v", doc)
	}
	for _, e := range doc.Config {
		switch e.Key {
		case "db_password", "encryption_key":
			if !e.Secret || !e.Masked || e.Value != nil {
				t.Errorf("%s = %+v", e.Key, e)
			}
		case "developer_mode":
			if e.Secret || e.Value != float64(1) {
				t.Errorf("%s = %+v", e.Key, e)
			}
		}
	}

	out, err = runCLI(t, "config", "list", "dev1", "--json", "--show-secrets")
	if err != nil || !strings.Contains(out, cfgSecret) {
		t.Errorf("--show-secrets did not reveal: %v\n%s", err, out)
	}
	out, err = runCLI(t, "config", "list", "dev1", "--show-secrets")
	if err != nil || !strings.Contains(out, cfgSecret) {
		t.Errorf("--show-secrets (text) did not reveal: %v\n%s", err, out)
	}
}

func TestConfigGetMasksASecretKey(t *testing.T) {
	mcpBenches(t)
	fakeexec.Install(t,
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": true, "value": "` + cfgSecret + `"}`)},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	out, err := runCLI(t, "config", "get", "dev1", "my_api_token")
	if err != nil || strings.Contains(out, cfgSecret) || !strings.Contains(out, manager.MaskedSecret) {
		t.Errorf("get = %q, %v", out, err)
	}
	out, err = runCLI(t, "config", "get", "dev1", "my_api_token", "--show-secrets")
	if err != nil || strings.TrimSpace(out) != cfgSecret {
		t.Errorf("get --show-secrets = %q, %v", out, err)
	}
	out, err = runCLI(t, "config", "get", "dev1", "my_api_token", "--json")
	if err != nil || strings.Contains(out, cfgSecret) || !strings.Contains(out, `"schema": "ffm.config/v1"`) || !strings.Contains(out, `"masked": true`) {
		t.Errorf("get --json = %q, %v", out, err)
	}
}

func TestConfigGetPlainValueAndBenchFromArgCount(t *testing.T) {
	mcpBenches(t) // two benches: with one argument the bench would have to be picked
	fakeexec.Install(t,
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": true, "value": 587}`)},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	out, err := runCLI(t, "config", "get", "dev1", "mail_port")
	if err != nil || strings.TrimSpace(out) != "587" {
		t.Errorf("get = %q, %v", out, err)
	}
	if _, err := runCLI(t, "config", "get"); err == nil {
		t.Error("get without a key succeeded")
	}
	if _, err := runCLI(t, "config", "get", "a", "b", "c"); err == nil {
		t.Error("get with three arguments succeeded")
	}
}

func TestConfigSetAndUnsetGuards(t *testing.T) {
	mcpBenches(t)
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psUp})
	for _, args := range [][]string{
		{"config", "set", "dev1", "db_password", "x"},
		{"config", "set", "dev1", "redis_queue", "x"},
		{"config", "set", "dev1", "encryption_key", "x"},
		{"config", "unset", "dev1", "db_name"},
		{"config", "set", "prod1", "host_name", "https://x.io"},
	} {
		_, err := runCLI(t, args...)
		if ExitCode(err) != ExitPreflight {
			t.Errorf("%v: err = %v (exit %d)", args, err, ExitCode(err))
		}
	}
	if fake.Called("site_config.json") {
		t.Error("a refused key reached the container")
	}
	if _, err := runCLI(t, "config", "set", "dev1", "k", "v", "--string", "--json"); ExitCode(err) != ExitUsage {
		t.Errorf("--string with --json: %v", err)
	}
	if _, err := runCLI(t, "config", "set", "dev1", "k", "{broken", "--json"); ExitCode(err) != ExitUsage {
		t.Errorf("invalid --json value: %v", err)
	}
}

func TestConfigSetValueStaysOffArgvWithStdin(t *testing.T) {
	mcpBenches(t)
	in := filepath.Join(t.TempDir(), "cfg.stdin")
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"changed": true}`), StdinTo: in},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	stdinReader = func() io.Reader { return strings.NewReader(cfgSecret + "\n") }
	t.Cleanup(func() { stdinReader = func() io.Reader { return os.Stdin } })
	out, err := runCLI(t, "config", "set", "dev1", "my_api_token", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, cfgSecret) {
		t.Errorf("the value was echoed: %s", out)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, cfgSecret) {
			t.Errorf("value on argv: %s", c)
		}
	}
	raw, _ := os.ReadFile(in)
	if !strings.Contains(string(raw), cfgSecret) {
		t.Errorf("request = %s", raw)
	}
}

func TestUserListJSON(t *testing.T) {
	mcpBenches(t)
	fakeexec.Install(t,
		fakeexec.Rule{Match: "frappe.new_doc", Stdout: result(`{"users": [{"name": "Administrator", "email": "a@b.io", "full_name": "Administrator",` +
			` "enabled": true, "user_type": "System User", "roles": ["System Manager"]}]}`)},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	out, err := runCLI(t, "user", "list", "dev1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc jsonUsers
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Schema != "ffm.users/v1" || doc.Bench != "dev1" ||
		len(doc.Users) != 1 || !doc.Users[0].SystemManager {
		t.Errorf("doc = %+v, %v\n%s", doc, err, out)
	}
	out, err = runCLI(t, "user", "list", "dev1")
	if err != nil || !strings.Contains(out, "System Manager") {
		t.Errorf("text = %q, %v", out, err)
	}
}

func TestUserAddPasswordSources(t *testing.T) {
	mcpBenches(t)
	in := filepath.Join(t.TempDir(), "u.stdin")
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "frappe.new_doc", Stdout: result(`{"name": "ana@example.com"}`), StdinTo: in},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	t.Cleanup(func() { stdinReader = func() io.Reader { return os.Stdin } })

	// No source and no terminal: refuse, naming the flag.
	t.Setenv(envUserPassword, "")
	_, err := runCLI(t, "user", "add", "ana@example.com", "dev1")
	if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("no password source: %v", err)
	}
	// stdin
	stdinReader = func() io.Reader { return strings.NewReader(userPW + "\n") }
	if _, err := runCLI(t, "user", "add", "ana@example.com", "dev1", "--password-stdin", "--system-manager", "--first-name", "Ana"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(in)
	if !strings.Contains(string(raw), userPW) || !strings.Contains(string(raw), `"system_manager":true`) {
		t.Errorf("request = %s", raw)
	}
	// environment
	os.Remove(in)
	t.Setenv(envUserPassword, userPW+"-env")
	if _, err := runCLI(t, "user", "add", "bo@example.com", "dev1"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(in)
	if !strings.Contains(string(raw), userPW+"-env") {
		t.Errorf("env request = %s", raw)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, userPW) {
			t.Errorf("password on argv: %s", c)
		}
	}
	// there is no flag that takes the password
	if _, err := runCLI(t, "user", "add", "x@example.com", "dev1", "--password", "nope"); ExitCode(err) != ExitUsage {
		t.Errorf("--password exists: %v", err)
	}
	if _, err := runCLI(t, "user", "add", "not-an-email", "dev1", "--password-stdin"); ExitCode(err) != ExitUsage {
		t.Errorf("bad email: %v", err)
	}
}

func TestUserDisableAdministratorIsRefused(t *testing.T) {
	mcpBenches(t)
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psUp})
	_, err := runCLI(t, "user", "disable", "Administrator", "dev1")
	if ExitCode(err) != ExitPreflight {
		t.Errorf("err = %v (exit %d)", err, ExitCode(err))
	}
	if fake.Called("frappe.new_doc") {
		t.Error("reached the container")
	}
}

func TestMailShowJSONHasNoPassword(t *testing.T) {
	mcpBenches(t)
	fakeexec.Install(t,
		fakeexec.Rule{Match: "Email Account", Stdout: result(`{"account": {"name": "Outgoing (ffm)", "email_id": "erp@example.com",` +
			` "smtp_server": "smtp.example.com", "smtp_port": 465, "use_tls": 0, "use_ssl_for_outgoing": 1, "login_id": "tok",` +
			` "login_id_is_different": 1, "no_smtp_authentication": 0, "has_password": true, "password": "` + smtpPW + `"}, "site_config": {}}`)},
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": true, "value": "Outgoing (ffm)"}`)},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	out, err := runCLI(t, "mail", "show", "dev1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, smtpPW) {
		t.Errorf("the SMTP password reached the output:\n%s", out)
	}
	var doc jsonMail
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Schema != "ffm.mail/v1" || doc.Effective != "account" || !doc.Managed ||
		doc.Account == nil || doc.Account.TLS != "ssl" || doc.Account.User != "tok" || !doc.Account.HasPassword {
		t.Errorf("doc = %+v, %v\n%s", doc, err, out)
	}
}

func TestMailSetPrintsRecordsAndKeepsPasswordOffArgv(t *testing.T) {
	mcpBenches(t)
	in := filepath.Join(t.TempDir(), "mail.stdin")
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "Email Account", Stdout: result(`{"account": {"name": "Outgoing (ffm)", "email_id": "erp@example.com",` +
			` "smtp_server": "smtp.postmarkapp.com", "smtp_port": 587, "use_tls": 1, "use_ssl_for_outgoing": 0, "login_id": "erp@example.com",` +
			` "login_id_is_different": 0, "no_smtp_authentication": 0, "has_password": true}}`), StdinTo: in},
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": false, "changed": true}`)},
		fakeexec.Rule{Match: " ps ", Stdout: psUp})
	stdinReader = func() io.Reader { return strings.NewReader(smtpPW + "\n") }
	t.Cleanup(func() { stdinReader = func() io.Reader { return os.Stdin } })
	out, err := runCLI(t, "mail", "set", "dev1", "--smtp-host", "smtp.postmarkapp.com", "--from", "erp@example.com", "--password-stdin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SPF", "include:spf.mtasv.net", "DKIM", "DMARC", "_dmarc.example.com", "transactional"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, smtpPW) {
		t.Errorf("password in the output:\n%s", out)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, smtpPW) {
			t.Errorf("password on argv: %s", c)
		}
	}
	raw, _ := os.ReadFile(in)
	if !strings.Contains(string(raw), smtpPW) {
		t.Errorf("the password did not arrive on stdin: %s", raw)
	}
	for _, args := range [][]string{
		{"mail", "set", "dev1", "--from", "erp@example.com"},                          // no host
		{"mail", "set", "dev1", "--smtp-host", "h.io"},                                // no from
		{"mail", "set", "dev1", "--smtp-host", "h.io", "--from", "x", "--tls", "tls"}, // bad
		{"mail", "set", "dev1", "--smtp-host", "h.io", "--from", "a@h.io", "--password", "x"},
	} {
		if _, err := runCLI(t, args...); ExitCode(err) != ExitUsage {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// Bare 'ffm mail <bench>' still goes to the Mailpit code, not to a subcommand:
// the test bench predates Mailpit, and that is the error only MailURL gives.
func TestBareMailStillTargetsMailpit(t *testing.T) {
	mcpBenches(t)
	fakeexec.Install(t)
	_, err := runCLI(t, "mail", "dev1", "--print")
	if err == nil || !strings.Contains(err.Error(), "Mailpit") {
		t.Errorf("mail dev1 --print: %v", err)
	}
}
