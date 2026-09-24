package bench

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestShellQuoteSurvivesBash runs each value through a real shell: what the
// command receives must be exactly what ffm recorded.
func TestShellQuoteSurvivesBash(t *testing.T) {
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, v := range []string{
		"plain", "with space", "dollar$HOME", "$(touch /tmp/ffm-pwned)", "`id`",
		"it's", `back\slash`, `dq"uote`, "semi;colon&amp|pipe>redir", "",
	} {
		out, err := exec.Command(sh, "-c", "printf %s "+ShellQuote(v)).Output()
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("ShellQuote(%q) reached the command as %q", v, out)
		}
	}
}

func TestValidatePasswords(t *testing.T) {
	for _, ok := range []string{"ffm123456", "Str0ng!pw#2026", "a&b|c;d(e)<f>", "it's"} {
		if err := ValidateDBPassword(ok); err != nil {
			t.Errorf("ValidateDBPassword(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "has space", "dollar$x", `quo"te`, `back\slash`, "tab\there"} {
		if ValidateDBPassword(bad) == nil {
			t.Errorf("ValidateDBPassword(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"admin", "with space", "$(anything)", `"quoted"`} {
		if err := ValidateAdminPassword(ok); err != nil {
			t.Errorf("ValidateAdminPassword(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "two\nlines", "nul\x00"} {
		if ValidateAdminPassword(bad) == nil {
			t.Errorf("ValidateAdminPassword(%q) accepted", bad)
		}
	}
}

func TestAdminPasswordLeadingDash(t *testing.T) {
	if ValidateAdminPassword("-x") == nil {
		t.Error("a leading dash was accepted")
	}
}

// TestCommandsQuoteHostileValues runs the commands ffm builds from user input
// through a real shell, with a payload that must never execute.
func TestCommandsQuoteHostileValues(t *testing.T) {
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	marker := t.TempDir() + "/pwned"
	payload := "x';touch " + marker + ";'%s$(touch " + marker + ")"

	// The credentials command, with printf and git swapped for echo-safe stubs.
	cmd := GitCredentialsCmd(payload)
	cmd = strings.Replace(cmd, " > /tmp/.git-credentials", " > "+t.TempDir()+"/creds", 1)
	cmd = strings.Replace(cmd, "git config --global", "true", 1)
	if out, err := exec.Command(sh, "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("credentials command failed: %v\n%s", err, out)
	}
	// The app command, with bench stubbed to print its arguments.
	app := AppSpec{Source: "https://github.com/x/y;touch " + marker, Branch: "main$(touch " + marker + ")"}
	get := strings.Replace(app.GetAppCmd(), "bench get-app", "printf '%s|'", 1)
	out, err := exec.Command(sh, "-c", get).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "--branch|" + app.Branch + "|" + app.Source + "|"; string(out) != want {
		t.Errorf("get-app received %q, want %q", out, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a payload executed")
	}
}

// TestGitCredentialsWritesTokenVerbatim: a token with ', % or $(...) must land
// in the credential store byte for byte — % inside printf's format used to be
// read as a directive.
func TestGitCredentialsWritesTokenVerbatim(t *testing.T) {
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	tok := "ghp_a'b%sc$(x)\\d"
	cmd := strings.Replace(GitCredentialsCmd(tok), "/tmp/.git-credentials", dir+"/c", 1)
	cmd = strings.Replace(cmd, "git config --global", "true", 1)
	if out, err := exec.Command(sh, "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got, _ := os.ReadFile(dir + "/c")
	if want := "https://x-oauth-basic:" + tok + "@github.com\n"; string(got) != want {
		t.Fatalf("credential store = %q, want %q", got, want)
	}
}
