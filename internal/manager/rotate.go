package manager

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// secretsTemplate is the first template whose database container reads its
// root password from ./secrets/db_root_password. An older bench has the
// password inline in its compose file and container environment, which a
// rotation could not change without recreating the container.
const secretsTemplate = 7

// RotateInput selects what RotateCredentials changes. Neither flag set means
// both.
type RotateInput struct {
	Name   string
	DBRoot bool
	Admin  bool
}

// RotateResult carries the NEW values, for the caller to show or hide.
type RotateResult struct {
	Name          string
	DBRotated     bool
	AdminRotated  bool
	DBPassword    string
	AdminPassword string
	// Verified is true when the new root password was used to open a database
	// connection from the frappe container, the way Frappe itself connects.
	Verified bool
}

// RotateCredentials replaces the bench's database root password and/or its
// Administrator password with freshly generated ones.
//
// The bench must be running: both changes are made through its containers. The
// database change goes ALTER USER first (the old password still works to run
// it), then the secret file is rewritten in place, then the state record is
// updated, then the new password is used for real. The site's own database
// user is not touched: Frappe connects with that, not with root.
//
// If anything after the ALTER fails, the database already has the new password
// and ffm's records do not: the new value is printed to stderr before the
// error is returned, because it exists nowhere else.
func (s *Service) RotateCredentials(in RotateInput, pw ProgressWriter) (RotateResult, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	if !in.DBRoot && !in.Admin {
		in.DBRoot, in.Admin = true, true
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return RotateResult{}, err
	}
	defer release()
	b, err := s.GetBench(in.Name)
	if err != nil {
		return RotateResult{}, err
	}
	res := RotateResult{Name: b.Name}
	if st := s.LiveStatus(b); st != StatusRunning {
		return res, fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}
	if in.DBRoot && b.TemplateVersion < secretsTemplate {
		return res, &PreflightError{msg: fmt.Sprintf("bench %q was built from template %d, whose database container "+
			"keeps the root password in its environment; run 'ffm reconcile %s' first, then rotate "+
			"(or rotate only the Administrator password with --admin)", b.Name, b.TemplateVersion, b.Name)}
	}

	if in.DBRoot {
		if err := s.rotateDBRoot(b, &res, pw); err != nil {
			return res, err
		}
		b.DBPassword = res.DBPassword
	}
	if in.Admin {
		if err := s.rotateAdmin(b, &res, pw); err != nil {
			return res, err
		}
	}
	return res, nil
}

// tellNewValue prints a password that exists nowhere but in memory.
func tellNewValue(what, value string) {
	fmt.Fprintf(os.Stderr, "\nThe new %s is already active but ffm could not record it. Note it now:\n  %s\n", what, value)
}

func (s *Service) rotateDBRoot(b state.Bench, res *RotateResult, pw ProgressWriter) error {
	newPw, err := randomPassword()
	if err != nil {
		return err
	}
	old := b.DBPassword
	runner := s.runnerFor(b)
	runner.Redact = append(runner.Redact, newPw)

	pw.Step("Changing the database root password")
	if b.IsPostgres() {
		sql := "ALTER ROLE postgres WITH PASSWORD " + sqlString(newPw) + ";\n"
		out, err := runner.ExecStdinOut("postgres", strings.NewReader(sql),
			"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-q")
		if err != nil {
			return fmt.Errorf("ALTER ROLE failed, nothing was changed: %w\n%s", err, lastLines(out, 5))
		}
	} else {
		hosts, err := mariaDBRootHosts(runner)
		if err != nil {
			return err
		}
		var up, down strings.Builder
		for _, h := range hosts {
			fmt.Fprintf(&up, "ALTER USER 'root'@%s IDENTIFIED BY %s;\n", sqlString(h), sqlString(newPw))
			fmt.Fprintf(&down, "ALTER USER 'root'@%s IDENTIFIED BY %s;\n", sqlString(h), sqlString(old))
		}
		out, err := runner.ExecStdinOut("mariadb", strings.NewReader(up.String()), bench.MariaDBRootStdinArgs()...)
		if err != nil {
			// DCL is not transactional: hosts before the failing one may have
			// changed. The session's own host (localhost) goes last, so the old
			// password still opens a connection to put them back.
			msg := fmt.Sprintf("ALTER USER failed: %v\n%s", err, lastLines(out, 5))
			if rout, rerr := runner.ExecStdinOut("mariadb", strings.NewReader(down.String()), bench.MariaDBRootStdinArgs()...); rerr != nil {
				return fmt.Errorf("%s\nreverting the hosts that may have changed also failed (%v):\n%s\n"+
					"check each root account's password before relying on %q", msg, rerr, lastLines(rout, 5), b.Name)
			}
			return fmt.Errorf("%s\nevery root account is back on the old password; nothing was changed", msg)
		}
	}
	res.DBRotated, res.DBPassword = true, newPw

	pw.Step("Recording the new root password")
	if err := bench.WriteDBRootSecret(b.Dir, newPw); err != nil {
		tellNewValue("database root password", newPw)
		return fmt.Errorf("write the secret file: %w", err)
	}
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) { rec.DBPassword = newPw }); err != nil {
		tellNewValue("database root password", newPw)
		return fmt.Errorf("update state: %w", err)
	}

	pw.Step("Verifying the new root password from the frappe container")
	script := verifyMariaDBScript
	if b.IsPostgres() {
		script = verifyPostgresScript
	}
	if out, err := runner.ExecStdinOut("frappe", strings.NewReader(newPw+"\n"),
		"bash", "-c", "cd /workspace/frappe-bench && env/bin/python -c "+bench.ShellQuote(script)); err != nil {
		return fmt.Errorf("the root password was changed and recorded, but a connection with it from the "+
			"frappe container failed: %w\n%s", err, lastLines(out, 5))
	}
	res.Verified = true
	return nil
}

// The verification connects over the bench network as root, the way Frappe's
// own setup code does. The password arrives on stdin.
const (
	verifyMariaDBScript = `import sys, pymysql
pymysql.connect(host="mariadb", user="root", password=sys.stdin.readline().rstrip("\n")).close()`
	verifyPostgresScript = `import sys, psycopg2
psycopg2.connect(host="postgres", user="postgres", dbname="postgres", password=sys.stdin.readline().rstrip("\n")).close()`
)

func (s *Service) rotateAdmin(b state.Bench, res *RotateResult, pw ProgressWriter) error {
	newPw, err := randomPassword()
	if err != nil {
		return err
	}
	pw.Step("Changing the Administrator password")
	runner := s.runnerFor(b)
	runner.Redact = append(runner.Redact, newPw)
	if err := setAdminPassword(runner, b.SiteName, newPw); err != nil {
		return fmt.Errorf("set the Administrator password: %w", err)
	}
	res.AdminRotated, res.AdminPassword = true, newPw
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) { rec.AdminPassword = newPw }); err != nil {
		tellNewValue("Administrator password", newPw)
		return fmt.Errorf("update state: %w", err)
	}
	return nil
}

var rootHostRe = regexp.MustCompile(`^[A-Za-z0-9._:%/-]+$`)

// mariaDBRootHosts lists the hosts MariaDB has a root account for, the
// session's own (localhost) last. Every one must change: Frappe connects as
// root@% over the bench network, a shell in the container as root@localhost.
func mariaDBRootHosts(runner *bench.Runner) ([]string, error) {
	out, err := runner.ExecSilent("mariadb",
		bench.MariaDBRootArgs("SELECT Host FROM mysql.user WHERE User='root'", "-N", "-B")...)
	if err != nil {
		return nil, fmt.Errorf("list the root accounts: %w\n%s", err, lastLines(out, 5))
	}
	seen := map[string]bool{}
	var hosts []string
	for _, line := range strings.Split(out, "\n") {
		h := strings.TrimSpace(line)
		if h == "" || seen[h] {
			continue
		}
		if !rootHostRe.MatchString(h) {
			return nil, fmt.Errorf("unexpected line while listing root accounts: %q", h)
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("MariaDB lists no root account")
	}
	sort.SliceStable(hosts, func(i, j int) bool { return hosts[j] == "localhost" && hosts[i] != "localhost" })
	return hosts, nil
}

// sqlString quotes s as a SQL string literal.
func sqlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}
