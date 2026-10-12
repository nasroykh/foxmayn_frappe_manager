package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	got := Scrub("pw=s3cret-db root=s3cret-db short=abc", "s3cret-db", "abc", "")
	if got != "pw=*** root=*** short=abc" {
		t.Fatalf("Scrub = %q", got)
	}
}

// MariaDBRootArgs must hand the SQL to mariadb byte for byte (it contains
// backticks, which bash would otherwise execute) and pass the password through
// the environment, never as an argument.
func TestMariaDBRootArgsKeepsPasswordOutOfArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\nprintf 'pwd=%s\\n' \"$MYSQL_PWD\"\nfor a in \"$@\"; do printf 'arg=%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, "mariadb"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	sql := "SELECT COUNT(*) FROM `db`.`tabError Log` WHERE `creation` < NOW(); $(touch pwned)"
	args := MariaDBRootArgs(sql, "-N")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MYSQL_ROOT_PASSWORD=s3cret-root")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "pwd=s3cret-root\n") {
		t.Errorf("password not passed through MYSQL_PWD:\n%s", got)
	}
	if !strings.Contains(got, "arg="+sql+"\n") || !strings.Contains(got, "arg=-N\n") {
		t.Errorf("SQL or flags not passed verbatim:\n%s", got)
	}
	if strings.Contains(strings.Join(args, " "), "s3cret-root") {
		t.Error("password appears in the argument list")
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("SQL was executed by the shell")
	}
}

func TestRunnerScrubsExecOutput(t *testing.T) {
	r := &Runner{Redact: []string{"s3cret-db"}}
	if got := r.scrub("Access denied for root using password s3cret-db"); strings.Contains(got, "s3cret-db") {
		t.Fatalf("not scrubbed: %q", got)
	}
}

// A bench from template 7 on has MARIADB_ROOT_PASSWORD_FILE and no
// MYSQL_ROOT_PASSWORD: the password must come from the file, still not argv.
func TestMariaDBRootArgsReadsSecretFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\nprintf 'pwd=%s\\n' \"$MYSQL_PWD\"\n"
	if err := os.WriteFile(filepath.Join(dir, "mariadb"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "pw")
	if err := os.WriteFile(secret, []byte("from-file-pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := MariaDBRootArgs("SELECT 1")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"),
		"MARIADB_ROOT_PASSWORD_FILE="+secret, "MYSQL_ROOT_PASSWORD=stale-env")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(string(out), "pwd=from-file-pw\n") {
		t.Errorf("secret file not used:\n%s", out)
	}
	if strings.Contains(strings.Join(args, " "), "from-file-pw") {
		t.Error("password appears in the argument list")
	}
}
