package manager

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// mailpitTemplateVersion is the first template version whose dev compose file
// has the mailpit service. Older benches get it from `ffm reconcile`.
const mailpitTemplateVersion = 2

// hasMailpit reports whether the bench's compose file runs Mailpit.
func hasMailpit(b state.Bench) bool {
	return b.IsDev() && b.TemplateVersion >= mailpitTemplateVersion
}

// ensureDevMail points the site's outgoing mail at the bench's Mailpit, unless
// site_config already names a mail server. Frappe uses these keys only when no
// Email Account is the default outgoing one (EmailAccount.find_from_config),
// so an account the user configured still wins.
func ensureDevMail(b state.Bench) error {
	if !hasMailpit(b) {
		return nil
	}
	return patchSiteConfig(b, func(cfg map[string]any) {
		if _, ok := cfg["mail_server"]; ok {
			return
		}
		cfg["mail_server"] = "mailpit"
		cfg["mail_port"] = 1025
		cfg["use_tls"] = 0
		cfg["disable_mail_smtp_authentication"] = 1
	})
}

// browserHost is where a browser on this machine reaches a bench's published
// ports, whether they are bound to 127.0.0.1 or to every interface.
func browserHost() string { return "localhost" }

// SiteURL is the URL that opens the bench's site in a browser.
func (s *Service) SiteURL(name string) (string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return "", err
	}
	return siteURL(b, proxy.IsRunning()), nil
}

func siteURL(b state.Bench, proxyRunning bool) string {
	switch {
	case b.IsProd() && b.ProxyHost != "":
		return b.ProxyHost
	case b.IsProd():
		return "https://" + b.Domain
	case b.ProxyHost != "":
		return b.ProxyHost
	case proxyRunning:
		return "http://" + b.SiteName
	}
	return fmt.Sprintf("http://%s:%d", browserHost(), b.WebPort)
}

// MailURL is the URL of a dev bench's Mailpit web UI.
func (s *Service) MailURL(name string) (string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return "", err
	}
	if !b.IsDev() {
		return "", fmt.Errorf("bench %q is a production bench; Mailpit runs on dev benches only", name)
	}
	if !hasMailpit(b) {
		return "", fmt.Errorf("bench %q was created before ffm ran Mailpit; run 'ffm reconcile %s' to add it", name, name)
	}
	return fmt.Sprintf("http://%s:%d", browserHost(), b.WebPort+bench.MailPortOffset), nil
}

// loginScript starts a session for argv[2] on site argv[1] and prints its sid.
// It is what `bench browse --user` does, minus opening a browser inside the
// container, and it commits so the session row survives the process. The user
// arrives through argv, never through the script text.
const loginScript = `import sys, frappe
from frappe.auth import CookieManager, LoginManager
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
if not frappe.db.exists("User", sys.argv[2]):
    sys.exit("user %s does not exist" % sys.argv[2])
frappe.utils.set_request(path="/")
frappe.local.cookie_manager = CookieManager()
frappe.local.login_manager = LoginManager()
frappe.local.login_manager.login_as(sys.argv[2])
frappe.db.commit()
print("FFM_SID=" + frappe.session.sid)
`

// LoginURL starts a session as user (Administrator when empty) on a dev bench
// and returns a URL that opens the desk already logged in. The URL carries the
// session id, so it is a credential: callers show it only on request.
func (s *Service) LoginURL(name, user string) (string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return "", err
	}
	if !b.IsDev() {
		return "", fmt.Errorf("ffm login works on dev benches only: it would hand out a session on a production site")
	}
	if user == "" {
		user = "Administrator"
	}
	runner := s.runnerFor(b)
	out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench/sites && /workspace/frappe-bench/env/bin/python -c "+bench.ShellQuote(loginScript)+
			" "+bench.ShellQuote(b.SiteName)+" "+bench.ShellQuote(user))
	if err != nil {
		return "", fmt.Errorf("start a session: %w\n%s", err, lastLines(out, 5))
	}
	sid := ""
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "FFM_SID="); ok {
			sid = v
		}
	}
	if sid == "" {
		return "", fmt.Errorf("start a session: no session id in the output\n%s", lastLines(out, 5))
	}
	return siteURL(b, proxy.IsRunning()) + "/app?sid=" + sid, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Interactive runs a bench command in the frappe container with the terminal
// attached (ffm console, ffm db).
func (s *Service) Interactive(name string, args ...string) error {
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	return s.runnerFor(b).ExecInDir("frappe", "/workspace/frappe-bench", args...)
}

// SiteCommand is `bench --site <site> <args…>` for a bench.
func (s *Service) SiteCommand(name string, args ...string) ([]string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return nil, err
	}
	return append([]string{"bench", "--site", b.SiteName}, args...), nil
}

// DBExportInput writes the site's database to a file on the host.
type DBExportInput struct {
	Name string
	// Path is the destination. A name ending in .gz gets the gzip dump as
	// Frappe wrote it; anything else is decompressed to plain SQL.
	Path string
	// Force overwrites an existing file.
	Force bool
}

// DBExport dumps the site's database with `bench backup` and copies it out.
func (s *Service) DBExport(in DBExportInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	if in.Path == "" {
		return fmt.Errorf("an export path is required")
	}
	if _, err := os.Stat(in.Path); err == nil && !in.Force {
		return fmt.Errorf("%s already exists (pass --force to overwrite)", in.Path)
	}
	runner := s.runnerFor(b)
	remote := fmt.Sprintf("/tmp/ffm-db-export-%d.sql.gz", time.Now().UnixNano())
	defer runner.ExecSilent("frappe", "rm", "-f", remote) //nolint:errcheck // best-effort cleanup

	pw.Step("Dumping the database")
	cmd := "cd /workspace/frappe-bench && bench --site " + bench.ShellQuote(b.SiteName) +
		" backup --verbose --compress --backup-path-db " + remote
	if out, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
		return fmt.Errorf("bench backup: %w\n%s", err, benchBackupFailure(out, s.Verbose))
	}

	tmp := in.Path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	var w io.Writer = f
	var gz *gzipToPlain
	if !strings.HasSuffix(in.Path, ".gz") {
		gz = newGzipToPlain(f)
		w = gz
	}
	pw.Step("Copying it to " + in.Path)
	if err := runner.ExecStream("frappe", w, "cat", remote); err != nil {
		f.Close()
		return fmt.Errorf("copy the dump out: %w", err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			f.Close()
			return fmt.Errorf("decompress the dump: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, in.Path); err != nil {
		return err
	}
	pw.Printf("Database of %q written to %s (it holds every password hash and API secret of the site).\n", b.Name, in.Path)
	return nil
}

// gzipToPlain is a Writer that decompresses the gzip stream written to it.
type gzipToPlain struct {
	pw   *io.PipeWriter
	done chan error
}

func newGzipToPlain(dst io.Writer) *gzipToPlain {
	pr, pw := io.Pipe()
	g := &gzipToPlain{pw: pw, done: make(chan error, 1)}
	go func() {
		zr, err := gzip.NewReader(pr)
		if err == nil {
			_, err = io.Copy(dst, zr)
		}
		pr.CloseWithError(err)
		g.done <- err
	}()
	return g
}

func (g *gzipToPlain) Write(p []byte) (int, error) { return g.pw.Write(p) }

func (g *gzipToPlain) Close() error {
	g.pw.Close()
	return <-g.done
}

// DBImportInput replaces the site's database with a dump.
type DBImportInput struct {
	Name string
	// Path is a .sql or .sql.gz dump, such as `ffm db --export` or
	// `bench backup` writes.
	Path string
	// Migrate runs `bench migrate` afterwards, for a dump from another
	// version of the apps.
	Migrate bool
}

// DBImport replaces the site's database with the dump at in.Path through
// `bench restore`, which drops and recreates the database first. Files and
// site_config stay as they are, so a dump from another site keeps this site's
// encryption key: its Password fields will not decrypt.
func (s *Service) DBImport(in DBImportInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	st, err := os.Stat(in.Path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file", in.Path)
	}
	ext := ".sql"
	if strings.HasSuffix(in.Path, ".gz") {
		ext = ".sql.gz"
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	runner := s.runnerFor(b)
	remote := fmt.Sprintf("/tmp/ffm-db-import-%d%s", time.Now().UnixNano(), ext)
	pw.Step("Copying " + filepath.Base(in.Path) + " into the bench")
	if err := runner.ExecStdin("frappe", f, "bash", "-c", "umask 077 && cat > "+remote); err != nil {
		return fmt.Errorf("copy the dump in: %w", err)
	}
	defer runner.ExecSilent("frappe", "rm", "-f", remote) //nolint:errcheck // best-effort cleanup

	rootUser := "root"
	if b.IsPostgres() {
		rootUser = "postgres"
	}
	q := bench.ShellQuote
	pw.Step("Replacing the database of site " + b.SiteName)
	// The root password arrives on stdin, so it is on no host command line.
	// bench itself still takes it as an argument inside the container.
	cmd := fmt.Sprintf(`cd /workspace/frappe-bench && IFS= read -r p && bench --site %s restore %s --db-root-username %s --db-root-password "$p" --force`,
		q(b.SiteName), q(remote), rootUser)
	if err := runner.ExecStdin("frappe", strings.NewReader(b.DBPassword+"\n"), "bash", "-c", cmd); err != nil {
		return fmt.Errorf("bench restore: %w", err)
	}
	if err := afterDatabaseSwap(runner, b, in.Migrate, pw); err != nil {
		return err
	}
	pw.Printf("Database of %q replaced from %s.\n", b.Name, in.Path)
	if !in.Migrate {
		pw.Println("  If the dump came from other app versions, run: ffm db " + b.Name + " --import … --migrate, or bench migrate in 'ffm shell'.")
	}
	return nil
}
