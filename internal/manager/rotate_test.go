package manager

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	oldRoot  = "OldRoot-Sentinel-11"
	oldAdmin = "OldAdmin-Sentinel-22"
)

func rotateBench(t *testing.T, db string, tmpl int, rules ...fakeexec.Rule) (*Service, state.Bench, *fakeexec.Fake) {
	t.Helper()
	rules = append([]fakeexec.Rule{{Match: " ps ", Stdout: psRunning}}, rules...)
	fake := fakeexec.Install(t, rules...)
	s, b := newTestBench(t, state.Bench{Name: "rot", Mode: "dev", DBType: db, SiteName: "rot.localhost",
		DBPassword: oldRoot, AdminPassword: oldAdmin, TemplateVersion: tmpl})
	if err := bench.WriteDBRootSecret(b.Dir, oldRoot); err != nil {
		t.Fatal(err)
	}
	return s, b, fake
}

var hex24 = regexp.MustCompile(`^[0-9a-f]{24}$`)

func TestRotateCredentialsMariaDB(t *testing.T) {
	dir := t.TempDir()
	alterIn, verifyIn, adminIn := filepath.Join(dir, "alter"), filepath.Join(dir, "verify"), filepath.Join(dir, "admin")
	s, b, fake := rotateBench(t, "", 7,
		fakeexec.Rule{Match: "mysql.user", Stdout: "%\nlocalhost\n127.0.0.1\n"},
		fakeexec.Rule{Match: "mariadb -u root", StdinTo: alterIn},
		fakeexec.Rule{Match: "env/bin/python", StdinTo: verifyIn},
		fakeexec.Rule{Match: "set-admin-password", StdinTo: adminIn},
	)
	secret := bench.DBRootSecretPath(b.Dir)
	before, _ := os.Stat(secret)

	res, err := s.RotateCredentials(RotateInput{Name: "rot"}, DiscardProgress{})
	if err != nil {
		t.Fatalf("RotateCredentials: %v\ncalls:\n%s", err, strings.Join(fake.Calls(), "\n"))
	}
	if !res.DBRotated || !res.AdminRotated || !res.Verified || !hex24.MatchString(res.DBPassword) || !hex24.MatchString(res.AdminPassword) {
		t.Fatalf("result = %+v", res)
	}
	if res.DBPassword == res.AdminPassword || res.DBPassword == oldRoot {
		t.Error("passwords not distinct or not new")
	}

	// One ALTER per root host, localhost (the session's own) last, SQL on stdin.
	sql := readFile(t, alterIn)
	i127, ipct, iloc := strings.Index(sql, "'127.0.0.1'"), strings.Index(sql, "'%'"), strings.Index(sql, "'localhost'")
	if i127 < 0 || ipct < 0 || iloc < 0 || iloc < i127 || iloc < ipct || strings.Count(sql, "ALTER USER") != 3 {
		t.Errorf("ALTER batch:\n%s", sql)
	}
	if !strings.Contains(sql, "IDENTIFIED BY '"+res.DBPassword+"'") {
		t.Errorf("new password not in the ALTER batch:\n%s", sql)
	}
	// Secret file rewritten in place, state updated, new values verified/set.
	if got := readFile(t, secret); got != res.DBPassword {
		t.Errorf("secret file = %q", got)
	}
	after, _ := os.Stat(secret)
	if !os.SameFile(before, after) {
		t.Error("the secret file was replaced instead of rewritten in place")
	}
	rec, _ := s.GetBench("rot")
	if rec.DBPassword != res.DBPassword || rec.AdminPassword != res.AdminPassword {
		t.Errorf("state = %q / %q", rec.DBPassword, rec.AdminPassword)
	}
	if readFile(t, verifyIn) != res.DBPassword+"\n" || readFile(t, adminIn) != res.AdminPassword+"\n" {
		t.Error("verification / admin password did not arrive on stdin")
	}
	noSecretOnArgv(t, fake, oldRoot, oldAdmin, res.DBPassword, res.AdminPassword)
}

func TestRotateCredentialsFailedAlterChangesNothing(t *testing.T) {
	s, b, fake := rotateBench(t, "", 7,
		fakeexec.Rule{Match: "mysql.user", Stdout: "%\nlocalhost\n"},
		fakeexec.Rule{Match: "mariadb -u root", Exit: 1, Stdout: "ERROR 1396 (HY000): Operation ALTER USER failed\n"},
	)
	_, err := s.RotateCredentials(RotateInput{Name: "rot", DBRoot: true}, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "ALTER USER failed") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, bench.DBRootSecretPath(b.Dir)); got != oldRoot {
		t.Errorf("secret file changed to %q", got)
	}
	rec, _ := s.GetBench("rot")
	if rec.DBPassword != oldRoot || rec.AdminPassword != oldAdmin {
		t.Errorf("state changed: %q / %q", rec.DBPassword, rec.AdminPassword)
	}
	if fake.Called("set-admin-password") {
		t.Error("the Administrator password was touched after a failed database change")
	}
}

func TestRotateCredentialsPostgres(t *testing.T) {
	alterIn := filepath.Join(t.TempDir(), "alter")
	s, b, fake := rotateBench(t, "postgres", 7,
		fakeexec.Rule{Match: "psql", StdinTo: alterIn},
		fakeexec.Rule{Match: "env/bin/python"},
	)
	res, err := s.RotateCredentials(RotateInput{Name: "rot", DBRoot: true}, DiscardProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AdminRotated {
		t.Error("--db-root also rotated the Administrator password")
	}
	if sql := readFile(t, alterIn); sql != "ALTER ROLE postgres WITH PASSWORD '"+res.DBPassword+"';\n" {
		t.Errorf("psql stdin = %q", sql)
	}
	if !fake.Called("psql -U postgres -v ON_ERROR_STOP=1") || readFile(t, bench.DBRootSecretPath(b.Dir)) != res.DBPassword {
		t.Errorf("calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	noSecretOnArgv(t, fake, oldRoot, res.DBPassword)
}

func TestRotateCredentialsRefusals(t *testing.T) {
	// Stopped: nothing runs but the status query.
	s, _, fake := rotateBench(t, "", 7)
	fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: ""})
	if _, err := s.RotateCredentials(RotateInput{Name: "rot"}, DiscardProgress{}); !errors.Is(err, ErrBenchStopped) {
		t.Errorf("stopped: err = %v", err)
	}
	_ = fake

	// A bench from an older template cannot rotate its root password, but its
	// Administrator password is fair game.
	adminIn := filepath.Join(t.TempDir(), "admin")
	s, b, fake := rotateBench(t, "", 6, fakeexec.Rule{Match: "set-admin-password", StdinTo: adminIn})
	var pre *PreflightError
	if _, err := s.RotateCredentials(RotateInput{Name: "rot"}, DiscardProgress{}); !errors.As(err, &pre) {
		t.Fatalf("old template: err = %v", err)
	}
	if fake.Called("set-admin-password") || readFile(t, bench.DBRootSecretPath(b.Dir)) != oldRoot {
		t.Error("something changed before the refusal")
	}
	res, err := s.RotateCredentials(RotateInput{Name: "rot", Admin: true}, DiscardProgress{})
	if err != nil || !res.AdminRotated || res.DBRotated {
		t.Fatalf("admin only: %+v %v", res, err)
	}
	if readFile(t, adminIn) != res.AdminPassword+"\n" {
		t.Error("admin password not on stdin")
	}
}
