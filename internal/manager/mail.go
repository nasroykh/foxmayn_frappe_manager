package manager

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// mailMarkerKey is the site_config.json key that says ffm configured the
// site's outgoing mail through an Email Account. ensureDevMail reads it so the
// Mailpit settings are not put back next to that account on every start. It
// lives in site_config (not in ffm's record) so it travels with a backup.
const mailMarkerKey = "ffm_mail_account"

// DefaultMailAccount is the name of the Email Account `ffm mail set` manages
// unless --name says otherwise.
const DefaultMailAccount = "Outgoing (ffm)"

// Transport security of the SMTP connection.
const (
	MailTLSStartTLS = "starttls"
	MailTLSSSL      = "ssl"
	MailTLSNone     = "none"
)

// DefaultSMTPPort is the conventional port for a security mode.
func DefaultSMTPPort(tls string) int {
	switch tls {
	case MailTLSSSL:
		return 465
	case MailTLSNone:
		return 25
	}
	return 587
}

// mailScript creates, updates, reads and disables the Email Account that
// carries a site's outgoing mail. The request, SMTP password included, arrives
// on stdin; the password is never printed back.
const mailScript = `import frappe
req = json.load(sys.stdin)
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
frappe.set_user("Administrator")
op = req["op"]
res = {}
FIELDS = ["name", "email_id", "smtp_server", "smtp_port", "use_tls", "use_ssl_for_outgoing", "login_id",
    "login_id_is_different", "enable_outgoing", "default_outgoing", "no_smtp_authentication"]

def describe(name):
    d = frappe.get_doc("Email Account", name)
    out = {f: d.get(f) for f in FIELDS}
    out["smtp_port"] = int(out.get("smtp_port") or 0)
    out["has_password"] = bool(d.get_password("password", raise_exception=False))
    return out

if op == "show":
    names = frappe.get_all("Email Account", filters={"enable_outgoing": 1, "default_outgoing": 1}, pluck="name")
    res["account"] = describe(names[0]) if names else None
    res["site_config"] = {k: frappe.conf.get(k) for k in ("mail_server", "mail_port", "use_tls", "use_ssl")
        if frappe.conf.get(k) is not None}
elif op == "set":
    name = req["account_name"]
    doc = None
    if frappe.db.exists("Email Account", name):
        doc = frappe.get_doc("Email Account", name)
    else:
        # An outgoing-only account is unique per address and on/off state
        # (unique_email_account_type), so an existing one is updated, enabled or
        # not, rather than a second one made.
        other = frappe.get_all("Email Account", filters={"email_id": req["email_id"], "enable_incoming": 0},
            order_by="enable_outgoing desc", pluck="name", limit=1)
        if other:
            doc = frappe.get_doc("Email Account", other[0])
    if doc is None:
        doc = frappe.new_doc("Email Account")
        doc.email_account_name = name
    doc.update({"email_id": req["email_id"], "smtp_server": req["smtp_server"],
        "smtp_port": req["smtp_port"], "use_tls": 1 if req["tls"] == "starttls" else 0,
        "use_ssl_for_outgoing": 1 if req["tls"] == "ssl" else 0,
        "enable_outgoing": 1, "default_outgoing": 1, "enable_incoming": 0})
    if req.get("name_as_sender"):
        doc.update({"always_use_account_name_as_sender_name": 1})
    if req.get("login"):
        doc.update({"login_id_is_different": 1 if req["login"] != req["email_id"] else 0, "login_id": req["login"],
            "no_smtp_authentication": 0})
    elif not req.get("password"):
        # No authentication: forget any login and stored password an earlier set left behind.
        doc.update({"login_id_is_different": 0, "login_id": None, "no_smtp_authentication": 1, "password": None})
        if not doc.is_new():
            from frappe.utils.password import remove_encrypted_password
            remove_encrypted_password("Email Account", doc.name, "password")
    if req.get("password"):
        doc.update({"password": req["password"], "no_smtp_authentication": 0})
    if not req.get("verify", True):
        # Email Account.validate stops before it connects to SMTP/IMAP while this is set.
        frappe.local.flags.in_patch = True
    doc.save(ignore_permissions=True)
    frappe.db.commit()
    res["account"] = describe(doc.name)
elif op == "unset":
    name = req["account_name"]
    if not frappe.db.exists("Email Account", name):
        res["changed"] = False
    else:
        doc = frappe.get_doc("Email Account", name)
        doc.update({"enable_outgoing": 0, "default_outgoing": 0})
        doc.save(ignore_permissions=True)
        frappe.db.commit()
        res["changed"] = True
print("` + resultPrefix + `" + json.dumps(res, default=str))
`

// MailAccount is an Email Account as ffm reports it. The password is never part of it.
type MailAccount struct {
	Name        string `json:"name"`
	EmailID     string `json:"email_id"`
	SMTPServer  string `json:"smtp_server"`
	SMTPPort    int    `json:"smtp_port"`
	UseTLS      int    `json:"use_tls"`
	UseSSL      int    `json:"use_ssl_for_outgoing"`
	LoginID     string `json:"login_id"`
	LoginDiffer int    `json:"login_id_is_different"`
	NoAuth      int    `json:"no_smtp_authentication"`
	HasPassword bool   `json:"has_password"`
}

// Security is starttls, ssl or none, from the account's two flags.
func (a MailAccount) Security() string {
	switch {
	case a.UseSSL != 0:
		return MailTLSSSL
	case a.UseTLS != 0:
		return MailTLSStartTLS
	}
	return MailTLSNone
}

// Login is the SMTP user name, or "" when the account does not authenticate.
func (a MailAccount) Login() string {
	if a.NoAuth != 0 {
		return ""
	}
	if a.LoginDiffer != 0 && a.LoginID != "" {
		return a.LoginID
	}
	return a.EmailID
}

// MailState is what `ffm mail show` reports.
type MailState struct {
	// Account is the default outgoing Email Account, nil when there is none.
	Account *MailAccount
	// SiteConfig holds the mail_* keys of site_config.json; Frappe falls back
	// to them only when no default outgoing account is enabled.
	SiteConfig map[string]any
	// Managed is true when ffm configured the account (the site_config marker).
	Managed bool
	// Effective says where Frappe will send mail: "account", "site_config" or "none".
	Effective string
}

// MailSetInput describes `ffm mail set`.
type MailSetInput struct {
	Bench    string
	SMTPHost string
	SMTPPort int    // 0 = the default for TLS
	TLS      string // starttls (default), ssl or none
	From     string // the account's email address
	User     string // SMTP login; empty = the From address when a password is given, no authentication otherwise
	Password string
	// AccountName names the Email Account and, when given, is also the name
	// recipients see as the sender. Empty reuses the account an earlier
	// 'ffm mail set' made, or DefaultMailAccount.
	AccountName string
	// NoVerify skips Frappe's test connection to the SMTP server on save.
	NoVerify bool
}

// ValidateMailSet checks and completes in, returning the normalised copy.
func ValidateMailSet(in MailSetInput) (MailSetInput, error) {
	if strings.TrimSpace(in.SMTPHost) == "" || strings.ContainsAny(in.SMTPHost, " \t\r\n/:") {
		return in, fmt.Errorf("--smtp-host: a host name such as smtp.example.com, not %q", in.SMTPHost)
	}
	if in.TLS == "" {
		in.TLS = MailTLSStartTLS
	}
	switch in.TLS {
	case MailTLSStartTLS, MailTLSSSL, MailTLSNone:
	default:
		return in, fmt.Errorf("--tls: starttls, ssl or none, not %q", in.TLS)
	}
	if in.SMTPPort == 0 {
		in.SMTPPort = DefaultSMTPPort(in.TLS)
	}
	if in.SMTPPort < 1 || in.SMTPPort > 65535 {
		return in, fmt.Errorf("--smtp-port: %d is not a port", in.SMTPPort)
	}
	a, err := mail.ParseAddress(in.From)
	if err != nil || a.Address != in.From || strings.ContainsAny(in.From, " \t,;<>") {
		return in, fmt.Errorf("--from: %q is not an email address", in.From)
	}
	if in.Password != "" {
		if err := singleLine("the SMTP password", in.Password); err != nil {
			return in, err
		}
	}
	for _, f := range []struct{ name, v string }{{"--user", in.User}, {"--name", in.AccountName}} {
		if strings.ContainsAny(f.v, "\x00\n\r") {
			return in, fmt.Errorf("%s cannot contain line breaks or NUL", f.name)
		}
	}
	return in, nil
}

type mailRequest struct {
	Op           string `json:"op"`
	AccountName  string `json:"account_name,omitempty"`
	EmailID      string `json:"email_id,omitempty"`
	SMTPServer   string `json:"smtp_server,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	TLS          string `json:"tls,omitempty"`
	Login        string `json:"login,omitempty"`
	Password     string `json:"password,omitempty"`
	NameAsSender bool   `json:"name_as_sender,omitempty"`
	Verify       bool   `json:"verify"`
}

func (s *Service) mailCall(b state.Bench, req mailRequest) (json.RawMessage, error) {
	var secrets []string
	if req.Password != "" {
		secrets = append(secrets, req.Password)
	}
	raw, err := runSiteRequest(s.quietRunnerFor(b), b.SiteName, mailScript, req, secrets...)
	if err != nil {
		return nil, fmt.Errorf("mail %s: %w", req.Op, err)
	}
	return raw, nil
}

func decodeAccount(raw json.RawMessage) (*MailAccount, error) {
	var res struct {
		Account *MailAccount `json:"account"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("unreadable result: %w", err)
	}
	return res.Account, nil
}

// MailSet creates or updates the site's default outgoing Email Account.
//
// Frappe sends through that account when one is enabled and marked default, and
// falls back to the mail_* keys of site_config.json only when none is
// (EmailAccount.find_outgoing, then find_from_config). On a dev bench those keys
// point at Mailpit, so the Mailpit keys are removed here too and a marker is
// left in site_config: ensureDevMail leaves a marked bench alone, so Mailpit
// does not come back on the next start. 'ffm mail unset' reverses all of it.
func (s *Service) MailSet(in MailSetInput) (*MailAccount, error) {
	in, err := ValidateMailSet(in)
	if err != nil {
		return nil, err
	}
	if in.User != "" && in.Password == "" {
		return nil, fmt.Errorf("--user needs the SMTP password: pass --password-stdin or set FFM_SMTP_PASSWORD")
	}
	release, err := s.lockBench(in.Bench)
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := s.runningBench(in.Bench)
	if err != nil {
		return nil, err
	}
	name := in.AccountName
	explicit := name != ""
	if !explicit {
		name = s.mailAccountName(b)
	}
	login := in.User
	if login == "" && in.Password != "" {
		login = in.From
	}
	raw, err := s.mailCall(b, mailRequest{Op: "set", AccountName: name, EmailID: in.From,
		SMTPServer: in.SMTPHost, SMTPPort: in.SMTPPort, TLS: in.TLS, Login: login, Password: in.Password,
		NameAsSender: explicit, Verify: !in.NoVerify})
	if err != nil {
		return nil, err
	}
	acct, err := decodeAccount(raw)
	if err != nil {
		return nil, fmt.Errorf("mail set: %w", err)
	}
	if acct == nil {
		return nil, fmt.Errorf("mail set: Frappe returned no account")
	}
	if err := s.dropDevMailpit(b); err != nil {
		return acct, fmt.Errorf("the Email Account is saved, but Mailpit's settings could not be removed from site_config: %w", err)
	}
	// The marker names the account Frappe really holds: an existing one for the
	// same address is reused whatever name was asked for.
	if _, err := s.configCall(b, configRequest{Op: "set", Key: mailMarkerKey, Value: acct.Name}); err != nil {
		return acct, fmt.Errorf("the Email Account is saved, but the marker could not be written to site_config: %w", err)
	}
	return acct, nil
}

// mailAccountName is the Email Account ffm manages on b: the one the marker
// names, else DefaultMailAccount.
func (s *Service) mailAccountName(b state.Bench) string {
	if res, err := s.configCall(b, configRequest{Op: "get", Key: mailMarkerKey}); err == nil && string(res["found"]) == "true" {
		var n string
		if json.Unmarshal(res["value"], &n) == nil && n != "" {
			return n
		}
	}
	return DefaultMailAccount
}

// devMailpitKeys are the site_config keys ensureDevMail writes.
var devMailpitKeys = []string{"mail_server", "mail_port", "use_tls", "disable_mail_smtp_authentication"}

// dropDevMailpit removes ensureDevMail's keys from a dev bench's site_config,
// but only while they are still Mailpit's: a mail_server the user set stays.
func (s *Service) dropDevMailpit(b state.Bench) error {
	if !hasMailpit(b) {
		return nil
	}
	res, err := s.configCall(b, configRequest{Op: "get", Key: "mail_server"})
	if err != nil {
		return err
	}
	if string(res["found"]) != "true" || string(res["value"]) != `"mailpit"` {
		return nil
	}
	for _, k := range devMailpitKeys {
		if _, err := s.configCall(b, configRequest{Op: "unset", Key: k}); err != nil {
			return err
		}
	}
	return nil
}

// MailShow reports where the site's outgoing mail goes.
func (s *Service) MailShow(name string) (MailState, error) {
	b, err := s.runningBench(name)
	if err != nil {
		return MailState{}, err
	}
	raw, err := s.mailCall(b, mailRequest{Op: "show"})
	if err != nil {
		return MailState{}, err
	}
	acct, err := decodeAccount(raw)
	if err != nil {
		return MailState{}, fmt.Errorf("mail show: %w", err)
	}
	var res struct {
		SiteConfig map[string]any `json:"site_config"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return MailState{}, fmt.Errorf("mail show: %w", err)
	}
	st := MailState{Account: acct, SiteConfig: res.SiteConfig, Effective: "none"}
	if _, ok := res.SiteConfig["mail_server"]; ok {
		st.Effective = "site_config"
	}
	if acct != nil {
		st.Effective = "account"
	}
	cfg, err := s.configCall(b, configRequest{Op: "get", Key: mailMarkerKey})
	if err == nil {
		st.Managed = string(cfg["found"]) == "true"
	}
	return st, nil
}

// MailUnset turns the ffm-managed Email Account off (it stays in Frappe,
// disabled and no longer the default) and, on a dev bench, points outgoing mail
// at Mailpit again. It reports whether anything changed.
func (s *Service) MailUnset(name, accountName string) (bool, error) {
	release, err := s.lockBench(name)
	if err != nil {
		return false, err
	}
	defer release()
	b, err := s.runningBench(name)
	if err != nil {
		return false, err
	}
	if accountName == "" {
		accountName = s.mailAccountName(b)
	}
	raw, err := s.mailCall(b, mailRequest{Op: "unset", AccountName: accountName})
	if err != nil {
		return false, err
	}
	var res struct {
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return false, fmt.Errorf("mail unset: %w", err)
	}
	marker, err := s.configCall(b, configRequest{Op: "unset", Key: mailMarkerKey})
	if err != nil {
		return res.Changed, err
	}
	if err := ensureDevMail(b); err != nil {
		return res.Changed, fmt.Errorf("could not point outgoing mail at Mailpit again: %w", err)
	}
	return res.Changed || string(marker["changed"]) == "true", nil
}

// MailRecords are the DNS records worth publishing for a From domain.
type MailRecord struct {
	Kind  string // SPF, DKIM or DMARC
	Type  string // TXT or CNAME
	Name  string
	Value string
	Note  string
}

// spfIncludes maps an SMTP host (suffix) to the SPF include its provider documents.
var spfIncludes = []struct{ suffix, include string }{
	{"smtp.sendgrid.net", "sendgrid.net"},
	{"smtp.mailgun.org", "mailgun.org"},
	{".amazonaws.com", "amazonses.com"},
	{"smtp.postmarkapp.com", "spf.mtasv.net"},
	{"smtp-relay.brevo.com", "spf.brevo.com"},
	{"smtp-relay.sendinblue.com", "spf.brevo.com"},
	{"smtp.gmail.com", "_spf.google.com"},
	{"smtp.office365.com", "spf.protection.outlook.com"},
}

// MailDNSRecords lists the SPF, DKIM and DMARC records for the domain of from.
// ffm cannot know a provider's DKIM key, so that record is a pointer to where
// the provider shows it.
func MailDNSRecords(from, smtpHost string) []MailRecord {
	i := strings.LastIndex(from, "@")
	if i < 0 {
		return nil
	}
	domain := strings.ToLower(from[i+1:])
	host := strings.ToLower(smtpHost)
	spf := "v=spf1 include:<your provider's SPF domain> ~all"
	note := "one SPF record per domain: merge the include into an existing v=spf1 record instead of adding a second"
	for _, p := range spfIncludes {
		if host == p.suffix || (strings.HasPrefix(p.suffix, ".") && strings.HasSuffix(host, p.suffix)) {
			spf = "v=spf1 include:" + p.include + " ~all"
			break
		}
	}
	return []MailRecord{
		{Kind: "SPF", Type: "TXT", Name: domain, Value: spf, Note: note},
		{Kind: "DKIM", Type: "TXT or CNAME", Name: "<selector>._domainkey." + domain,
			Value: "the value your mail provider shows when you add the domain", Note: "each provider issues its own selector and key"},
		{Kind: "DMARC", Type: "TXT", Name: "_dmarc." + domain,
			Value: "v=DMARC1; p=none; rua=mailto:postmaster@" + domain,
			Note:  "start with p=none and read the reports before moving to quarantine or reject"},
	}
}

// MailRecommendation is the advice printed after `ffm mail set`.
const MailRecommendation = "For anything beyond a test, send through a transactional mail provider (Amazon SES, " +
	"Postmark, Mailgun, Brevo, ...) rather than a mailbox: deliverability, bounce handling and sending limits are theirs to manage."
