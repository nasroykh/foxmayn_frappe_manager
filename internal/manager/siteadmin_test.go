package manager

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	secSMTP     = "Smtp-Sentinel-31"
	secUserPW   = "UserPw-Sentinel-42"
	secCfgValue = "CfgValue-Sentinel-53"
)

// result is the line a site script prints.
func result(doc string) string { return resultPrefix + doc + "\n" }

// siteBench is a running dev bench whose frappe container answers by script.
func siteBench(t *testing.T, mut func(*state.Bench), rules ...fakeexec.Rule) (*Service, state.Bench, *fakeexec.Fake) {
	t.Helper()
	rules = append(rules, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	fake := fakeexec.Install(t, rules...)
	b := state.Bench{Name: "sa", Mode: "dev", SiteName: "sa.localhost", DBPassword: secDB, AdminPassword: secAdmin,
		TemplateVersion: bench.TemplateVersion}
	if mut != nil {
		mut(&b)
	}
	s, b := newTestBench(t, b)
	return s, b, fake
}

func stdinOf(t *testing.T, p string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, p)), &m); err != nil {
		t.Fatalf("stdin %s is not one JSON document: %v\n%s", p, err, readFile(t, p))
	}
	return m
}

func TestParseConfigValue(t *testing.T) {
	cases := []struct {
		raw  string
		mode ConfigValueMode
		want string // FormatConfigValue of the result
		typ  string
	}{
		{"true", ConfigAuto, "true", "bool"},
		{"False", ConfigAuto, "false", "bool"},
		{"587", ConfigAuto, "587", "number"},
		{"-3", ConfigAuto, "-3", "number"},
		{"0.5", ConfigAuto, "0.5", "number"},
		{"12345678901234567890", ConfigAuto, "12345678901234567890", "number"}, // not rounded through a float
		{"007", ConfigAuto, "007", "string"},
		{"1e3", ConfigAuto, "1e3", "string"},
		{"null", ConfigAuto, "null", "string"},
		{`["a","b"]`, ConfigAuto, `["a","b"]`, "array"},
		{`{"k": 1}`, ConfigAuto, `{"k":1}`, "object"},
		{`[broken`, ConfigAuto, `[broken`, "string"},
		{"hello world", ConfigAuto, "hello world", "string"},
		{"true", ConfigString, "true", "string"},
		{"587", ConfigString, "587", "string"},
		{"null", ConfigJSON, "null", "nil"},
		{`"x"`, ConfigJSON, "x", "string"},
		{"5", ConfigJSON, "5", "number"},
	}
	for _, c := range cases {
		v, err := ParseConfigValue(c.raw, c.mode)
		if err != nil {
			t.Errorf("%q: %v", c.raw, err)
			continue
		}
		typ := "string"
		switch v.(type) {
		case bool:
			typ = "bool"
		case json.Number:
			typ = "number"
		case []any:
			typ = "array"
		case map[string]any:
			typ = "object"
		case nil:
			typ = "nil"
		}
		if typ != c.typ || FormatConfigValue(v) != c.want {
			t.Errorf("%q (mode %d) = %v (%s), want %s (%s)", c.raw, c.mode, FormatConfigValue(v), typ, c.want, c.typ)
		}
	}
	for _, bad := range []string{"", "nope", `{"a":`, `1 2`} {
		if _, err := ParseConfigValue(bad, ConfigJSON); err == nil {
			t.Errorf("--json accepted %q", bad)
		}
	}
	// A number reaches Frappe as a JSON number, a string as a string.
	raw, _ := json.Marshal(map[string]any{"a": json.Number("587"), "b": "587"})
	if string(raw) != `{"a":587,"b":"587"}` {
		t.Errorf("marshalled = %s", raw)
	}
}

func TestIsSecretConfigKey(t *testing.T) {
	for _, k := range []string{"db_password", "encryption_key", "backup_encryption_key", "api_secret", "Mail_Password",
		"github_token", "razorpay_key_secret", "auth_key"} {
		if !IsSecretConfigKey(k, "x") {
			t.Errorf("%s is not masked", k)
		}
	}
	for _, k := range []string{"developer_mode", "mail_server", "db_name", "host_name", "maintenance_mode", "max_file_size"} {
		if IsSecretConfigKey(k, "x") {
			t.Errorf("%s is masked", k)
		}
	}
	if !IsSecretConfigKey("redis_cache", "redis://:hunter2@redis-cache:6379") {
		t.Error("a URL carrying a password is not masked")
	}
	if IsSecretConfigKey("redis_cache", "redis://redis-cache:6379") {
		t.Error("a URL without credentials is masked")
	}
}

func TestConfigGuard(t *testing.T) {
	dev := state.Bench{Name: "d", Mode: "dev"}
	prod := state.Bench{Name: "p", Mode: "prod"}
	proxied := state.Bench{Name: "x", Mode: "dev", ProxyHost: "https://x.example.com"}
	refused := func(b state.Bench, key string, force bool) bool {
		err := configGuard(b, key, force)
		var pre *PreflightError
		return err != nil && (errors.As(err, &pre) || strings.HasPrefix(err.Error(), "invalid key"))
	}
	for k := range configOwnedKeys {
		if !refused(dev, k, true) {
			t.Errorf("%s is not refused (even --force must not lift it)", k)
		}
	}
	// What Create writes with `set-config` must be on the list.
	for _, k := range []string{"db_host", "db_port", "db_type", "db_schema", "redis_cache", "redis_queue", "redis_socketio",
		"socketio_port", "socketio_frappe_url", "db_name", "db_password", "installed_apps"} {
		if _, ok := configOwnedKeys[k]; !ok {
			t.Errorf("%s missing from configOwnedKeys", k)
		}
	}
	if !refused(prod, "host_name", false) || !refused(proxied, "host_name", false) || refused(dev, "host_name", false) {
		t.Error("host_name: refused on prod and behind a proxy host only")
	}
	if !refused(dev, "encryption_key", false) || refused(dev, "encryption_key", true) {
		t.Error("encryption_key: refused unless --force")
	}
	for _, ok := range []string{"developer_mode", "max_file_size", "mail_server", "allow_cors", "my.key-1"} {
		if refused(dev, ok, false) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "1abc", "a b", "a;b", "a\nb", "$(x)"} {
		if !refused(dev, bad, false) {
			t.Errorf("key %q accepted", bad)
		}
	}
}

func TestConfigSetSendsTheValueOnStdinOnly(t *testing.T) {
	in := filepath.Join(t.TempDir(), "cfg.stdin")
	s, _, fake := siteBench(t, nil, fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"changed": true}`), StdinTo: in})
	v, _ := ParseConfigValue(secCfgValue, ConfigAuto)
	if err := s.ConfigSet(ConfigSetInput{Bench: "sa", Key: "my_api_token", Value: v}); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	noSecretOnArgv(t, fake, secCfgValue)
	req := stdinOf(t, in)
	if req["op"] != "set" || req["key"] != "my_api_token" || req["value"] != secCfgValue {
		t.Errorf("request = %v", req)
	}
	// A number stays a number, false stays present.
	for raw, want := range map[string]string{"587": `"value":587`, "false": `"value":false`} {
		v, _ := ParseConfigValue(raw, ConfigAuto)
		if err := s.ConfigSet(ConfigSetInput{Bench: "sa", Key: "k", Value: v}); err != nil {
			t.Fatal(err)
		}
		if got := strings.ReplaceAll(readFile(t, in), " ", ""); !strings.Contains(got, want) {
			t.Errorf("%s: request %s lacks %s", raw, got, want)
		}
	}
}

func TestConfigRefusalsRunNothing(t *testing.T) {
	s, _, fake := siteBench(t, nil)
	for _, key := range []string{"db_password", "db_name", "redis_queue", "encryption_key", "socketio_port"} {
		err := s.ConfigSet(ConfigSetInput{Bench: "sa", Key: key, Value: "x"})
		var pre *PreflightError
		if !errors.As(err, &pre) {
			t.Errorf("set %s: %v", key, err)
		}
		if _, err := s.ConfigUnset("sa", key, false); !errors.As(err, &pre) {
			t.Errorf("unset %s: %v", key, err)
		}
	}
	if fake.Called("site_config.json") {
		t.Errorf("a refused key reached the container:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestConfigNeedsARunningBench(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: ""})
	s, _ := newTestBench(t, state.Bench{Name: "off", Mode: "dev", SiteName: "off.localhost"})
	for name, err := range map[string]error{
		"list":  func() error { _, e := s.ConfigList("off"); return e }(),
		"get":   func() error { _, e := s.ConfigGet("off", "k"); return e }(),
		"set":   s.ConfigSet(ConfigSetInput{Bench: "off", Key: "k", Value: "v"}),
		"unset": func() error { _, e := s.ConfigUnset("off", "k", false); return e }(),
		"users": func() error { _, e := s.UserList("off"); return e }(),
		"mail":  func() error { _, e := s.MailShow("off"); return e }(),
	} {
		if !errors.Is(err, ErrBenchStopped) {
			t.Errorf("%s on a stopped bench: %v", name, err)
		}
	}
}

func TestConfigListGetUnset(t *testing.T) {
	s, _, _ := siteBench(t, nil, fakeexec.Rule{Match: "site_config.json", Stdout: result(
		`{"config": {"zeta": 1, "alpha": "a", "maintenance_mode": 0, "allow_cors": ["x"], "big": 12345678901234567890},` +
			` "found": true, "value": 587, "changed": true}`)})
	list, err := s.ConfigList("sa")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range list {
		keys = append(keys, e.Key)
	}
	if strings.Join(keys, ",") != "allow_cors,alpha,big,maintenance_mode,zeta" {
		t.Errorf("keys = %v", keys)
	}
	if got := FormatConfigValue(list[2].Value); got != "12345678901234567890" {
		t.Errorf("big = %s", got)
	}
	e, err := s.ConfigGet("sa", "mail_port")
	if err != nil || FormatConfigValue(e.Value) != "587" {
		t.Errorf("get = %v, %v", e, err)
	}
	if changed, err := s.ConfigUnset("sa", "zeta", false); err != nil || !changed {
		t.Errorf("unset = %v, %v", changed, err)
	}
}

func TestConfigGetMissingKey(t *testing.T) {
	s, _, _ := siteBench(t, nil, fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": false, "value": null}`)})
	if _, err := s.ConfigGet("sa", "nope"); !errors.Is(err, ErrConfigKeyNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := s.ConfigGet("sa", "bad key"); err == nil {
		t.Error("an invalid key reached the container")
	}
}

func TestConfigErrorsDoNotLeakTheValue(t *testing.T) {
	s, _, _ := siteBench(t, nil, fakeexec.Rule{Match: "site_config.json", Exit: 1, Stdout: "Traceback ... " + secCfgValue + "\n"})
	err := s.ConfigSet(ConfigSetInput{Bench: "sa", Key: "my_secret", Value: secCfgValue})
	if err == nil || strings.Contains(err.Error(), secCfgValue) {
		t.Errorf("err = %v", err)
	}
}

func TestUserAddKeepsThePasswordOffArgv(t *testing.T) {
	in := filepath.Join(t.TempDir(), "user.stdin")
	s, _, fake := siteBench(t, nil, fakeexec.Rule{Match: "frappe.new_doc", Stdout: result(`{"name": "ana@example.com"}`), StdinTo: in})
	err := s.UserAdd(UserAddInput{Bench: "sa", Email: "ana@example.com", FirstName: "Ana", SystemManager: true, Password: secUserPW})
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	noSecretOnArgv(t, fake, secUserPW)
	req := stdinOf(t, in)
	if req["op"] != "add" || req["password"] != secUserPW || req["email"] != "ana@example.com" || req["system_manager"] != true {
		t.Errorf("request = %v", req)
	}
	// No bench add-user / set-password: both take the password as an argument.
	if fake.Called("add-user") || fake.Called("set-password") || fake.Called("add-system-manager") {
		t.Errorf("a bench user command was used:\n%s", strings.Join(fake.Calls(), "\n"))
	}
}

func TestUserAddValidation(t *testing.T) {
	s, _, fake := siteBench(t, nil)
	for _, c := range []UserAddInput{
		{Bench: "sa", Email: "not-an-email", Password: "x"},
		{Bench: "sa", Email: "Ana <ana@example.com>", Password: "x"},
		{Bench: "sa", Email: "a@b.co", Password: ""},
		{Bench: "sa", Email: "a@b.co", Password: "two\nlines"},
	} {
		if err := s.UserAdd(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if fake.Called("frappe.new_doc") {
		t.Error("an invalid user reached the container")
	}
}

func TestUserDisableRefusals(t *testing.T) {
	s, _, fake := siteBench(t, func(b *state.Bench) { b.Agent = true })
	for _, u := range []string{"Administrator", "administrator", " ADMINISTRATOR ", "Guest", "agent@sa.localhost"} {
		err := s.UserSetEnabled("sa", u, false)
		var pre *PreflightError
		if !errors.As(err, &pre) {
			t.Errorf("disable %q: %v", u, err)
		}
	}
	if fake.Called("frappe.new_doc") {
		t.Error("a refused disable reached the container")
	}
}

func TestUserEnableDisableRun(t *testing.T) {
	in := filepath.Join(t.TempDir(), "u.stdin")
	s, _, _ := siteBench(t, nil, fakeexec.Rule{Match: "frappe.new_doc", Stdout: result(`{"name": "ana@example.com"}`), StdinTo: in})
	if err := s.UserSetEnabled("sa", "ana@example.com", false); err != nil {
		t.Fatal(err)
	}
	if req := stdinOf(t, in); req["op"] != "disable" || req["name"] != "ana@example.com" {
		t.Errorf("request = %v", req)
	}
	if err := s.UserSetEnabled("sa", "administrator", true); err != nil { // enabling is always fine
		t.Fatal(err)
	}
	if req := stdinOf(t, in); req["op"] != "enable" || req["name"] != "Administrator" {
		t.Errorf("request = %v", req)
	}
}

func TestUserPasswordForAdministratorReconcilesState(t *testing.T) {
	in := filepath.Join(t.TempDir(), "admin.stdin")
	s, _, fake := siteBench(t, nil, fakeexec.Rule{Match: "set-admin-password", StdinTo: in})
	if err := s.UserSetPassword("sa", "administrator", secUserPW); err != nil {
		t.Fatalf("UserSetPassword: %v\n%s", err, strings.Join(fake.Calls(), "\n"))
	}
	noSecretOnArgv(t, fake, secUserPW, secAdmin)
	if got := readFile(t, in); got != secUserPW+"\n" {
		t.Errorf("set-admin-password stdin = %q", got)
	}
	rec, _ := s.GetBench("sa")
	if rec.AdminPassword != secUserPW {
		t.Errorf("state AdminPassword = %q", rec.AdminPassword)
	}
	if err := s.UserSetPassword("sa", "Administrator", "-leading-dash"); err == nil {
		t.Error("accepted a password set-admin-password would misread")
	}
}

func TestUserPasswordForOthersUsesTheDoctype(t *testing.T) {
	in := filepath.Join(t.TempDir(), "pw.stdin")
	s, _, fake := siteBench(t, nil, fakeexec.Rule{Match: "frappe.new_doc", Stdout: result(`{"name": "ana@example.com"}`), StdinTo: in})
	if err := s.UserSetPassword("sa", "ana@example.com", secUserPW); err != nil {
		t.Fatal(err)
	}
	noSecretOnArgv(t, fake, secUserPW)
	if req := stdinOf(t, in); req["op"] != "password" || req["password"] != secUserPW {
		t.Errorf("request = %v", req)
	}
	rec, _ := s.GetBench("sa")
	if rec.AdminPassword != secAdmin {
		t.Error("another user's password changed ffm's Administrator record")
	}
}

func TestUserListMarksSystemManagers(t *testing.T) {
	s, _, _ := siteBench(t, nil, fakeexec.Rule{Match: "frappe.new_doc", Stdout: result(`{"users": [` +
		`{"name": "Administrator", "email": "admin@example.com", "full_name": "Administrator", "enabled": true, "user_type": "System User", "last_login": null, "roles": ["System Manager", "Desk User"]},` +
		`{"name": "ana@example.com", "email": "ana@example.com", "full_name": "Ana", "enabled": false, "user_type": "System User", "roles": null}]}`)})
	users, err := s.UserList("sa")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || !users[0].SystemManager || users[1].SystemManager || !users[0].Enabled || users[1].Enabled || users[1].Roles == nil {
		t.Errorf("users = %+v", users)
	}
}

// ---- mail ----

func TestValidateMailSet(t *testing.T) {
	ok, err := ValidateMailSet(MailSetInput{SMTPHost: "smtp.example.com", From: "erp@example.com"})
	if err != nil || ok.TLS != "starttls" || ok.SMTPPort != 587 {
		t.Fatalf("defaults = %+v, %v", ok, err)
	}
	if in, _ := ValidateMailSet(MailSetInput{SMTPHost: "h.io", From: "a@h.io", TLS: "ssl"}); in.SMTPPort != 465 {
		t.Errorf("ssl port = %d", in.SMTPPort)
	}
	if in, _ := ValidateMailSet(MailSetInput{SMTPHost: "h.io", From: "a@h.io", TLS: "none"}); in.SMTPPort != 25 {
		t.Errorf("none port = %d", in.SMTPPort)
	}
	for _, bad := range []MailSetInput{
		{SMTPHost: "", From: "a@h.io"},
		{SMTPHost: "smtp://h.io", From: "a@h.io"},
		{SMTPHost: "h.io:587", From: "a@h.io"},
		{SMTPHost: "h.io", From: "nope"},
		{SMTPHost: "h.io", From: "A <a@h.io>"},
		{SMTPHost: "h.io", From: "a@h.io", TLS: "tls"},
		{SMTPHost: "h.io", From: "a@h.io", SMTPPort: 70000},
		{SMTPHost: "h.io", From: "a@h.io", Password: "a\nb"},
		{SMTPHost: "h.io", From: "a@h.io", AccountName: "a\nb"},
	} {
		if _, err := ValidateMailSet(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestMailSetKeepsTheSMTPPasswordOffArgv(t *testing.T) {
	mailIn := filepath.Join(t.TempDir(), "mail.stdin")
	cfgIn := filepath.Join(t.TempDir(), "cfg.stdin")
	s, _, fake := siteBench(t, nil,
		fakeexec.Rule{Match: "Email Account", Stdout: result(`{"account": {"name": "Outgoing (ffm)", "email_id": "erp@example.com",` +
			` "smtp_server": "smtp.example.com", "smtp_port": 587, "use_tls": 1, "use_ssl_for_outgoing": 0, "login_id": "tok",` +
			` "login_id_is_different": 1, "no_smtp_authentication": 0, "has_password": true}}`), StdinTo: mailIn},
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": true, "value": "mailpit", "changed": true}`), StdinTo: cfgIn})
	acct, err := s.MailSet(MailSetInput{Bench: "sa", SMTPHost: "smtp.example.com", From: "erp@example.com", User: "tok",
		Password: secSMTP, AccountName: "Outgoing (ffm)"})
	if err != nil {
		t.Fatalf("MailSet: %v\n%s", err, strings.Join(fake.Calls(), "\n"))
	}
	noSecretOnArgv(t, fake, secSMTP)
	req := stdinOf(t, mailIn)
	if req["op"] != "set" || req["password"] != secSMTP || req["login"] != "tok" || req["tls"] != "starttls" ||
		req["smtp_port"] != float64(587) || req["verify"] != true || req["account_name"] != "Outgoing (ffm)" {
		t.Errorf("request = %v", req)
	}
	// The last site_config call writes the marker that keeps Mailpit away.
	if m := stdinOf(t, cfgIn); m["op"] != "set" || m["key"] != mailMarkerKey || m["value"] != "Outgoing (ffm)" {
		t.Errorf("marker request = %v", m)
	}
	if acct.Security() != "starttls" || acct.Login() != "tok" || !acct.HasPassword {
		t.Errorf("account = %+v", acct)
	}
	if b, _ := s.GetBench("sa"); b.AdminPassword != secAdmin {
		t.Error("state touched")
	}
}

func TestMailSetNamedAccountIsTheSenderName(t *testing.T) {
	mailIn := filepath.Join(t.TempDir(), "mail.stdin")
	s, _, _ := siteBench(t, func(b *state.Bench) { b.Mode = "prod" },
		fakeexec.Rule{Match: "Email Account", Stdout: result(`{"account": {"name": "Acme ERP", "email_id": "erp@acme.io"}}`), StdinTo: mailIn},
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": false, "changed": true}`)})
	if _, err := s.MailSet(MailSetInput{Bench: "sa", SMTPHost: "smtp.acme.io", From: "erp@acme.io", AccountName: "Acme ERP", NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	req := stdinOf(t, mailIn)
	if req["name_as_sender"] != true || req["account_name"] != "Acme ERP" || req["verify"] != false {
		t.Errorf("request = %v", req)
	}
	if _, has := req["password"]; has || req["login"] != nil {
		t.Errorf("an unauthenticated account carried credentials: %v", req)
	}
}

func TestMailSetWithoutANameLetsFrappeNameTheAccount(t *testing.T) {
	mailIn := filepath.Join(t.TempDir(), "mail.stdin")
	s, _, _ := siteBench(t, func(b *state.Bench) { b.Mode = "prod" },
		fakeexec.Rule{Match: "Email Account", Stdout: result(`{"account": {"name": "Outgoing (ffm)", "email_id": "erp@acme.io"}}`), StdinTo: mailIn},
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": false, "changed": true}`)})
	if _, err := s.MailSet(MailSetInput{Bench: "sa", SMTPHost: "smtp.acme.io", From: "erp@acme.io", TLS: "ssl", SMTPPort: 2465}); err != nil {
		t.Fatal(err)
	}
	req := stdinOf(t, mailIn)
	if _, named := req["name_as_sender"]; named || req["account_name"] != nil || req["tls"] != "ssl" || req["smtp_port"] != float64(2465) {
		t.Errorf("request = %v", req)
	}
}

func TestMailSetUserNeedsPassword(t *testing.T) {
	s, _, fake := siteBench(t, nil)
	if _, err := s.MailSet(MailSetInput{Bench: "sa", SMTPHost: "h.io", From: "a@h.io", User: "u"}); err == nil {
		t.Error("--user without a password accepted")
	}
	if fake.Called("Email Account") {
		t.Error("reached the container")
	}
}

func TestMailDNSRecords(t *testing.T) {
	recs := MailDNSRecords("Erp@Example.COM", "smtp.postmarkapp.com")
	if len(recs) != 3 {
		t.Fatalf("records = %+v", recs)
	}
	want := map[string]string{"SPF": "example.com", "DKIM": "<selector>._domainkey.example.com", "DMARC": "_dmarc.example.com"}
	for _, r := range recs {
		if r.Name != want[r.Kind] {
			t.Errorf("%s name = %q", r.Kind, r.Name)
		}
	}
	if !strings.Contains(recs[0].Value, "include:spf.mtasv.net") || !strings.HasPrefix(recs[2].Value, "v=DMARC1;") {
		t.Errorf("values = %q / %q", recs[0].Value, recs[2].Value)
	}
	if got := MailDNSRecords("a@b.io", "email-smtp.eu-west-1.amazonaws.com")[0].Value; !strings.Contains(got, "include:amazonses.com") {
		t.Errorf("ses = %q", got)
	}
	if got := MailDNSRecords("a@b.io", "mail.unknown.example")[0].Value; !strings.Contains(got, "<your provider's SPF domain>") {
		t.Errorf("unknown = %q", got)
	}
	if MailDNSRecords("nope", "h") != nil {
		t.Error("records for an address without a domain")
	}
}

// siteConfigFile writes the dev bench's site_config.json and returns its path.
func siteConfigFile(t *testing.T, b state.Bench, body string) string {
	t.Helper()
	dir := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", b.SiteName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "site_config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsureDevMailLeavesAMailConfiguredBenchAlone(t *testing.T) {
	_, b, _ := siteBench(t, nil)
	p := siteConfigFile(t, b, `{"db_name": "x", "`+mailMarkerKey+`": "Outgoing (ffm)"}`)
	if err := ensureDevMail(b); err != nil {
		t.Fatal(err)
	}
	cfg, _ := readJSONFile(p)
	for _, k := range devMailpitKeys {
		if _, set := cfg[k]; set {
			t.Errorf("Mailpit's %s came back on a bench with its own Email Account", k)
		}
	}
	// Without the marker the existing behaviour holds: Mailpit is wired in...
	p2 := siteConfigFile(t, b, `{"db_name": "x"}`)
	if err := ensureDevMail(b); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := readJSONFile(p2); cfg["mail_server"] != "mailpit" {
		t.Errorf("config = %v", cfg)
	}
	// ...unless a mail server is already set.
	siteConfigFile(t, b, `{"mail_server": "smtp.mine.io"}`)
	if err := ensureDevMail(b); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := readJSONFile(p2); cfg["mail_server"] != "smtp.mine.io" {
		t.Errorf("config = %v", cfg)
	}
}

func TestMailUnsetPutsMailpitBack(t *testing.T) {
	var p string
	s, b, fake := siteBench(t, nil,
		fakeexec.Rule{Match: "Email Account", Stdout: result(`{"changed": true}`)},
		fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": true, "value": "Acme ERP", "changed": true}`)})
	p = siteConfigFile(t, b, `{"db_name": "x"}`)
	changed, err := s.MailUnset("sa", "")
	if err != nil || !changed {
		t.Fatalf("MailUnset = %v, %v\n%s", changed, err, strings.Join(fake.Calls(), "\n"))
	}
	if cfg, _ := readJSONFile(p); cfg["mail_server"] != "mailpit" {
		t.Errorf("Mailpit not restored: %v", cfg)
	}
}

func TestMailShowEffective(t *testing.T) {
	for _, c := range []struct {
		name, out, want string
		managed         bool
	}{
		{"account wins", `{"account": {"name": "A", "email_id": "a@b.io"}, "site_config": {"mail_server": "mailpit"}}`, "account", true},
		{"site_config", `{"account": null, "site_config": {"mail_server": "mailpit"}}`, "site_config", false},
		{"none", `{"account": null, "site_config": {}}`, "none", false},
	} {
		out := result(c.out)
		rules := []fakeexec.Rule{{Match: "Email Account", Stdout: out}}
		found := "false"
		if c.managed {
			found = "true"
		}
		rules = append(rules, fakeexec.Rule{Match: "site_config.json", Stdout: result(`{"found": ` + found + `}`)})
		s, _, _ := siteBench(t, nil, rules...)
		st, err := s.MailShow("sa")
		if err != nil || st.Effective != c.want || st.Managed != c.managed {
			t.Errorf("%s: %+v, %v", c.name, st, err)
		}
	}
}

// The reserved names keep `ffm mail set|show|unset` unambiguous.
func TestMailSubcommandsAreReservedBenchNames(t *testing.T) {
	for _, n := range []string{"set", "show", "unset"} {
		if err := bench.ValidateNewName(n); err == nil {
			t.Errorf("%q is allowed as a new bench name", n)
		}
	}
}

func TestSiteScriptFailureShowsFrappesMessageNotATraceback(t *testing.T) {
	s, _, _ := siteBench(t, nil, fakeexec.Rule{Match: "frappe.new_doc", Exit: 1,
		Stdout: "noise\n" + errorPrefix + "Avoid sequences like abc or 6543 as they are easy to guess (ValidationError)\n"})
	err := s.UserAdd(UserAddInput{Bench: "sa", Email: "weak@example.com", Password: "abc"})
	if err == nil || err.Error() != "user add: Avoid sequences like abc or 6543 as they are easy to guess (ValidationError)" {
		t.Errorf("err = %v", err)
	}
}

func TestSiteScriptsStartWithTheErrorHook(t *testing.T) {
	for name, script := range map[string]string{"config": configScript, "user": userScript, "mail": mailScript} {
		full := scriptPrelude + script
		if !strings.Contains(full, "sys.excepthook") || !strings.HasPrefix(full, "import json, os, re, sys") {
			t.Errorf("%s script lacks the prelude", name)
		}
	}
}
